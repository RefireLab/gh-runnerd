package guest

import (
	"context"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/mdlayher/vsock"
)

const DefaultPort = 5099

// pendingTTL is how long an unclaimed guest session may wait for a VM to
// pick it up. It comfortably exceeds vm.boot_timeout; anything older is a
// connection nobody will ever claim (a probe, a stray dial from a job) and
// holding it would only grow the pending list.
const pendingTTL = 5 * time.Minute

// Session is one connected guest.
type Session struct {
	Conn     *Conn
	Hello    Message
	RemoteIP string
	// RemoteCID is the guest's vsock context ID, 0 for TCP connections.
	RemoteCID uint32
	raw       net.Conn
	arrivedAt time.Time
}

// Host accepts guest-agent connections over TCP (bridge) and vsock.
type Host struct {
	Log     *slog.Logger
	mu      sync.Mutex
	pending []*Session
	// wake is closed and replaced every time a session arrives, waking
	// every NextFor waiter to rescan pending (broadcast semantics).
	wake    chan struct{}
	lnTCP   net.Listener
	lnVsock net.Listener
	closed  bool
}

func NewHost(log *slog.Logger) *Host {
	return &Host{Log: log, wake: make(chan struct{})}
}

// ListenTCP binds the guest control port on hostIP.
func (h *Host) ListenTCP(hostIP string, port int) error {
	ln, err := net.Listen("tcp", net.JoinHostPort(hostIP, strconv.Itoa(port)))
	if err != nil {
		return err
	}
	h.mu.Lock()
	h.lnTCP = ln
	h.mu.Unlock()
	go h.acceptLoop(ln)
	return nil
}

// ListenVsock binds CID 2 (host) if the kernel module is available.
func (h *Host) ListenVsock(port uint32) error {
	ln, err := vsock.Listen(port, nil)
	if err != nil {
		return err
	}
	h.mu.Lock()
	h.lnVsock = ln
	h.mu.Unlock()
	go h.acceptLoop(ln)
	return nil
}

func (h *Host) acceptLoop(ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go h.handle(c)
	}
}

func (h *Host) handle(raw net.Conn) {
	_ = raw.SetDeadline(time.Now().Add(2 * time.Minute))
	gc := NewConn(raw)
	msg, err := gc.Recv()
	if err != nil {
		_ = raw.Close()
		return
	}
	if msg.Type != KindHello {
		_ = raw.Close()
		return
	}
	sess := &Session{Conn: gc, Hello: msg, raw: raw}
	// The session's identity is taken from the transport, never from the
	// Hello payload: the source IP of the TCP connection on the isolated
	// bridge, or the vsock context ID, both assigned by the host side.
	switch addr := raw.RemoteAddr().(type) {
	case *vsock.Addr:
		sess.RemoteCID = addr.ContextID
	default:
		sess.RemoteIP, _, _ = net.SplitHostPort(raw.RemoteAddr().String())
	}
	h.admit(sess)
}

// admit parks a session until the VM it belongs to claims it via NextFor.
func (h *Host) admit(sess *Session) {
	sess.arrivedAt = time.Now()
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		_ = sess.Close()
		return
	}
	h.prunePendingLocked()
	h.pending = append(h.pending, sess)
	old := h.wake
	h.wake = make(chan struct{})
	h.mu.Unlock()
	close(old)
}

// prunePendingLocked drops unclaimed sessions past pendingTTL. Callers hold mu.
func (h *Host) prunePendingLocked() {
	kept := h.pending[:0]
	for _, s := range h.pending {
		if time.Since(s.arrivedAt) > pendingTTL {
			_ = s.Close()
			continue
		}
		kept = append(kept, s)
	}
	h.pending = kept
}

// NextFor waits for the guest session belonging to one specific VM,
// identified by its bridge IP (TCP) or vsock CID. Sessions are matched on
// transport identity, never on arrival order: when several VMs boot at
// once, arrival order is effectively random, and handing a VM some other
// guest's session would cross-wire JIT configs — the wrong QEMU would then
// be destroyed when the crossed session ends, killing a runner mid-job.
func (h *Host) NextFor(ctx context.Context, ip string, cid uint32) (*Session, error) {
	for {
		h.mu.Lock()
		for i, s := range h.pending {
			if (ip != "" && s.RemoteIP == ip) || (cid != 0 && s.RemoteCID == cid) {
				h.pending = append(h.pending[:i], h.pending[i+1:]...)
				h.mu.Unlock()
				return s, nil
			}
		}
		if h.closed {
			h.mu.Unlock()
			return nil, net.ErrClosed
		}
		ch := h.wake
		h.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ch:
		}
	}
}

// SendJIT delivers the encoded JIT config and clears the deadline for the job.
func (s *Session) SendJIT(encoded string) error {
	if s.raw != nil {
		_ = s.raw.SetDeadline(time.Time{})
	}
	return s.Conn.Send(Message{Type: KindJIT, Encoded: encoded})
}

// Shutdown asks the guest to power off.
func (s *Session) Shutdown() error {
	return s.Conn.Send(Message{Type: KindShutdown})
}

func (s *Session) Close() error {
	if s.raw != nil {
		return s.raw.Close()
	}
	return nil
}

// RecvLoop reads guest events until the connection dies.
func (s *Session) RecvLoop(fn func(Message)) error {
	for {
		m, err := s.Conn.Recv()
		if err != nil {
			return err
		}
		if fn != nil {
			fn(m)
		}
		if m.Type == KindJobFinished {
			return nil
		}
	}
}

func (h *Host) Close() error {
	h.mu.Lock()
	h.closed = true
	for _, s := range h.pending {
		_ = s.Close()
	}
	h.pending = nil
	old := h.wake
	h.wake = make(chan struct{})
	if h.lnTCP != nil {
		_ = h.lnTCP.Close()
	}
	if h.lnVsock != nil {
		_ = h.lnVsock.Close()
	}
	h.mu.Unlock()
	close(old)
	return nil
}
