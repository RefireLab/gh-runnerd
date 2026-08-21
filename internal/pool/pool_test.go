package pool

import (
	"context"
	"io"
	"log/slog"
	"net"
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
	t         *testing.T
	guests    chan *guest.Session
	taps      int
	overlays  int
	imagePath string
	mu        sync.Mutex
	jits      int
	removed   []int64
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
	f.removed = append(f.removed, id)
	return nil
}

func (f *fakeBackend) removedIDs() []int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int64(nil), f.removed...)
}

func (f *fakeBackend) StartVM(ctx context.Context, spec qemu.Spec) (*qemu.Instance, error) {
	return &qemu.Instance{Spec: spec}, nil
}

func (f *fakeBackend) WaitGuest(ctx context.Context) (*guest.Session, error) {
	select {
	case s := <-f.guests:
		return s, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (f *fakeBackend) CreateTAP(bridge, tap string) error { f.taps++; return nil }
func (f *fakeBackend) DeleteTAP(tap string) error         { return nil }
func (f *fakeBackend) CreateOverlay(backing, overlay string, diskGB int) error {
	f.overlays++
	if err := os.MkdirAll(filepath.Dir(overlay), 0o700); err != nil {
		return err
	}
	return os.WriteFile(overlay, []byte("overlay"), 0o600)
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

func TestPoolExhausted(t *testing.T) {
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
	if err := m.HandleQueuedJob(context.Background(), ghapi.QueuedJob{ID: 2, Labels: []string{"gh-runnerd"}}); err == nil {
		t.Fatal("expected exhaustion")
	}
}
