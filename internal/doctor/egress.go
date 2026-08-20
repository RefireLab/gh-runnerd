package doctor

import (
	"fmt"
	"os"
	"runtime"

	"github.com/RefireLab/gh-runnerd/internal/config"
	"github.com/RefireLab/gh-runnerd/internal/selftest"
)

// EgressChecks probes the VM datapath from a namespace attached to the
// runner bridge: DNS and TCP 443 the way a VM would use them. The daemon
// runs the deeper variant (with DHCP and the control port) at startup;
// this one works standalone from the CLI.
func EgressChecks(cfg config.Config) []Check {
	if runtime.GOOS != "linux" {
		return []Check{{"vm-egress", Warn, "egress probe requires linux"}}
	}
	if os.Geteuid() != 0 {
		return []Check{{"vm-egress", Warn, "run doctor as root to probe VM internet access"}}
	}
	if _, err := os.Stat("/sys/class/net/" + cfg.Network.Bridge); err != nil {
		return []Check{{"vm-egress", Warn, fmt.Sprintf("bridge %s absent — it is created when serve starts", cfg.Network.Bridge)}}
	}
	rep := selftest.Run(selftest.Options{
		Bridge:    cfg.Network.Bridge,
		CIDR:      cfg.Network.CIDR,
		HostIP:    cfg.Network.HostIP,
		GuestPort: cfg.Network.GuestPort,
	})
	var checks []Check
	for _, s := range rep.Steps {
		name := "vm-egress-" + s.Name
		switch s.Status {
		case selftest.OK:
			checks = append(checks, Check{name, OK, s.Detail})
		case selftest.Fail:
			checks = append(checks, Check{name, Error, s.Detail + " — " + s.Fix})
		case selftest.Skip:
			checks = append(checks, Check{name, Warn, s.Detail})
		default:
			panic(fmt.Sprintf("unknown selftest status %q", s.Status))
		}
	}
	return checks
}
