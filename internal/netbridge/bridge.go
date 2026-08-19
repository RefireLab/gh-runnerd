package netbridge

import (
	"fmt"
	"net"
	"os/exec"
	"strings"
)

const (
	defaultBridge = "br-ghrunnerd"
	defaultCIDR   = "10.87.0.0/16"
	defaultHostIP = "10.87.0.1"
)

// Config is the isolated VM network.
type Config struct {
	Bridge string
	CIDR   string
	HostIP string
}

func (c Config) withDefaults() Config {
	if c.Bridge == "" {
		c.Bridge = defaultBridge
	}
	if c.CIDR == "" {
		c.CIDR = defaultCIDR
	}
	if c.HostIP == "" {
		c.HostIP = defaultHostIP
	}
	return c
}

// SetupCommands returns the ip/iptables commands that create the isolated bridge.
func SetupCommands(c Config) [][]string {
	c = c.withDefaults()
	return [][]string{
		{"ip", "link", "add", c.Bridge, "type", "bridge"},
		{"ip", "addr", "replace", c.HostIP + "/16", "dev", c.Bridge},
		{"ip", "link", "set", c.Bridge, "up"},
		{"iptables", "-t", "nat", "-C", "POSTROUTING", "-s", c.CIDR, "!", "-d", c.CIDR, "-j", "MASQUERADE"},
		{"iptables", "-C", "FORWARD", "-i", c.Bridge, "-o", c.Bridge, "-j", "DROP"},
		{"iptables", "-C", "INPUT", "-i", c.Bridge, "-p", "tcp", "--dport", "443", "-d", c.HostIP, "-j", "ACCEPT"},
		{"iptables", "-C", "INPUT", "-i", c.Bridge, "-p", "tcp", "--dport", "5099", "-d", c.HostIP, "-j", "ACCEPT"},
		{"iptables", "-C", "INPUT", "-i", c.Bridge, "-p", "udp", "--dport", "67", "-j", "ACCEPT"},
		{"iptables", "-C", "INPUT", "-i", c.Bridge, "-d", c.HostIP, "-j", "DROP"},
	}
}

// TAPCommands creates a tap attached to the bridge.
func TAPCommands(bridge, tap string) [][]string {
	if bridge == "" {
		bridge = defaultBridge
	}
	return [][]string{
		{"ip", "tuntap", "add", "dev", tap, "mode", "tap"},
		{"ip", "link", "set", tap, "master", bridge},
		{"ip", "link", "set", tap, "up"},
	}
}

// DeleteTAPCommands removes a tap.
func DeleteTAPCommands(tap string) [][]string {
	return [][]string{{"ip", "link", "delete", tap}}
}

// Setup applies SetupCommands, ignoring "already exists" / "rule exists" errors.
func Setup(c Config) error {
	c = c.withDefaults()
	if err := runIgnore([]string{"ip", "link", "add", c.Bridge, "type", "bridge"}, "exists", "File exists"); err != nil {
		return err
	}
	// `ip addr replace` is idempotent; `add` fails on restart because the
	// bridge (and its address) survives daemon exits, and newer iproute2
	// reports "Address already assigned" instead of "File exists".
	if err := run([]string{"ip", "addr", "replace", c.HostIP + "/16", "dev", c.Bridge}); err != nil {
		return err
	}
	if err := run([]string{"ip", "link", "set", c.Bridge, "up"}); err != nil {
		return err
	}
	if err := ensureIptables(c); err != nil {
		return err
	}
	return nil
}

func ensureIptables(c Config) error {
	nat := []string{"iptables", "-t", "nat", "-A", "POSTROUTING", "-s", c.CIDR, "!", "-d", c.CIDR, "-j", "MASQUERADE"}
	if err := runIgnore(append([]string{"iptables", "-t", "nat", "-C"}, nat[4:]...), ""); err != nil {
		if err := run(nat); err != nil {
			return err
		}
	}
	rules := [][]string{
		{"FORWARD", "-i", c.Bridge, "-o", c.Bridge, "-j", "DROP"},
		{"INPUT", "-i", c.Bridge, "-p", "tcp", "--dport", "443", "-d", c.HostIP, "-j", "ACCEPT"},
		{"INPUT", "-i", c.Bridge, "-p", "tcp", "--dport", "5099", "-d", c.HostIP, "-j", "ACCEPT"},
		{"INPUT", "-i", c.Bridge, "-p", "udp", "--dport", "67", "-j", "ACCEPT"},
		{"INPUT", "-i", c.Bridge, "-d", c.HostIP, "-j", "DROP"},
	}
	for _, r := range rules {
		check := append([]string{"iptables", "-C"}, r...)
		add := append([]string{"iptables", "-A"}, r...)
		if err := runIgnore(check, ""); err != nil {
			if err := run(add); err != nil {
				return err
			}
		}
	}
	return nil
}

// CreateTAP adds a tap nic for one VM.
func CreateTAP(bridge, tap string) error {
	for _, cmd := range TAPCommands(bridge, tap) {
		if err := runIgnore(cmd, "exists", "File exists"); err != nil {
			return err
		}
	}
	return nil
}

// DeleteTAP removes a tap nic.
func DeleteTAP(tap string) error {
	return runIgnore([]string{"ip", "link", "delete", tap}, "Cannot find", "does not exist")
}

func run(args []string) error {
	cmd := exec.Command(args[0], args[1:]...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %s (%w)", strings.Join(args, " "), strings.TrimSpace(string(out)), err)
	}
	return nil
}

func runIgnore(args []string, needles ...string) error {
	cmd := exec.Command(args[0], args[1:]...)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}
	text := string(out) + err.Error()
	for _, n := range needles {
		if n != "" && strings.Contains(text, n) {
			return nil
		}
	}
	// iptables -C returns exit 1 when the rule is missing; caller handles that.
	if len(needles) == 1 && needles[0] == "" {
		return err
	}
	return fmt.Errorf("%s: %s (%w)", strings.Join(args, " "), strings.TrimSpace(string(out)), err)
}

// ParseHostIP validates the host address.
func ParseHostIP(s string) (net.IP, error) {
	ip := net.ParseIP(s)
	if ip == nil {
		return nil, fmt.Errorf("invalid host ip %q", s)
	}
	return ip, nil
}

// MACForIndex returns a locally-administered unicast MAC.
func MACForIndex(i int) string {
	return fmt.Sprintf("52:54:00:87:%02x:%02x", (i>>8)&0xff, i&0xff)
}

// IPForIndex returns 10.87.X.Y for slot i (starting at 2).
func IPForIndex(i int) string {
	n := i + 2
	return fmt.Sprintf("10.87.%d.%d", (n>>8)&0xff, n&0xff)
}
