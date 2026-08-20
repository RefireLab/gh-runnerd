package guest

import (
	"errors"
	"net"
	"strconv"
	"testing"
	"time"
)

func TestAgentReceivesJITAndReportsFinish(t *testing.T) {
	t.Parallel()
	h, g := net.Pipe()
	t.Cleanup(func() { _ = h.Close(); _ = g.Close() })

	done := make(chan error, 1)
	go func() {
		done <- RunAgent(AgentConfig{
			Hostname:    "vm-1",
			ConnectWait: time.Second,
			Dial:        func() (net.Conn, error) { return g, nil },
			RunnerStart: func(encoded string) (int, error) {
				if encoded != "jit-bytes" {
					t.Errorf("encoded %q", encoded)
				}
				return 0, nil
			},
		})
	}()

	hc := NewConn(h)
	hello, err := hc.Recv()
	if err != nil {
		t.Fatal(err)
	}
	if hello.Type != KindHello || hello.Hostname != "vm-1" {
		t.Fatalf("%+v", hello)
	}
	if err := hc.Send(Message{Type: KindJIT, Encoded: "jit-bytes"}); err != nil {
		t.Fatal(err)
	}
	started, err := hc.Recv()
	if err != nil {
		t.Fatal(err)
	}
	if started.Type != KindJobStarted {
		t.Fatalf("%+v", started)
	}
	finished, err := hc.Recv()
	if err != nil {
		t.Fatal(err)
	}
	if finished.Type != KindJobFinished || finished.ExitCode != 0 {
		t.Fatalf("%+v", finished)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestWithTimeoutAbandonsHangingDial(t *testing.T) {
	t.Parallel()
	started := time.Now()
	_, err := withTimeout(80*time.Millisecond, func() (net.Conn, error) {
		time.Sleep(2 * time.Second)
		return nil, errors.New("late")
	})
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("timed out too slowly: %s", elapsed)
	}
}

func TestDialHostFallsBackToTCPWhenVsockHangs(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		_ = c.Close()
	}()

	_, portStr, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	p, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatal(err)
	}

	old := vsockDial
	t.Cleanup(func() { vsockDial = old })
	vsockDial = func(uint32, uint32, time.Duration) (net.Conn, error) {
		time.Sleep(2 * time.Second)
		return nil, errors.New("vsock hung")
	}

	started := time.Now()
	c, err := dialHost("127.0.0.1", p)
	if err != nil {
		t.Fatal(err)
	}
	_ = c.Close()
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("TCP fallback waited on vsock: %s", elapsed)
	}
}
