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
	"strings"
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
	"github.com/RefireLab/gh-runnerd/internal/tlsutil"
	"github.com/RefireLab/gh-runnerd/internal/version"
)

// Daemon is the long-running gh-runnerd process.
type Daemon struct {
	Cfg    config.Config
	Log    *slog.Logger
	client *ghapi.Client
	pool   *pool.Manager
	host   *guest.Host
	dhcp   *netbridge.DHCP
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
		if err := d.dhcp.ListenAndServe(d.Cfg.Network.HostIP); err != nil {
			d.Log.Warn("dhcp server", "err", err)
		}
	}()

	d.host = guest.NewHost(d.Log)
	if err := d.host.ListenTCP(d.Cfg.Network.HostIP, d.Cfg.Network.GuestPort); err != nil {
		return fmt.Errorf("guest tcp: %w", err)
	}
	if err := d.host.ListenVsock(uint32(d.Cfg.Network.GuestPort)); err != nil {
		d.Log.Warn("vsock listen failed, using TCP on the isolated bridge only", "err", err)
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

	if err := d.pool.MaintainIdle(ctx); err != nil {
		d.Log.Warn("warm pool", "err", err)
	}

	d.Log.Info("gh-runnerd running", "version", version.Version, "labels", d.Cfg.Runner.Labels)

	for {
		select {
		case <-ctx.Done():
			_ = wh.Shutdown(context.Background())
			_ = regSrv.Shutdown(context.Background())
			_ = d.host.Close()
			_ = d.dhcp.Close()
			return nil
		case <-ticker.C:
			d.poll(ctx)
			if err := d.pool.MaintainIdle(ctx); err != nil {
				d.Log.Warn("maintain idle", "err", err)
			}
		case <-statusTick.C:
			d.writeStatus()
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
		Pool    pool.Status `json:"pool"`
		Time    time.Time   `json:"time"`
	}{Version: version.Version, Pool: d.pool.Status(), Time: time.Now().UTC()}
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
