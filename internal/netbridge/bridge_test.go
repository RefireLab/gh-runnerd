package netbridge

import (
	"strings"
	"testing"
)

func TestSetupCommandsStayOnInternalBridge(t *testing.T) {
	t.Parallel()
	cmds := SetupCommands(Config{})
	joined := ""
	for _, c := range cmds {
		joined += strings.Join(c, " ") + "\n"
	}
	if !strings.Contains(joined, "br-ghrunnerd") {
		t.Fatal(joined)
	}
	if strings.Contains(joined, "0.0.0.0") {
		t.Fatal("must not bind a public host port")
	}
	if !strings.Contains(joined, "--dport 443") {
		t.Fatal("registry port must be allowed from the bridge")
	}
	if !strings.Contains(joined, "-i br-ghrunnerd -o br-ghrunnerd -j DROP") {
		t.Fatal("vm-to-vm must be dropped")
	}
}

func TestMACAndIP(t *testing.T) {
	t.Parallel()
	if MACForIndex(0) != "52:54:00:87:00:00" {
		t.Fatalf("%s", MACForIndex(0))
	}
	if IPForIndex(0) != "10.87.0.2" {
		t.Fatalf("%s", IPForIndex(0))
	}
}
