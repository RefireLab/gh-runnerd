package guest

import (
	"context"
	"log/slog"
	"net"
	"sync"
	"testing"
	"time"
)

func fakeSession(ip string, cid uint32) *Session {
	a, _ := net.Pipe()
	return &Session{Conn: NewConn(a), RemoteIP: ip, RemoteCID: cid, raw: a}
}

// Sessions must be claimed by transport identity, not arrival order: when
// several VMs boot at once, handing VM A the guest of VM B cross-wires JIT
// configs and later destroys the wrong (possibly busy) VM.
func TestNextForMatchesByIPNotArrivalOrder(t *testing.T) {
	t.Parallel()
	h := NewHost(slog.Default())
	first := fakeSession("10.87.0.3", 0)
	second := fakeSession("10.87.0.4", 0)
	h.admit(first)
	h.admit(second)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	got, err := h.NextFor(ctx, "10.87.0.4", 0)
	if err != nil {
		t.Fatal(err)
	}
	if got != second {
		t.Fatalf("got session %q, want 10.87.0.4", got.RemoteIP)
	}
	got, err = h.NextFor(ctx, "10.87.0.3", 0)
	if err != nil {
		t.Fatal(err)
	}
	if got != first {
		t.Fatalf("got session %q, want 10.87.0.3", got.RemoteIP)
	}
}

func TestNextForMatchesByVsockCID(t *testing.T) {
	t.Parallel()
	h := NewHost(slog.Default())
	h.admit(fakeSession("", 7))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	got, err := h.NextFor(ctx, "10.87.0.9", 7)
	if err != nil {
		t.Fatal(err)
	}
	if got.RemoteCID != 7 {
		t.Fatalf("got CID %d", got.RemoteCID)
	}
}

func TestNextForWaitsForLateArrival(t *testing.T) {
	t.Parallel()
	h := NewHost(slog.Default())
	var wg sync.WaitGroup
	wg.Add(1)
	var got *Session
	var err error
	go func() {
		defer wg.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		got, err = h.NextFor(ctx, "10.87.0.5", 0)
	}()
	time.Sleep(50 * time.Millisecond)
	h.admit(fakeSession("10.87.0.6", 0)) // someone else's guest: must not match
	h.admit(fakeSession("10.87.0.5", 0))
	wg.Wait()
	if err != nil {
		t.Fatal(err)
	}
	if got.RemoteIP != "10.87.0.5" {
		t.Fatalf("claimed wrong session %q", got.RemoteIP)
	}
}

func TestNextForHonorsContext(t *testing.T) {
	t.Parallel()
	h := NewHost(slog.Default())
	h.admit(fakeSession("10.87.0.6", 0))
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	if _, err := h.NextFor(ctx, "10.87.0.99", 0); err == nil {
		t.Fatal("expected context timeout for a session that never arrives")
	}
}

func TestAdmitPrunesStaleSessions(t *testing.T) {
	t.Parallel()
	h := NewHost(slog.Default())
	stale := fakeSession("10.87.0.7", 0)
	h.admit(stale)
	h.mu.Lock()
	stale.arrivedAt = time.Now().Add(-pendingTTL - time.Minute)
	h.mu.Unlock()
	h.admit(fakeSession("10.87.0.8", 0))
	h.mu.Lock()
	n := len(h.pending)
	h.mu.Unlock()
	if n != 1 {
		t.Fatalf("stale session must be pruned, %d pending", n)
	}
}
