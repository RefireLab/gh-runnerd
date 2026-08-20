//go:build linux

package selftest

import (
	"log/slog"
	"net"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/RefireLab/gh-runnerd/internal/netbridge"
)

// TestIntegrationBridgeProbe builds the real thing on a scratch bridge:
// netbridge.Setup, the daemon's DHCP server, a control listener, then the
// full namespace probe against them. Root + iproute2 + iptables only, so
// CI (non-root) skips it; run locally with:
//
//	sudo go test ./internal/selftest -run Integration -v
func TestIntegrationBridgeProbe(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root")
	}
	for _, bin := range []string{"ip", "iptables"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not installed", bin)
		}
	}
	cfg := netbridge.Config{
		Bridge:        "ghrd-it-br",
		CIDR:          "10.99.7.0/24",
		HostIP:        "10.99.7.1",
		RegistryPort:  443,
		RegistryLocal: 42999,
		GuestPort:     5977,
	}
	t.Cleanup(func() {
		_ = exec.Command("ip", "link", "del", cfg.Bridge).Run()
	})
	if err := netbridge.Setup(cfg); err != nil {
		t.Skipf("bridge setup impossible here: %v", err)
	}

	dhcp := netbridge.NewDHCP(slog.Default())
	go func() { _ = dhcp.ListenAndServe(cfg.Bridge) }()
	t.Cleanup(func() { _ = dhcp.Close() })

	ctl, err := net.Listen("tcp", net.JoinHostPort(cfg.HostIP, "5977"))
	if err != nil {
		t.Fatalf("control listener: %v", err)
	}
	t.Cleanup(func() { _ = ctl.Close() })
	go func() {
		for {
			c, err := ctl.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()

	rep := Run(Options{
		Bridge:    cfg.Bridge,
		CIDR:      cfg.CIDR,
		HostIP:    cfg.HostIP,
		GuestPort: cfg.GuestPort,
		DHCP:      dhcp,
		Control:   true,
		Timeout:   8 * time.Second,
		Log:       slog.Default(),
	})
	t.Logf("report:\n%s", rep.String())

	got := map[string]Status{}
	for _, s := range rep.Steps {
		got[s.Name] = s.Status
	}
	if got["dhcp"] != OK {
		t.Errorf("dhcp probe: %v", got["dhcp"])
	}
	if got["control"] != OK {
		t.Errorf("control probe: %v", got["control"])
	}
	// DNS/TCP verify real internet egress through NAT. Sandboxes often
	// proxy host egress while dropping forwarded traffic, so these are
	// asserted only on hosts known to allow NAT egress.
	if os.Getenv("GH_SELFTEST_REQUIRE_EGRESS") == "1" {
		if got["dns"] != OK {
			t.Errorf("dns probe: %v", got["dns"])
		}
		if got["tcp443"] != OK {
			t.Errorf("tcp443 probe: %v", got["tcp443"])
		}
	} else {
		t.Logf("dns/tcp443 informational here (set GH_SELFTEST_REQUIRE_EGRESS=1 to assert): dns=%v tcp443=%v", got["dns"], got["tcp443"])
	}
}
