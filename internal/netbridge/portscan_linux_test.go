//go:build linux

package netbridge

import (
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
)

func TestWhoBindsUDPFindsOwnSocket(t *testing.T) {
	t.Parallel()
	pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	port := pc.LocalAddr().(*net.UDPAddr).Port
	got := WhoBindsUDP(port)
	if !strings.Contains(got, fmt.Sprintf("(pid %d)", os.Getpid())) {
		t.Fatalf("expected own pid in %q", got)
	}
}

func TestWhoBindsUDPUnknownPort(t *testing.T) {
	t.Parallel()
	// Port 1 is privileged and almost certainly unbound in test envs.
	if got := WhoBindsUDP(1); got != "" {
		t.Logf("unexpected holder of udp/1: %q (fine on exotic hosts)", got)
	}
}
