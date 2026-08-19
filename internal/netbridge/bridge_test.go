package netbridge

import (
	"net"
	"strings"
	"testing"
)

func setupJoined(c Config) string {
	joined := ""
	for _, cmd := range SetupCommands(c) {
		joined += strings.Join(cmd, " ") + "\n"
	}
	return joined
}

func TestSetupCommandsStayOnInternalBridge(t *testing.T) {
	t.Parallel()
	joined := setupJoined(Config{})
	if !strings.Contains(joined, "br-ghrunnerd") {
		t.Fatal(joined)
	}
	if strings.Contains(joined, "0.0.0.0") {
		t.Fatal("must not bind a public host port")
	}
	if !strings.Contains(joined, "-i br-ghrunnerd -o br-ghrunnerd -j DROP") {
		t.Fatal("vm-to-vm must be dropped")
	}
	if !strings.Contains(joined, "ip addr replace 10.87.0.1/16 dev br-ghrunnerd") {
		t.Fatal("host address must be assigned idempotently (replace, not add)")
	}
}

func TestSetupCommandsRedirectRegistryPort(t *testing.T) {
	t.Parallel()
	joined := setupJoined(Config{})
	if !strings.Contains(joined, "PREROUTING -i br-ghrunnerd -d 10.87.0.1 -p tcp --dport 443 -j REDIRECT --to-ports 42443") {
		t.Fatalf("default config must redirect 443 to the local registry port:\n%s", joined)
	}
	if !strings.Contains(joined, "--dport 42443 -d 10.87.0.1 -j ACCEPT") {
		t.Fatalf("INPUT must accept the redirected registry port:\n%s", joined)
	}
	if !strings.Contains(joined, "--dport 5099 -d 10.87.0.1 -j ACCEPT") {
		t.Fatalf("INPUT must accept the guest port:\n%s", joined)
	}

	same := setupJoined(Config{RegistryPort: 443, RegistryLocal: 443})
	if strings.Contains(same, "REDIRECT") {
		t.Fatalf("no redirect expected when ports match:\n%s", same)
	}
	if !strings.Contains(same, "--dport 443 -d 10.87.0.1 -j ACCEPT") {
		t.Fatalf("INPUT must accept 443 when ports match:\n%s", same)
	}
}

func TestSetupCommandsCustomNetwork(t *testing.T) {
	t.Parallel()
	joined := setupJoined(Config{HostIP: "10.99.0.1", CIDR: "10.99.0.0/24"})
	if !strings.Contains(joined, "ip addr replace 10.99.0.1/24 dev br-ghrunnerd") {
		t.Fatalf("custom prefix length must be used:\n%s", joined)
	}
	if !strings.Contains(joined, "-s 10.99.0.0/24") {
		t.Fatalf("masquerade must use the custom cidr:\n%s", joined)
	}
}

func TestMACAndIP(t *testing.T) {
	t.Parallel()
	if MACForIndex(0) != "52:54:00:87:00:00" {
		t.Fatalf("%s", MACForIndex(0))
	}
	if got := IPForIndex("10.87.0.0/16", 0); got != "10.87.0.2" {
		t.Fatalf("%s", got)
	}
	if got := IPForIndex("192.168.200.0/24", 3); got != "192.168.200.5" {
		t.Fatalf("%s", got)
	}
	if got := IPForIndex("bogus", 0); got != "10.87.0.2" {
		t.Fatalf("fallback: %s", got)
	}
}

func TestMaskFromCIDR(t *testing.T) {
	t.Parallel()
	if ones, _ := MaskFromCIDR("10.99.0.0/24").Size(); ones != 24 {
		t.Fatalf("ones %d", ones)
	}
	if ones, _ := MaskFromCIDR("bogus").Size(); ones != 16 {
		t.Fatalf("fallback ones %d", ones)
	}
}

func TestNetsOverlap(t *testing.T) {
	t.Parallel()
	parse := func(s string) *net.IPNet {
		_, n, err := net.ParseCIDR(s)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	if !netsOverlap(parse("10.87.0.0/16"), parse("10.87.3.0/24")) {
		t.Fatal("nested networks must overlap")
	}
	if !netsOverlap(parse("10.87.3.0/24"), parse("10.0.0.0/8")) {
		t.Fatal("containing networks must overlap")
	}
	if netsOverlap(parse("10.87.0.0/16"), parse("10.88.0.0/16")) {
		t.Fatal("disjoint networks must not overlap")
	}
}
