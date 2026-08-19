package pool

import (
	"context"
	"fmt"
	"log/slog"
	"net"
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
	JobID     int64     `json:"job_id,omitempty"`
	inst      *qemu.Instance
	sess      *guest.Session
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
	StartVM(ctx context.Context, spec qemu.Spec) (*qemu.Instance, error)
	WaitGuest(ctx context.Context) (*guest.Session, error)
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
	m.seenJob[job.ID] = time.Now()
	if m.liveCount() >= m.cfg.Pool.MaxConcurrent {
		m.mu.Unlock()
		return fmt.Errorf("pool exhausted (%d max concurrent VMs)", m.cfg.Pool.MaxConcurrent)
	}
	m.mu.Unlock()
	labels := githubutil.MergeLabels(m.cfg.Runner.Labels, job.Labels)
	_, err := m.spawn(ctx, labels, job.ID)
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
	m.mu.Lock()
	var doomed []*VM
	now := time.Now()
	for _, vm := range m.vms {
		if vm.State == StateIdle && now.Sub(vm.JITAt) >= limit {
			doomed = append(doomed, vm)
		}
	}
	m.mu.Unlock()
	for _, vm := range doomed {
		m.log.Info("recycling idle VM before JIT expiry", "name", vm.Name, "age", time.Since(vm.JITAt).String())
		m.destroy(vm)
	}
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
	sess, err := m.backend.WaitGuest(cctx)
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
	} else {
		vm.State = StateIdle
	}
	m.mu.Unlock()

	go func() {
		_ = sess.RecvLoop(func(msg guest.Message) {
			if msg.Type == guest.KindJobStarted {
				m.mu.Lock()
				vm.State = StateBusy
				m.mu.Unlock()
			}
			if msg.Type == guest.KindJobFinished {
				m.log.Info("job finished", "vm", vm.Name, "exit", msg.ExitCode)
			}
		})
		m.destroy(vm)
	}()
}

func (m *Manager) destroy(vm *VM) {
	m.mu.Lock()
	vm.State = StateDead
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
}

func parseIPv4(s string) net.IP {
	return net.ParseIP(s).To4()
}
