package pool

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/RefireLab/gh-runnerd/internal/config"
	"github.com/RefireLab/gh-runnerd/internal/ghapi"
	"github.com/RefireLab/gh-runnerd/internal/githubutil"
	"github.com/RefireLab/gh-runnerd/internal/guest"
	"github.com/RefireLab/gh-runnerd/internal/images"
	"github.com/RefireLab/gh-runnerd/internal/netbridge"
	"github.com/RefireLab/gh-runnerd/internal/qemu"
)

type State string

const (
	StateBooting State = "booting"
	StateIdle    State = "idle"
	StateBusy    State = "busy"
	StateDead    State = "dead"
)

type VM struct {
	Name      string    `json:"name"`
	State     State     `json:"state"`
	Labels    []string  `json:"labels"`
	CID       uint32    `json:"cid"`
	MAC       string    `json:"mac"`
	IP        string    `json:"ip"`
	TAP       string    `json:"tap"`
	Overlay   string    `json:"overlay"`
	StartedAt time.Time `json:"started_at"`
	JITAt     time.Time `json:"jit_at"`
	// BusyAt is when the VM was last known to take a workflow job; zero if
	// it never did (or the guest image predates the job_active signal).
	BusyAt   time.Time `json:"busy_at,omitempty"`
	JobID    int64     `json:"job_id,omitempty"`
	RunnerID int64     `json:"runner_id,omitempty"`
	inst     *qemu.Instance
	sess     *guest.Session
}

type Status struct {
	Idle    int  `json:"idle"`
	Busy    int  `json:"busy"`
	Booting int  `json:"booting"`
	Max     int  `json:"max"`
	VMs     []VM `json:"vms"`
}

// Backend is the side-effecting world the pool talks to. Tests stub it.
type Backend interface {
	GenerateJIT(ctx context.Context, name string, labels []string) (ghapi.JITResult, error)
	RemoveRunner(ctx context.Context, id int64) error
	StartVM(ctx context.Context, spec qemu.Spec) (*qemu.Instance, error)
	// WaitGuest waits for the control session of one specific VM,
	// identified by its bridge IP (TCP) or vsock CID.
	WaitGuest(ctx context.Context, ip string, cid uint32) (*guest.Session, error)
	CreateTAP(bridge, tap string) error
	DeleteTAP(tap string) error
	CreateOverlay(backing, overlay string, diskGB int) error
	RegisterDHCP(lease netbridge.Lease)
	UnregisterDHCP(mac string)
	RunnerImage() (images.RunnerImage, error)
}

type Manager struct {
	cfg     config.Config
	log     *slog.Logger
	backend Backend
	mu      sync.Mutex
	vms     map[string]*VM
	next    int
	seenJob map[int64]time.Time
}

func New(cfg config.Config, log *slog.Logger, backend Backend) *Manager {
	return &Manager{
		cfg:     cfg,
		log:     log,
		backend: backend,
		vms:     map[string]*VM{},
		seenJob: map[int64]time.Time{},
	}
}

func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := Status{Max: m.cfg.Pool.MaxConcurrent}
	for _, vm := range m.vms {
		cp := *vm
		cp.inst = nil
		cp.sess = nil
		st.VMs = append(st.VMs, cp)
		switch vm.State {
		case StateIdle:
			st.Idle++
		case StateBusy:
			st.Busy++
		case StateBooting:
			st.Booting++
		}
	}
	return st
}

func (m *Manager) liveCount() int {
	n := 0
	for _, vm := range m.vms {
		if vm.State != StateDead {
			n++
		}
	}
	return n
}

// HandleQueuedJob boots or reuses capacity for a matching job.
func (m *Manager) HandleQueuedJob(ctx context.Context, job ghapi.QueuedJob) error {
	if !githubutil.OwnsJob(m.cfg.Runner.Labels, job.Labels) {
		return nil
	}
	m.mu.Lock()
	if _, dup := m.seenJob[job.ID]; dup {
		m.mu.Unlock()
		return nil
	}
	now := time.Now()
	for id, at := range m.seenJob {
		if now.Sub(at) > 24*time.Hour {
			delete(m.seenJob, id)
		}
	}
	m.seenJob[job.ID] = now
	if m.liveCount() >= m.cfg.Pool.MaxConcurrent {
		// Not an error: the job stays queued in GitHub and either lands on
		// a warm runner or is retried by the next poll once a slot frees.
		// The seen mark must not stick, or polling would skip the job
		// forever.
		delete(m.seenJob, job.ID)
		m.mu.Unlock()
		m.log.Debug("job waits: pool at capacity", "job", job.ID, "max", m.cfg.Pool.MaxConcurrent)
		return nil
	}
	m.mu.Unlock()
	labels := githubutil.MergeLabels(m.cfg.Runner.Labels, job.Labels)
	_, err := m.spawn(ctx, labels, job.ID)
	if err != nil {
		m.mu.Lock()
		delete(m.seenJob, job.ID)
		m.mu.Unlock()
	}
	return err
}

// MaintainIdle boots VMs so that idle+booting >= min_idle, and recycles JIT-expired idles.
func (m *Manager) MaintainIdle(ctx context.Context) error {
	m.recycleExpired()
	for {
		m.mu.Lock()
		idle := 0
		for _, vm := range m.vms {
			if vm.State == StateIdle || vm.State == StateBooting {
				idle++
			}
		}
		need := m.cfg.Pool.MinIdle - idle
		can := m.cfg.Pool.MaxConcurrent - m.liveCount()
		m.mu.Unlock()
		if need <= 0 || can <= 0 {
			return nil
		}
		if _, err := m.spawn(ctx, m.cfg.Runner.Labels, 0); err != nil {
			return err
		}
	}
}

func (m *Manager) recycleExpired() {
	limit := m.cfg.Pool.RecycleIdleAfter.Duration
	if limit <= 0 {
		limit = 45 * time.Minute
	}
	jobLimit := m.cfg.Pool.JobTimeout.Duration
	m.mu.Lock()
	var doomed, overdue []*VM
	now := time.Now()
	for _, vm := range m.vms {
		switch vm.State {
		case StateIdle:
			if !vm.JITAt.IsZero() && now.Sub(vm.JITAt) >= limit {
				doomed = append(doomed, vm)
			}
		case StateBusy:
			// job_timeout backstop: a hung guest (frozen agent, kernel
			// panic — nothing else detects it) must not hold a pool slot
			// forever. Measured from BusyAt when the guest reported the
			// job, else from JIT as an upper bound on the job's age.
			since := vm.BusyAt
			if since.IsZero() {
				since = vm.JITAt
			}
			if jobLimit > 0 && !since.IsZero() && now.Sub(since) >= jobLimit {
				overdue = append(overdue, vm)
			}
		}
	}
	m.mu.Unlock()
	for _, vm := range doomed {
		m.recycleIdle(vm)
	}
	for _, vm := range overdue {
		m.log.Warn("destroying VM: job exceeded pool.job_timeout", "name", vm.Name, "timeout", jobLimit.String())
		m.destroy(vm)
	}
}

// recycleIdle tears down an idle VM, deregistering its runner from GitHub
// FIRST. GitHub assigns queued jobs to idle JIT runners directly, without
// telling the daemon, so "idle" here may be stale: the only authoritative
// check is the deregistration itself — GitHub refuses to delete a runner
// that is executing a job (422). Destroying the VM before deregistering
// would take a just-assigned job down with it and leave a ghost
// registration that GitHub fails ~10 minutes later with "runner lost
// communication with the server" and empty logs.
func (m *Manager) recycleIdle(vm *VM) {
	m.mu.Lock()
	if vm.State != StateIdle {
		m.mu.Unlock()
		return
	}
	runnerID := vm.RunnerID
	age := time.Since(vm.JITAt)
	m.mu.Unlock()
	if runnerID != 0 {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := m.backend.RemoveRunner(ctx, runnerID); err != nil {
			var apiErr *ghapi.APIError
			if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusUnprocessableEntity {
				// The runner picked up a job. Hands off: record what we
				// learned so the recycler stops trying and job_timeout has
				// a starting point. The VM is destroyed normally when the
				// job ends (runner exits, RecvLoop returns).
				m.log.Info("recycle skipped: runner took a job", "name", vm.Name, "runner_id", runnerID)
				m.mu.Lock()
				if vm.State == StateIdle {
					vm.State = StateBusy
					vm.BusyAt = time.Now()
				}
				m.mu.Unlock()
				return
			}
			m.log.Warn("recycle deferred: deregister failed, will retry", "name", vm.Name, "runner_id", runnerID, "err", err)
			return
		}
		m.mu.Lock()
		vm.RunnerID = 0
		m.mu.Unlock()
	}
	m.log.Info("recycling idle VM before JIT expiry", "name", vm.Name, "age", age.String())
	m.destroy(vm)
}

func (m *Manager) spawn(ctx context.Context, labels []string, jobID int64) (*VM, error) {
	img, err := m.backend.RunnerImage()
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	idx := m.next
	m.next++
	m.mu.Unlock()

	name := fmt.Sprintf("%s-%d-%d", m.cfg.Runner.NamePrefix, time.Now().Unix()%100000, idx)
	overlay := filepath.Join(m.cfg.Layout().Runtime, name+".qcow2")
	tap := fmt.Sprintf("tap-ghrd%d", idx)
	mac := netbridge.MACForIndex(idx)
	ip := netbridge.IPForIndex(m.cfg.Network.CIDR, idx)
	cid := uint32(3 + idx)

	if err := m.backend.CreateOverlay(img.Path, overlay, m.cfg.DiskGB()); err != nil {
		return nil, err
	}
	if err := m.backend.CreateTAP(m.cfg.Network.Bridge, tap); err != nil {
		_ = os.Remove(overlay)
		return nil, err
	}
	m.backend.RegisterDHCP(netbridge.Lease{
		MAC:    mac,
		IP:     parseIPv4(ip),
		Mask:   netbridge.MaskFromCIDR(m.cfg.Network.CIDR),
		Router: parseIPv4(m.cfg.Network.HostIP),
	})

	spec := qemu.Spec{
		Name:     name,
		Backing:  img.Path,
		Overlay:  overlay,
		CPUs:     m.cfg.VM.CPUs,
		MemoryMB: m.cfg.MemoryMB(),
		DiskGB:   m.cfg.DiskGB(),
		CID:      cid,
		MAC:      mac,
		TAP:      tap,
	}
	inst, err := m.backend.StartVM(ctx, spec)
	if err != nil {
		m.backend.DeleteTAP(tap)
		_ = os.Remove(overlay)
		return nil, err
	}
	vm := &VM{
		Name:      name,
		State:     StateBooting,
		Labels:    labels,
		CID:       cid,
		MAC:       mac,
		IP:        ip,
		TAP:       tap,
		Overlay:   overlay,
		StartedAt: time.Now(),
		JobID:     jobID,
		inst:      inst,
	}
	m.mu.Lock()
	m.vms[name] = vm
	m.mu.Unlock()

	go m.finishBoot(ctx, vm, labels)
	return vm, nil
}

func (m *Manager) finishBoot(ctx context.Context, vm *VM, labels []string) {
	bootTimeout := m.cfg.VM.BootTimeout.Duration
	if bootTimeout <= 0 {
		bootTimeout = 90 * time.Second
	}
	cctx, cancel := context.WithTimeout(ctx, bootTimeout)
	defer cancel()
	sess, err := m.backend.WaitGuest(cctx, vm.IP, vm.CID)
	if err != nil {
		m.log.Error("guest agent did not connect", "vm", vm.Name, "err", err)
		m.destroy(vm)
		return
	}
	jit, err := m.backend.GenerateJIT(ctx, vm.Name, labels)
	if err != nil {
		m.log.Error("generate-jitconfig failed", "vm", vm.Name, "err", err)
		m.destroy(vm)
		return
	}
	m.mu.Lock()
	vm.RunnerID = jit.Runner.ID
	m.mu.Unlock()
	if err := sess.SendJIT(jit.Encoded); err != nil {
		m.log.Error("send jit failed", "vm", vm.Name, "err", err)
		m.destroy(vm)
		return
	}
	m.mu.Lock()
	vm.sess = sess
	vm.JITAt = time.Now()
	if vm.JobID != 0 {
		vm.State = StateBusy
		vm.BusyAt = time.Now()
	} else {
		vm.State = StateIdle
	}
	m.mu.Unlock()

	go func() {
		_ = sess.RecvLoop(func(msg guest.Message) {
			if msg.Type == guest.KindJobStarted && vm.JobID != 0 {
				m.mu.Lock()
				vm.State = StateBusy
				m.mu.Unlock()
			}
			// job_active is the guest's authoritative "a workflow job is
			// running here" signal — GitHub hands jobs to warm idle
			// runners without telling the daemon.
			if msg.Type == guest.KindJobActive {
				m.mu.Lock()
				if vm.State == StateIdle || vm.State == StateBooting {
					vm.State = StateBusy
				}
				if vm.BusyAt.IsZero() {
					vm.BusyAt = time.Now()
				}
				m.mu.Unlock()
			}
			if msg.Type == guest.KindJobFinished {
				m.log.Info("job finished", "vm", vm.Name, "exit", msg.ExitCode)
			}
		})
		m.destroy(vm)
	}()
}

// ActiveNames returns the names of every VM the pool currently tracks,
// including ones still booting. The daemon sweeper skips them so a runner
// registration belonging to a live VM is never removed.
func (m *Manager) ActiveNames() map[string]bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	names := make(map[string]bool, len(m.vms))
	for name := range m.vms {
		names[name] = true
	}
	return names
}

// DestroyAll tears down every VM. Called on daemon shutdown so QEMU
// processes and TAP devices never outlive the daemon.
func (m *Manager) DestroyAll() {
	m.mu.Lock()
	var all []*VM
	for _, vm := range m.vms {
		all = append(all, vm)
	}
	m.mu.Unlock()
	for _, vm := range all {
		m.destroy(vm)
	}
}

func (m *Manager) destroy(vm *VM) {
	m.mu.Lock()
	if vm.State == StateDead {
		// Another goroutine is already tearing this VM down (RecvLoop exit
		// racing the recycler or DestroyAll); doing it twice would race on
		// cmd.Wait and double-free the network resources.
		m.mu.Unlock()
		return
	}
	vm.State = StateDead
	runnerID := vm.RunnerID
	vm.RunnerID = 0
	m.mu.Unlock()
	if vm.sess != nil {
		_ = vm.sess.Shutdown()
		_ = vm.sess.Close()
	}
	if vm.inst != nil {
		_ = vm.inst.Kill()
	}
	m.backend.DeleteTAP(vm.TAP)
	m.backend.UnregisterDHCP(vm.MAC)
	_ = os.Remove(vm.Overlay)
	m.mu.Lock()
	delete(m.vms, vm.Name)
	m.mu.Unlock()
	// Deregister from GitHub so the registration never lingers Offline (or
	// Idle for a killed runner: GitHub needs minutes to notice the broken
	// connection). After a completed job GitHub removes the ephemeral
	// runner itself; that answers 404, which RemoveRunner treats as done.
	if runnerID != 0 {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := m.backend.RemoveRunner(ctx, runnerID); err != nil {
			m.log.Warn("deregister runner in GitHub", "vm", vm.Name, "runner_id", runnerID, "err", err)
		} else {
			m.log.Info("deregistered runner in GitHub", "vm", vm.Name, "runner_id", runnerID)
		}
	}
}

func parseIPv4(s string) net.IP {
	return net.ParseIP(s).To4()
}
