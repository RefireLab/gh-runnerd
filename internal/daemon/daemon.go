package daemon

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/RefireLab/gh-runnerd/internal/config"
	"github.com/RefireLab/gh-runnerd/internal/doctor"
	"github.com/RefireLab/gh-runnerd/internal/ghapi"
	"github.com/RefireLab/gh-runnerd/internal/githubutil"
	"github.com/RefireLab/gh-runnerd/internal/guest"
	"github.com/RefireLab/gh-runnerd/internal/images"
	"github.com/RefireLab/gh-runnerd/internal/netbridge"
	"github.com/RefireLab/gh-runnerd/internal/pool"
	"github.com/RefireLab/gh-runnerd/internal/qemu"
	"github.com/RefireLab/gh-runnerd/internal/registry"
	"github.com/RefireLab/gh-runnerd/internal/selftest"
	"github.com/RefireLab/gh-runnerd/internal/tlsutil"
	"github.com/RefireLab/gh-runnerd/internal/version"
)

// Daemon is the long-running gh-runnerd process.
type Daemon struct {
	Cfg     config.Config
	Log     *slog.Logger
	client  *ghapi.Client
	pool    *pool.Manager
	host    *guest.Host
	dhcp    *netbridge.DHCP
	vsockOK bool
	// egress is "ok", "failed", or "unknown" (probe skipped). Runners are
	// held back only on "failed": booting them would register Offline
	// garbage in GitHub.
	egress atomic.Value
}

func New(cfg config.Config, log *slog.Logger) *Daemon {
	return &Daemon{Cfg: cfg, Log: log}
}

func (d *Daemon) Run(ctx context.Context) error {
	if err := d.Cfg.Layout().Ensure(); err != nil {
		return err
	}
	rep := doctor.Run(d.Cfg)
	d.Log.Info("doctor", "report", rep.String())
	if rep.HasErrors() {
		return fmt.Errorf("doctor found blocking problems; fix them or see gh-runnerd doctor")
	}

	netCfg := netbridge.Config{
		Bridge:        d.Cfg.Network.Bridge,
		CIDR:          d.Cfg.Network.CIDR,
		HostIP:        d.Cfg.Network.HostIP,
		RegistryPort:  d.Cfg.Network.RegistryPort,
		RegistryLocal: d.Cfg.RegistryLocalPort(),
		GuestPort:     d.Cfg.Network.GuestPort,
	}
	if err := netbridge.Setup(netCfg); err != nil {
		return fmt.Errorf("network bridge: %w", err)
	}

	bundle, err := tlsutil.Load(d.Cfg.Layout().CA)
	if err != nil {
		return fmt.Errorf("load CA: %w", err)
	}
	cert, err := tls.X509KeyPair(bundle.ServerCertPEM, bundle.ServerKeyPEM)
	if err != nil {
		return err
	}

	pinned := registry.OpenPinned(d.Cfg.Layout(), d.Cfg.PinnedQuotaBytes())
	cache := registry.OpenCache(d.Cfg.Layout(), d.Cfg.CacheQuotaBytes())
	reg := &registry.Server{
		Pinned: pinned,
		Cache:  cache,
		Auth: registry.UpstreamAuth{
			DockerHubUser:  firstNonEmpty(d.Cfg.Registry.DockerHubUsername, d.Cfg.GitHub.DockerHubUsername),
			DockerHubToken: firstNonEmpty(d.Cfg.Registry.DockerHubToken, d.Cfg.GitHub.DockerHubToken),
		},
		Log: d.Log,
	}

	regListen := d.Cfg.RegistryListen()
	ln, err := tls.Listen("tcp", regListen, &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	})
	if err != nil {
		if strings.Contains(err.Error(), "address already in use") {
			return fmt.Errorf("registry listen %s: %w — another service owns this port; set registry.listen to a free port (e.g. %s:%d) or re-run gh-runnerd init",
				regListen, err, d.Cfg.Network.HostIP, netbridge.DefaultRegistryLocalPort)
		}
		return fmt.Errorf("registry listen %s: %w (the bridge must own %s)", regListen, err, d.Cfg.Network.HostIP)
	}
	regSrv := &http.Server{Handler: reg.Handler()}
	go func() {
		d.Log.Info("registry listening", "addr", regListen)
		if err := regSrv.Serve(ln); err != nil && err != http.ErrServerClosed {
			d.Log.Error("registry server", "err", err)
		}
	}()

	d.dhcp = netbridge.NewDHCP(d.Log)
	go func() {
		for {
			err := d.dhcp.ListenAndServe(d.Cfg.Network.Bridge)
			if ctx.Err() != nil {
				return
			}
			holder := netbridge.WhoBindsUDP(67)
			if holder != "" {
				d.Log.Error("dhcp server down — VMs cannot get an IP", "err", err,
					"holder", holder,
					"fix", "free UDP :67 (e.g. dnsmasq: add except-interface="+d.Cfg.Network.Bridge+" or bind-interfaces) — retrying in 15s")
			} else {
				d.Log.Error("dhcp server down — VMs cannot get an IP", "err", err, "fix", "check ss -ulpn 'sport = :67' — retrying in 15s")
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(15 * time.Second):
			}
		}
	}()

	d.host = guest.NewHost(d.Log)
	if err := d.host.ListenTCP(d.Cfg.Network.HostIP, d.Cfg.Network.GuestPort); err != nil {
		return fmt.Errorf("guest tcp: %w", err)
	}
	if err := d.host.ListenVsock(uint32(d.Cfg.Network.GuestPort)); err != nil {
		d.Log.Warn("vsock listen failed, using TCP on the isolated bridge only", "err", err)
	} else {
		d.vsockOK = true
	}

	d.client = ghapi.New(d.Cfg.GitHub)
	backend := &liveBackend{d: d}
	d.pool = pool.New(d.Cfg, d.Log, backend)

	mux := http.NewServeMux()
	mux.HandleFunc(d.Cfg.Webhook.Path, d.handleWebhook)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = io.WriteString(w, "ok")
	})
	wh := &http.Server{Addr: d.Cfg.Webhook.Listen, Handler: mux}
	go func() {
		d.Log.Info("webhook listening", "addr", d.Cfg.Webhook.Listen)
		if err := wh.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			d.Log.Error("webhook server", "err", err)
		}
	}()

	ticker := time.NewTicker(d.Cfg.GitHub.PollInterval.Duration)
	if d.Cfg.GitHub.PollInterval.Duration <= 0 {
		ticker = time.NewTicker(15 * time.Second)
	}
	defer ticker.Stop()
	statusTick := time.NewTicker(3 * time.Second)
	defer statusTick.Stop()

	d.cleanupOrphans()
	d.runSelftest()

	if d.egressState() != "failed" {
		if err := d.pool.MaintainIdle(ctx); err != nil {
			d.Log.Warn("warm pool", "err", err)
		}
	}

	d.Log.Info("gh-runnerd running", "version", version.Version, "labels", d.Cfg.Runner.Labels, "egress", d.egressState())

	for {
		select {
		case <-ctx.Done():
			d.Log.Info("shutting down: destroying pool VMs")
			d.pool.DestroyAll()
			_ = wh.Shutdown(context.Background())
			_ = regSrv.Shutdown(context.Background())
			_ = d.host.Close()
			_ = d.dhcp.Close()
			return nil
		case <-ticker.C:
			if d.egressState() == "failed" {
				d.runSelftest()
				if d.egressState() == "failed" {
					continue
				}
				d.Log.Info("egress restored — starting runners")
			}
			d.poll(ctx)
			if err := d.pool.MaintainIdle(ctx); err != nil {
				d.Log.Warn("maintain idle", "err", err)
			}
		case <-statusTick.C:
			d.writeStatus()
		}
	}
}

func (d *Daemon) egressState() string {
	if v, ok := d.egress.Load().(string); ok {
		return v
	}
	return "unknown"
}

// runSelftest probes the VM datapath (DHCP, control port, DNS, TCP 443)
// through a namespace attached to the real bridge, then gates runner
// creation on the result so broken networking cannot mint Offline runners.
func (d *Daemon) runSelftest() {
	rep := selftest.Run(selftest.Options{
		Bridge:    d.Cfg.Network.Bridge,
		CIDR:      d.Cfg.Network.CIDR,
		HostIP:    d.Cfg.Network.HostIP,
		GuestPort: d.Cfg.Network.GuestPort,
		DHCP:      d.dhcp,
		Control:   true,
		Log:       d.Log,
	})
	rep.Log(d.Log)
	if err := d.dhcp.Err(); err != nil {
		holder := netbridge.WhoBindsUDP(67)
		d.Log.Error("dhcp server is not running", "err", err, "holder", holder)
	}
	switch {
	case rep.EgressBroken():
		d.egress.Store("failed")
		d.Log.Error("VMs cannot reach the internet — runners would register Offline in GitHub",
			"failed", strings.Join(rep.FailedSteps(), ","),
			"action", "runner creation paused; fixes above; retrying automatically")
	case len(rep.Steps) > 0 && rep.Steps[0].Status == selftest.Skip:
		d.egress.Store("unknown")
	default:
		d.egress.Store("ok")
	}
}

// cleanupOrphans removes leftovers from a previous daemon that was killed
// without a graceful stop: runner QEMU processes (they hold the TAPs and
// cause "Device or resource busy"), stale tap devices, and overlay disks.
func (d *Daemon) cleanupOrphans() {
	pattern := fmt.Sprintf(`qemu-system\S* .*-name %s-[0-9]+-[0-9]+`, d.Cfg.Runner.NamePrefix)
	if out, err := exec.Command("pgrep", "-af", pattern).Output(); err == nil && len(out) > 0 {
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		d.Log.Info("killing orphan runner VMs from a previous run", "count", len(lines))
		_ = exec.Command("pkill", "-TERM", "-f", pattern).Run()
		deadline := time.Now().Add(8 * time.Second)
		for time.Now().Before(deadline) {
			if err := exec.Command("pgrep", "-f", pattern).Run(); err != nil {
				break
			}
			time.Sleep(500 * time.Millisecond)
		}
		_ = exec.Command("pkill", "-KILL", "-f", pattern).Run()
	}
	taps, _ := filepath.Glob("/sys/class/net/tap-ghrd*")
	for _, p := range taps {
		tap := filepath.Base(p)
		if err := netbridge.DeleteTAP(tap); err == nil {
			d.Log.Info("removed stale tap", "tap", tap)
		}
	}
	overlays, _ := filepath.Glob(filepath.Join(d.Cfg.Layout().Runtime, "*.qcow2"))
	for _, p := range overlays {
		if err := os.Remove(p); err == nil {
			d.Log.Info("removed stale overlay", "path", p)
		}
	}
}

func (d *Daemon) handleWebhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read", 400)
		return
	}
	if d.Cfg.Webhook.Secret != "" {
		if !githubutil.ValidSignature(d.Cfg.Webhook.Secret, r.Header.Get("X-Hub-Signature-256"), body) {
			http.Error(w, "bad signature", http.StatusUnauthorized)
			return
		}
	}
	if r.Header.Get("X-GitHub-Event") != "workflow_job" && r.Header.Get("X-GitHub-Event") != "" {
		w.WriteHeader(202)
		return
	}
	job, action, err := ghapi.ParseWorkflowJob(body)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	w.WriteHeader(202)
	if action != "queued" || d.pool == nil {
		return
	}
	if d.egressState() == "failed" {
		d.Log.Warn("job deferred: VM egress is broken; it will be picked up by polling once fixed", "job", job.ID)
		return
	}
	go func() {
		if err := d.pool.HandleQueuedJob(context.Background(), job); err != nil {
			d.Log.Error("handle queued job", "job", job.ID, "err", err)
		}
	}()
}

func (d *Daemon) poll(ctx context.Context) {
	jobs, err := d.client.ListQueuedJobs(ctx)
	if err != nil {
		d.Log.Warn("poll queued jobs", "err", err)
		return
	}
	for _, job := range jobs {
		if err := d.pool.HandleQueuedJob(ctx, job); err != nil {
			d.Log.Error("handle queued job", "job", job.ID, "err", err)
		}
	}
}

func (d *Daemon) writeStatus() {
	st := struct {
		Version string      `json:"version"`
		Egress  string      `json:"network_egress"`
		Pool    pool.Status `json:"pool"`
		Time    time.Time   `json:"time"`
	}{Version: version.Version, Egress: d.egressState(), Pool: d.pool.Status(), Time: time.Now().UTC()}
	raw, _ := json.MarshalIndent(st, "", "  ")
	_ = os.WriteFile(d.Cfg.Layout().StatusFile(), raw, 0o600)
}

type liveBackend struct {
	d *Daemon
}

func (b *liveBackend) GenerateJIT(ctx context.Context, name string, labels []string) (ghapi.JITResult, error) {
	return b.d.client.GenerateJITConfig(ctx, name, labels)
}

func (b *liveBackend) StartVM(ctx context.Context, spec qemu.Spec) (*qemu.Instance, error) {
	if !b.d.vsockOK {
		spec.DisableVsock = true
	}
	return qemu.Start(ctx, spec)
}

func (b *liveBackend) WaitGuest(ctx context.Context) (*guest.Session, error) {
	return b.d.host.Next(ctx)
}

func (b *liveBackend) CreateTAP(bridge, tap string) error {
	return netbridge.CreateTAP(bridge, tap)
}

func (b *liveBackend) DeleteTAP(tap string) error {
	return netbridge.DeleteTAP(tap)
}

func (b *liveBackend) CreateOverlay(backing, overlay string, diskGB int) error {
	return qemu.CreateOverlay(backing, overlay, diskGB)
}

func (b *liveBackend) RegisterDHCP(lease netbridge.Lease) {
	b.d.dhcp.Set(lease)
}

func (b *liveBackend) UnregisterDHCP(mac string) {
	b.d.dhcp.Delete(mac)
}

func (b *liveBackend) RunnerImage() (images.RunnerImage, error) {
	cat := images.Catalog{Dir: b.d.Cfg.Layout().Runner}
	if img, err := cat.Find(b.d.Cfg.VM.Template); err == nil {
		return img, nil
	}
	return cat.Active()
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
