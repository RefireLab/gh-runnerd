package guest

import (
	"context"
	"io"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/mdlayher/vsock"
)

const DefaultPort = 5099

// Session is one connected guest.
type Session struct {
	Conn     *Conn
	Hello    Message
	RemoteIP string
	raw      net.Conn
}

// Host accepts guest-agent connections over TCP (bridge) and vsock.
type Host struct {
	Log      *slog.Logger
	sessions chan *Session
	mu       sync.Mutex
	lnTCP    net.Listener
	lnVsock  net.Listener
}

func NewHost(log *slog.Logger) *Host {
	return &Host{Log: log, sessions: make(chan *Session, 16)}
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
	ip, _, _ := net.SplitHostPort(raw.RemoteAddr().String())
	sess := &Session{Conn: gc, Hello: msg, RemoteIP: ip, raw: raw}
	select {
	case h.sessions <- sess:
	case <-time.After(30 * time.Second):
		_ = raw.Close()
	}
}

// Next waits for the next guest hello.
func (h *Host) Next(ctx context.Context) (*Session, error) {
	select {
	case s := <-h.sessions:
		if s == nil {
			return nil, io.EOF
		}
		return s, nil
	case <-ctx.Done():
		return nil, ctx.Err()
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
	defer h.mu.Unlock()
	if h.lnTCP != nil {
		_ = h.lnTCP.Close()
	}
	if h.lnVsock != nil {
		_ = h.lnVsock.Close()
	}
	return nil
}
