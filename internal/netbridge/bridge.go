package netbridge

import (
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

const (
	defaultBridge = "br-ghrunnerd"
	defaultCIDR   = "10.87.0.0/16"
	defaultHostIP = "10.87.0.1"

	// DefaultRegistryPort is what VMs dial (baked into images as plain
	// HTTPS). DefaultRegistryLocalPort is where the host actually listens;
	// traffic from the bridge is redirected between the two so the host's
	// real port 443 is never touched.
	DefaultRegistryPort      = 443
	DefaultRegistryLocalPort = 42443
	DefaultGuestPort         = 5099
)

// Config is the isolated VM network.
type Config struct {
	Bridge        string
	CIDR          string
	HostIP        string
	RegistryPort  int // port VMs connect to (usually 443)
	RegistryLocal int // port the registry actually listens on
	GuestPort     int
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
	if c.RegistryPort == 0 {
		c.RegistryPort = DefaultRegistryPort
	}
	if c.RegistryLocal == 0 {
		c.RegistryLocal = DefaultRegistryLocalPort
	}
	if c.GuestPort == 0 {
		c.GuestPort = DefaultGuestPort
	}
	return c
}

// prefixLen returns the CIDR prefix length (default /16).
func (c Config) prefixLen() int {
	if _, ipnet, err := net.ParseCIDR(c.CIDR); err == nil {
		ones, _ := ipnet.Mask.Size()
		return ones
	}
	return 16
}

// SetupCommands returns the ip/iptables commands that create the isolated bridge.
func SetupCommands(c Config) [][]string {
	c = c.withDefaults()
	cmds := [][]string{
		{"ip", "link", "add", c.Bridge, "type", "bridge"},
		{"ip", "addr", "replace", fmt.Sprintf("%s/%d", c.HostIP, c.prefixLen()), "dev", c.Bridge},
		{"ip", "link", "set", c.Bridge, "up"},
		{"sysctl", "-w", "net.ipv4.ip_forward=1"},
		{"iptables", "-t", "mangle", "-C", "POSTROUTING", "-o", c.Bridge, "-p", "udp", "--dport", "68", "-j", "CHECKSUM", "--checksum-fill"},
		{"iptables", "-t", "nat", "-C", "POSTROUTING", "-s", c.CIDR, "!", "-d", c.CIDR, "-j", "MASQUERADE"},
		{"iptables", "-C", "FORWARD", "-i", c.Bridge, "!", "-o", c.Bridge, "-j", "ACCEPT"},
		{"iptables", "-C", "FORWARD", "-o", c.Bridge, "-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "ACCEPT"},
		{"iptables", "-C", "FORWARD", "-i", c.Bridge, "-o", c.Bridge, "-j", "DROP"},
	}
	if c.RegistryLocal != c.RegistryPort {
		// VMs dial HostIP:RegistryPort; rewrite it to the real listener so
		// the host's own port (e.g. a web server on 443) is never claimed.
		cmds = append(cmds, []string{
			"iptables", "-t", "nat", "-C", "PREROUTING", "-i", c.Bridge, "-d", c.HostIP,
			"-p", "tcp", "--dport", strconv.Itoa(c.RegistryPort),
			"-j", "REDIRECT", "--to-ports", strconv.Itoa(c.RegistryLocal),
		})
	}
	cmds = append(cmds,
		[]string{"iptables", "-C", "INPUT", "-i", c.Bridge, "-p", "tcp", "--dport", strconv.Itoa(c.RegistryLocal), "-d", c.HostIP, "-j", "ACCEPT"},
		[]string{"iptables", "-C", "INPUT", "-i", c.Bridge, "-p", "tcp", "--dport", strconv.Itoa(c.GuestPort), "-d", c.HostIP, "-j", "ACCEPT"},
		[]string{"iptables", "-C", "INPUT", "-i", c.Bridge, "-p", "udp", "--dport", "67", "-j", "ACCEPT"},
		[]string{"iptables", "-C", "INPUT", "-i", c.Bridge, "-d", c.HostIP, "-j", "DROP"},
	)
	return cmds
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
	if err := run([]string{"ip", "addr", "replace", fmt.Sprintf("%s/%d", c.HostIP, c.prefixLen()), "dev", c.Bridge}); err != nil {
		return err
	}
	if err := run([]string{"ip", "link", "set", c.Bridge, "up"}); err != nil {
		return err
	}
	if err := enableIPForward(); err != nil {
		return err
	}
	if err := ensureIptables(c); err != nil {
		return err
	}
	return nil
}

func enableIPForward() error {
	const path = "/proc/sys/net/ipv4/ip_forward"
	b, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if strings.TrimSpace(string(b)) == "1" {
		return nil
	}
	if err := os.WriteFile(path, []byte("1\n"), 0o644); err != nil {
		return fmt.Errorf("enable ip_forward: %w", err)
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
	if c.RegistryLocal != c.RegistryPort {
		rule := []string{
			"PREROUTING", "-i", c.Bridge, "-d", c.HostIP,
			"-p", "tcp", "--dport", strconv.Itoa(c.RegistryPort),
			"-j", "REDIRECT", "--to-ports", strconv.Itoa(c.RegistryLocal),
		}
		check := append([]string{"iptables", "-t", "nat", "-C"}, rule...)
		add := append([]string{"iptables", "-t", "nat", "-A"}, rule...)
		if err := runIgnore(check, ""); err != nil {
			if err := run(add); err != nil {
				return err
			}
		}
	}
	// DHCP replies generated by the host leave with an unfilled UDP
	// checksum (deferred to offload that a tap never performs); the
	// guests' DHCP client BPF drops such packets, so VMs would ignore
	// every offer. Fill it in like libvirt/dnsmasq do. Best effort:
	// exotic kernels without xt_CHECKSUM just skip it.
	csum := []string{"POSTROUTING", "-o", c.Bridge, "-p", "udp", "--dport", "68", "-j", "CHECKSUM", "--checksum-fill"}
	if err := runIgnore(append([]string{"iptables", "-t", "mangle", "-C"}, csum...), ""); err != nil {
		_ = run(append([]string{"iptables", "-t", "mangle", "-A"}, csum...))
	}
	// Insert outbound ACCEPT at the top so Docker/ufw FORWARD DROP
	// (policy or a later reject rule) cannot swallow VM traffic to GitHub.
	for _, r := range [][]string{
		{"FORWARD", "-i", c.Bridge, "!", "-o", c.Bridge, "-j", "ACCEPT"},
		{"FORWARD", "-o", c.Bridge, "-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "ACCEPT"},
	} {
		check := append([]string{"iptables", "-C"}, r...)
		insert := append([]string{"iptables", "-I"}, r...)
		if err := runIgnore(check, ""); err != nil {
			if err := run(insert); err != nil {
				return err
			}
		}
	}
	rules := [][]string{
		{"FORWARD", "-i", c.Bridge, "-o", c.Bridge, "-j", "DROP"},
		{"INPUT", "-i", c.Bridge, "-p", "tcp", "--dport", strconv.Itoa(c.RegistryLocal), "-d", c.HostIP, "-j", "ACCEPT"},
		{"INPUT", "-i", c.Bridge, "-p", "tcp", "--dport", strconv.Itoa(c.GuestPort), "-d", c.HostIP, "-j", "ACCEPT"},
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

// IPForIndex returns the i-th VM address inside cidr; host numbering
// starts at .2 (.0 is the network, .1 the host/bridge).
func IPForIndex(cidr string, i int) string {
	base := uint32(10)<<24 | uint32(87)<<16 // 10.87.0.0 fallback
	if _, ipnet, err := net.ParseCIDR(cidr); err == nil {
		if v4 := ipnet.IP.To4(); v4 != nil {
			base = binary.BigEndian.Uint32(v4)
		}
	}
	ip := make(net.IP, 4)
	binary.BigEndian.PutUint32(ip, base+uint32(i)+2)
	return ip.String()
}

// MaskFromCIDR returns the IPv4 mask of cidr (default /16).
func MaskFromCIDR(cidr string) net.IPMask {
	if _, ipnet, err := net.ParseCIDR(cidr); err == nil {
		return ipnet.Mask
	}
	return net.CIDRMask(16, 32)
}

// FindOverlap reports the first host network that overlaps cidr, ignoring
// the interface named ignoreIface (the gh-runnerd bridge itself). An empty
// string means no overlap was found.
func FindOverlap(cidr, ignoreIface string) string {
	_, want, err := net.ParseCIDR(cidr)
	if err != nil {
		return ""
	}
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, ifc := range ifaces {
		if ifc.Name == ignoreIface {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok || ipnet.IP.To4() == nil {
				continue
			}
			if netsOverlap(want, ipnet) {
				return fmt.Sprintf("%s (%s)", ifc.Name, ipnet.String())
			}
		}
	}
	return ""
}

func netsOverlap(a, b *net.IPNet) bool {
	return a.Contains(b.IP.Mask(b.Mask)) || b.Contains(a.IP.Mask(a.Mask))
}
