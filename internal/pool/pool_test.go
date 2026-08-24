package pool

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/RefireLab/gh-runnerd/internal/config"
	"github.com/RefireLab/gh-runnerd/internal/ghapi"
	"github.com/RefireLab/gh-runnerd/internal/guest"
	"github.com/RefireLab/gh-runnerd/internal/images"
	"github.com/RefireLab/gh-runnerd/internal/netbridge"
	"github.com/RefireLab/gh-runnerd/internal/qemu"
)

type fakeBackend struct {
	t           *testing.T
	guests      chan *guest.Session
	taps        int
	imagePath   string
	mu          sync.Mutex
	overlays    int
	jits        int
	removed     []int64
	removeErr   error
	removeCalls int
	seq         []string
}

func (f *fakeBackend) GenerateJIT(ctx context.Context, name string, labels []string) (ghapi.JITResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.jits++
	res := ghapi.JITResult{Encoded: "jit-" + name}
	res.Runner.ID = int64(100 + f.jits)
	return res, nil
}

func (f *fakeBackend) jitCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.jits
}

func (f *fakeBackend) RemoveRunner(ctx context.Context, id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removeCalls++
	if f.removeErr != nil {
		return f.removeErr
	}
	f.removed = append(f.removed, id)
	f.seq = append(f.seq, "remove")
	return nil
}

func (f *fakeBackend) removeAttempts() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.removeCalls
}

func (f *fakeBackend) removedIDs() []int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int64(nil), f.removed...)
}

func (f *fakeBackend) StartVM(ctx context.Context, spec qemu.Spec) (*qemu.Instance, error) {
	return &qemu.Instance{Spec: spec}, nil
}

func (f *fakeBackend) WaitGuest(ctx context.Context, ip string, cid uint32) (*guest.Session, error) {
	select {
	case s := <-f.guests:
		return s, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (f *fakeBackend) CreateTAP(bridge, tap string) error { f.taps++; return nil }
func (f *fakeBackend) DeleteTAP(tap string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seq = append(f.seq, "deltap")
	return nil
}
func (f *fakeBackend) CreateOverlay(backing, overlay string, diskGB int) error {
	f.mu.Lock()
	f.overlays++
	f.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(overlay), 0o700); err != nil {
		return err
	}
	return os.WriteFile(overlay, []byte("overlay"), 0o600)
}

func (f *fakeBackend) overlayCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.overlays
}

func (f *fakeBackend) sequence() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.seq...)
}

func (f *fakeBackend) setRemoveErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removeErr = err
}
func (f *fakeBackend) RegisterDHCP(lease netbridge.Lease) {}
func (f *fakeBackend) UnregisterDHCP(mac string)          {}
func (f *fakeBackend) RunnerImage() (images.RunnerImage, error) {
	return images.RunnerImage{Name: "ubuntu-24.04-amd64", Path: f.imagePath}, nil
}

func pipeSession(t *testing.T) (*guest.Session, net.Conn) {
	t.Helper()
	a, b := net.Pipe()
	t.Cleanup(func() { _ = a.Close(); _ = b.Close() })
	return &guest.Session{Conn: guest.NewConn(a)}, b
}

func TestHandleQueuedJobSpawnsVM(t *testing.T) {
	cfg := config.Defaults()
	cfg.DataDir = t.TempDir()
	_ = cfg.Layout().Ensure()
	backend := &fakeBackend{t: t, guests: make(chan *guest.Session, 1), imagePath: filepath.Join(t.TempDir(), "base.qcow2")}
	sess, peer := pipeSession(t)
	backend.guests <- sess
	go func() {
		c := guest.NewConn(peer)
		msg, err := c.Recv()
		if err != nil {
			return
		}
		if msg.Type != guest.KindJIT {
			t.Errorf("got %s", msg.Type)
		}
		_ = c.Send(guest.Message{Type: guest.KindJobStarted})
		_ = c.Send(guest.Message{Type: guest.KindJobFinished, ExitCode: 0})
	}()
	m := New(cfg, slog.Default(), backend)
	err := m.HandleQueuedJob(context.Background(), ghapi.QueuedJob{ID: 7, Labels: []string{"gh-runnerd"}})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if backend.jitCount() > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if backend.jitCount() == 0 || backend.overlays == 0 {
		t.Fatalf("jits=%d overlays=%d", backend.jitCount(), backend.overlays)
	}
}

func TestHandleQueuedJobIgnoresForeignLabels(t *testing.T) {
	cfg := config.Defaults()
	cfg.DataDir = t.TempDir()
	backend := &fakeBackend{t: t, guests: make(chan *guest.Session), imagePath: "x"}
	m := New(cfg, slog.Default(), backend)
	if err := m.HandleQueuedJob(context.Background(), ghapi.QueuedJob{ID: 1, Labels: []string{"ubuntu-latest"}}); err != nil {
		t.Fatal(err)
	}
	if backend.overlays != 0 {
		t.Fatal("should not spawn")
	}
}

func TestMaintainIdleKeepsWarmVMIdleAfterRunnerStarts(t *testing.T) {
	cfg := config.Defaults()
	cfg.DataDir = t.TempDir()
	cfg.Pool.MinIdle = 1
	cfg.Pool.MaxConcurrent = 5
	_ = cfg.Layout().Ensure()
	backend := &fakeBackend{t: t, guests: make(chan *guest.Session, 2), imagePath: filepath.Join(t.TempDir(), "b.qcow2")}
	sess, peer := pipeSession(t)
	backend.guests <- sess
	go func() {
		c := guest.NewConn(peer)
		msg, err := c.Recv()
		if err != nil || msg.Type != guest.KindJIT {
			return
		}
		_ = c.Send(guest.Message{Type: guest.KindJobStarted})
		io.Copy(io.Discard, peer)
	}()
	m := New(cfg, slog.Default(), backend)
	if err := m.MaintainIdle(context.Background()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		st := m.Status()
		if st.Idle == 1 && backend.jitCount() == 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := m.MaintainIdle(context.Background()); err != nil {
		t.Fatal(err)
	}
	st := m.Status()
	if st.Idle != 1 || st.Busy != 0 || st.Booting != 0 || len(st.VMs) != 1 {
		t.Fatalf("warm VM must stay idle after run.sh starts: %+v", st)
	}
}

func TestDestroyAllRemovesEveryVM(t *testing.T) {
	cfg := config.Defaults()
	cfg.DataDir = t.TempDir()
	cfg.Pool.MinIdle = 2
	_ = cfg.Layout().Ensure()
	backend := &fakeBackend{t: t, guests: make(chan *guest.Session, 2), imagePath: filepath.Join(t.TempDir(), "b.qcow2")}
	for i := 0; i < 2; i++ {
		sess, peer := pipeSession(t)
		backend.guests <- sess
		go io.Copy(io.Discard, peer)
	}
	m := New(cfg, slog.Default(), backend)
	if err := m.MaintainIdle(context.Background()); err != nil {
		t.Fatal(err)
	}
	m.DestroyAll()
	st := m.Status()
	if len(st.VMs) != 0 || st.Idle != 0 || st.Busy != 0 || st.Booting != 0 {
		t.Fatalf("expected empty pool after DestroyAll: %+v", st)
	}
}

func TestDestroyDeregistersRunner(t *testing.T) {
	cfg := config.Defaults()
	cfg.DataDir = t.TempDir()
	_ = cfg.Layout().Ensure()
	backend := &fakeBackend{t: t, guests: make(chan *guest.Session, 1), imagePath: filepath.Join(t.TempDir(), "b.qcow2")}
	sess, peer := pipeSession(t)
	backend.guests <- sess
	go func() {
		c := guest.NewConn(peer)
		if _, err := c.Recv(); err != nil {
			return
		}
		// The runner process dies without finishing a job: RecvLoop ends
		// and the pool must deregister the runner from GitHub.
		_ = peer.Close()
	}()
	m := New(cfg, slog.Default(), backend)
	if err := m.HandleQueuedJob(context.Background(), ghapi.QueuedJob{ID: 7, Labels: []string{"gh-runnerd"}}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(backend.removedIDs()) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	got := backend.removedIDs()
	if len(got) != 1 || got[0] != 101 {
		t.Fatalf("expected runner id 101 deregistered once, got %v", got)
	}
	if len(m.ActiveNames()) != 0 {
		t.Fatalf("pool must be empty after destroy: %v", m.ActiveNames())
	}
}

func TestPoolExhaustedJobRetriedOnceCapacityFrees(t *testing.T) {
	cfg := config.Defaults()
	cfg.DataDir = t.TempDir()
	cfg.Pool.MaxConcurrent = 1
	_ = cfg.Layout().Ensure()
	backend := &fakeBackend{t: t, guests: make(chan *guest.Session, 2), imagePath: filepath.Join(t.TempDir(), "b.qcow2")}
	m := New(cfg, slog.Default(), backend)
	sess, peer := pipeSession(t)
	backend.guests <- sess
	go io.Copy(io.Discard, peer)
	if err := m.HandleQueuedJob(context.Background(), ghapi.QueuedJob{ID: 1, Labels: []string{"gh-runnerd"}}); err != nil {
		t.Fatal(err)
	}
	// A full pool is not an error: the job stays queued in GitHub. But it
	// must not be remembered as handled, or polling would skip it forever.
	if err := m.HandleQueuedJob(context.Background(), ghapi.QueuedJob{ID: 2, Labels: []string{"gh-runnerd"}}); err != nil {
		t.Fatalf("exhaustion must not error: %v", err)
	}
	if backend.overlayCount() != 1 {
		t.Fatalf("job 2 must not spawn while pool is full: %d overlays", backend.overlayCount())
	}
	m.DestroyAll()
	sess2, peer2 := pipeSession(t)
	backend.guests <- sess2
	go io.Copy(io.Discard, peer2)
	if err := m.HandleQueuedJob(context.Background(), ghapi.QueuedJob{ID: 2, Labels: []string{"gh-runnerd"}}); err != nil {
		t.Fatal(err)
	}
	if backend.overlayCount() != 2 {
		t.Fatalf("job 2 must spawn once capacity freed: %d overlays", backend.overlayCount())
	}
}

func onlyVM(t *testing.T, m *Manager) *VM {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.vms) != 1 {
		t.Fatalf("want exactly 1 VM, have %d", len(m.vms))
	}
	for _, vm := range m.vms {
		return vm
	}
	return nil
}

// warmVM boots one warm (min_idle) VM whose fake guest sends the boot-time
// job_started and then keeps the session open. It hands back the pool entry
// and the guest side of the control channel (close it to simulate a lost
// session).
func warmVM(t *testing.T, backend *fakeBackend, m *Manager, jobGo chan struct{}) (*VM, net.Conn) {
	t.Helper()
	sess, peer := pipeSession(t)
	backend.guests <- sess
	go func() {
		c := guest.NewConn(peer)
		msg, err := c.Recv()
		if err != nil || msg.Type != guest.KindJIT {
			return
		}
		_ = c.Send(guest.Message{Type: guest.KindJobStarted})
		if jobGo != nil {
			<-jobGo
			_ = c.Send(guest.Message{Type: guest.KindJobActive})
		}
		_, _ = io.Copy(io.Discard, peer)
	}()
	if err := m.MaintainIdle(context.Background()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if m.Status().Idle == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if st := m.Status(); st.Idle != 1 {
		t.Fatalf("warm VM did not become idle: %+v", st)
	}
	return onlyVM(t, m), peer
}

func TestRecycleDeregistersBeforeDestroy(t *testing.T) {
	cfg := config.Defaults()
	cfg.DataDir = t.TempDir()
	cfg.Pool.MinIdle = 1
	_ = cfg.Layout().Ensure()
	backend := &fakeBackend{t: t, guests: make(chan *guest.Session, 1), imagePath: filepath.Join(t.TempDir(), "b.qcow2")}
	m := New(cfg, slog.Default(), backend)
	vm, _ := warmVM(t, backend, m, nil)
	m.mu.Lock()
	vm.JITAt = time.Now().Add(-time.Hour)
	m.mu.Unlock()
	m.recycleExpired()
	if got := backend.removedIDs(); len(got) != 1 || got[0] != 101 {
		t.Fatalf("expected exactly one deregistration of runner 101, got %v", got)
	}
	if n := len(m.ActiveNames()); n != 0 {
		t.Fatalf("VM must be destroyed after recycle, %d left", n)
	}
	seq := backend.sequence()
	if len(seq) < 2 || seq[0] != "remove" || seq[1] != "deltap" {
		t.Fatalf("runner must be deregistered BEFORE the VM is torn down, got %v", seq)
	}
}

func TestRecycleSparesRunnerThatTookAJob(t *testing.T) {
	cfg := config.Defaults()
	cfg.DataDir = t.TempDir()
	cfg.Pool.MinIdle = 1
	_ = cfg.Layout().Ensure()
	backend := &fakeBackend{t: t, guests: make(chan *guest.Session, 1), imagePath: filepath.Join(t.TempDir(), "b.qcow2")}
	m := New(cfg, slog.Default(), backend)
	vm, _ := warmVM(t, backend, m, nil)
	// GitHub gave the "idle" runner a job just before the recycle pass:
	// DELETE answers 422 and the VM must survive.
	backend.setRemoveErr(&ghapi.APIError{StatusCode: http.StatusUnprocessableEntity, Status: "422 Unprocessable Entity"})
	m.mu.Lock()
	vm.JITAt = time.Now().Add(-time.Hour)
	m.mu.Unlock()
	m.recycleExpired()
	st := m.Status()
	if len(m.ActiveNames()) != 1 || st.Busy != 1 || st.Idle != 0 {
		t.Fatalf("busy runner's VM must survive the recycler and be marked busy: %+v", st)
	}
	if seq := backend.sequence(); len(seq) != 0 {
		t.Fatalf("nothing may be torn down for a busy runner, got %v", seq)
	}
}

func TestJobActiveMarksWarmVMBusyAndRecyclerSpares(t *testing.T) {
	cfg := config.Defaults()
	cfg.DataDir = t.TempDir()
	cfg.Pool.MinIdle = 1
	_ = cfg.Layout().Ensure()
	backend := &fakeBackend{t: t, guests: make(chan *guest.Session, 1), imagePath: filepath.Join(t.TempDir(), "b.qcow2")}
	m := New(cfg, slog.Default(), backend)
	jobGo := make(chan struct{})
	vm, _ := warmVM(t, backend, m, jobGo)
	close(jobGo)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if m.Status().Busy == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if st := m.Status(); st.Busy != 1 || st.Idle != 0 {
		t.Fatalf("job_active must flip a warm VM to busy: %+v", st)
	}
	m.mu.Lock()
	vm.JITAt = time.Now().Add(-time.Hour)
	m.mu.Unlock()
	m.recycleExpired()
	if len(m.ActiveNames()) != 1 {
		t.Fatal("recycler must not touch a busy VM")
	}
	if got := backend.removedIDs(); len(got) != 0 {
		t.Fatalf("recycler must not deregister a busy runner, got %v", got)
	}
}

func TestJobTimeoutReapsHungBusyVM(t *testing.T) {
	cfg := config.Defaults()
	cfg.DataDir = t.TempDir()
	cfg.Pool.MinIdle = 1
	_ = cfg.Layout().Ensure()
	backend := &fakeBackend{t: t, guests: make(chan *guest.Session, 1), imagePath: filepath.Join(t.TempDir(), "b.qcow2")}
	m := New(cfg, slog.Default(), backend)
	jobGo := make(chan struct{})
	vm, _ := warmVM(t, backend, m, jobGo)
	close(jobGo)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if m.Status().Busy == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	m.mu.Lock()
	vm.BusyAt = time.Now().Add(-cfg.Pool.JobTimeout.Duration - time.Hour)
	m.mu.Unlock()
	m.recycleExpired()
	if n := len(m.ActiveNames()); n != 0 {
		t.Fatalf("hung busy VM must be reaped after job_timeout, %d left", n)
	}
}

// A dead control channel must not kill a VM whose runner GitHub still
// reports busy: the runner is a separate guest process and its job keeps
// running without the agent.
func TestSessionLostWhileBusyKeepsVMUntilRunnerReleased(t *testing.T) {
	cfg := config.Defaults()
	cfg.DataDir = t.TempDir()
	cfg.Pool.MinIdle = 1
	_ = cfg.Layout().Ensure()
	backend := &fakeBackend{t: t, guests: make(chan *guest.Session, 1), imagePath: filepath.Join(t.TempDir(), "b.qcow2")}
	m := New(cfg, slog.Default(), backend)
	jobGo := make(chan struct{})
	vm, peer := warmVM(t, backend, m, jobGo)
	close(jobGo)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if m.Status().Busy == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if st := m.Status(); st.Busy != 1 {
		t.Fatalf("VM not busy: %+v", st)
	}
	// GitHub still holds the runner busy: DELETE answers 422.
	backend.setRemoveErr(&ghapi.APIError{StatusCode: http.StatusUnprocessableEntity, Status: "422 Unprocessable Entity"})
	_ = peer.Close() // agent crash: control channel dies mid-job
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if backend.removeAttempts() >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	st := m.Status()
	if len(m.ActiveNames()) != 1 || st.Busy != 1 {
		t.Fatalf("busy VM must survive a lost control channel: %+v", st)
	}
	if seq := backend.sequence(); len(seq) != 0 {
		t.Fatalf("nothing may be torn down while GitHub says busy, got %v", seq)
	}
	// The job ends: GitHub releases the registration (DELETE now succeeds)
	// and the next recycle pass reaps the VM.
	backend.setRemoveErr(nil)
	m.recycleExpired()
	if n := len(m.ActiveNames()); n != 0 {
		t.Fatalf("VM must be reaped once GitHub releases its runner, %d left", n)
	}
	_ = vm
}

// A failed boot must not poison the job dedup set: polling has to be able
// to spawn a fresh VM for the job that never got a runner.
func TestBootFailureAllowsJobRetry(t *testing.T) {
	cfg := config.Defaults()
	cfg.DataDir = t.TempDir()
	cfg.VM.BootTimeout.Duration = 30 * time.Millisecond
	_ = cfg.Layout().Ensure()
	backend := &fakeBackend{t: t, guests: make(chan *guest.Session, 1), imagePath: filepath.Join(t.TempDir(), "b.qcow2")}
	m := New(cfg, slog.Default(), backend)
	if err := m.HandleQueuedJob(context.Background(), ghapi.QueuedJob{ID: 7, Labels: []string{"gh-runnerd"}}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(m.ActiveNames()) == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if n := len(m.ActiveNames()); n != 0 {
		t.Fatalf("boot-timeout VM must be destroyed, %d left", n)
	}
	sess, peer := pipeSession(t)
	backend.guests <- sess
	go io.Copy(io.Discard, peer)
	if err := m.HandleQueuedJob(context.Background(), ghapi.QueuedJob{ID: 7, Labels: []string{"gh-runnerd"}}); err != nil {
		t.Fatal(err)
	}
	if backend.overlayCount() != 2 {
		t.Fatalf("job must be retriable after a failed boot: %d overlays", backend.overlayCount())
	}
}
