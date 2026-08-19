package cli

import (
	"fmt"
	"net"
	"strings"

	"github.com/RefireLab/gh-runnerd/internal/config"
	"github.com/RefireLab/gh-runnerd/internal/netbridge"
	"github.com/RefireLab/gh-runnerd/internal/wizard"
)

// askNetwork configures the private VM network and the registry port so
// gh-runnerd never collides with the machine's existing services.
func askNetwork(p *wizard.Prompter, cfg *config.Config) error {
	p.Say("")
	p.Say("VM network")
	p.Say("----------")
	p.Say("gh-runnerd creates a private bridge for its VMs. It must not overlap")
	p.Say("your existing networks (LAN, Docker, VPN) — checked automatically.")

	def := suggestSubnet(cfg.Network.Bridge)
	for i := 0; i < 3; i++ {
		ans, err := p.Ask("Private network for the VMs", def)
		if err != nil {
			return err
		}
		hostIP, cidr, perr := parseHostCIDR(ans)
		if perr != nil {
			p.Say("[!!] %v — use e.g. 10.87.0.1/16", perr)
			continue
		}
		if over := netbridge.FindOverlap(cidr, cfg.Network.Bridge); over != "" {
			p.Say("[!!] %s overlaps your existing network on %s", cidr, over)
			force, ferr := p.AskYesNo("Use it anyway?", false)
			if ferr != nil {
				return ferr
			}
			if !force {
				continue
			}
		}
		cfg.Network.HostIP = hostIP
		cfg.Network.CIDR = cidr
		p.Say("[ok] VM network %s (bridge address %s)", cidr, hostIP)
		break
	}

	defPort := firstFreePort(netbridge.DefaultRegistryLocalPort)
	for i := 0; i < 3; i++ {
		port, err := p.AskInt("Local port for the internal image cache (VMs are not affected)", defPort)
		if err != nil {
			return err
		}
		if perr := probePort(port); perr != nil {
			p.Say("[!!] port %d is already in use on this machine — pick another (next free: %d)", port, firstFreePort(port+1))
			continue
		}
		cfg.Registry.Listen = net.JoinHostPort(cfg.Network.HostIP, fmt.Sprintf("%d", port))
		p.Say("[ok] registry will listen on %s", cfg.Registry.Listen)
		return nil
	}
	cfg.Registry.Listen = net.JoinHostPort(cfg.Network.HostIP, fmt.Sprintf("%d", defPort))
	p.Say("using %s — change registry.listen in the config if it clashes", cfg.Registry.Listen)
	return nil
}

// parseHostCIDR accepts "10.87.0.1/16" or a bare "10.87.0.1" (implies /16)
// and returns the bridge host address plus the network CIDR.
func parseHostCIDR(s string) (string, string, error) {
	s = strings.TrimSpace(s)
	if !strings.Contains(s, "/") {
		s += "/16"
	}
	ip, ipnet, err := net.ParseCIDR(s)
	if err != nil {
		return "", "", fmt.Errorf("%q is not a valid address", s)
	}
	v4 := ip.To4()
	if v4 == nil {
		return "", "", fmt.Errorf("%q is not IPv4", s)
	}
	ones, _ := ipnet.Mask.Size()
	if ones > 24 {
		return "", "", fmt.Errorf("network %s is too small — use /24 or larger", ipnet)
	}
	host := v4
	if v4.Equal(ipnet.IP) {
		// A network address like 10.87.0.0/16 was given; the bridge takes .1.
		host = make(net.IP, 4)
		copy(host, ipnet.IP.To4())
		host[3]++
	}
	return host.String(), ipnet.String(), nil
}

// suggestSubnet returns the first private /16 that does not overlap any
// host network.
func suggestSubnet(ignoreIface string) string {
	for second := 87; second <= 126; second++ {
		cidr := fmt.Sprintf("10.%d.0.0/16", second)
		if netbridge.FindOverlap(cidr, ignoreIface) == "" {
			return fmt.Sprintf("10.%d.0.1/16", second)
		}
	}
	return "10.87.0.1/16"
}

// probePort reports an error when TCP port is already taken by any
// listener (wildcard or specific address).
func probePort(port int) error {
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return err
	}
	return ln.Close()
}

// firstFreePort scans upward from start for a bindable TCP port.
func firstFreePort(start int) int {
	for p := start; p < start+100 && p < 65536; p++ {
		if probePort(p) == nil {
			return p
		}
	}
	return start
}
