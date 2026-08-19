package guest

import (
	"net"
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
