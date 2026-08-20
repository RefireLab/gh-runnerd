//go:build linux

package selftest

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/RefireLab/gh-runnerd/internal/netbridge"
)

const (
	nsName   = "gh-runnerd-check"
	vethHost = "ghrdchk0"
	vethNS   = "ghrdchk1"
	probeMAC = "52:54:00:87:ff:fe"
)

// Run probes the VM datapath. It never boots a VM: a veth pair attached
// to the bridge inside a network namespace behaves exactly like one.
func Run(o Options) Report {
	o = o.withDefaults()
	ip, prefix, err := probeIP(o.CIDR, o.HostIP)
	if err != nil {
		return Report{Steps: []Step{{Name: "setup", Status: Skip, Detail: err.Error()}}}
	}
	if _, err := exec.LookPath("ip"); err != nil {
		return Report{Steps: []Step{{Name: "setup", Status: Skip, Detail: "ip (iproute2) not in PATH"}}}
	}
	if os.Geteuid() != 0 {
		return Report{Steps: []Step{{Name: "setup", Status: Skip, Detail: "egress probe needs root"}}}
	}

	cleanup := func() {
		_ = ipCmd("netns", "del", nsName)
		_ = ipCmd("link", "del", vethHost)
	}
	cleanup()
	setup := [][]string{
		{"netns", "add", nsName},
		{"link", "add", vethHost, "type", "veth", "peer", "name", vethNS},
		{"link", "set", vethHost, "master", o.Bridge, "up"},
		{"link", "set", vethNS, "netns", nsName, "address", probeMAC},
		{"-n", nsName, "link", "set", "lo", "up"},
		{"-n", nsName, "link", "set", vethNS, "up"},
	}
	for _, args := range setup {
		if err := ipCmd(args...); err != nil {
			cleanup()
			return Report{Steps: []Step{{Name: "setup", Status: Skip, Detail: err.Error()}}}
		}
	}
	defer cleanup()

	var rep Report
	add := func(s Step) { rep.Steps = append(rep.Steps, s) }

	// DHCP first, from an interface with no IP — exactly like a booting VM.
	if o.DHCP != nil {
		mac, _ := net.ParseMAC(probeMAC)
		o.DHCP.Set(netbridge.Lease{
			MAC:    probeMAC,
			IP:     ip,
			Mask:   net.CIDRMask(prefix, 32),
			Router: net.ParseIP(o.HostIP).To4(),
		})
		defer o.DHCP.Delete(probeMAC)
		step := Step{Name: "dhcp", Status: OK, Detail: fmt.Sprintf("offer %s on %s", ip, o.Bridge)}
		if err := inNS(func() error { return dhcpProbe(mac, ip, o.Timeout) }); err != nil {
			step = Step{Name: "dhcp", Status: Fail, Detail: err.Error(),
				Fix: "no DHCP answer on the bridge — check that nothing else binds :67 (ss -ulpn 'sport = :67') and restart serve"}
		}
		add(step)
	}

	if err := ipCmd("-n", nsName, "addr", "add", fmt.Sprintf("%s/%d", ip, prefix), "dev", vethNS); err != nil {
		add(Step{Name: "setup", Status: Skip, Detail: err.Error()})
		return rep
	}
	if err := ipCmd("-n", nsName, "route", "add", "default", "via", o.HostIP); err != nil {
		add(Step{Name: "setup", Status: Skip, Detail: err.Error()})
		return rep
	}

	if o.Control {
		step := Step{Name: "control", Status: OK, Detail: fmt.Sprintf("tcp %s:%d reachable", o.HostIP, o.GuestPort)}
		err := inNS(func() error {
			c, err := net.DialTimeout("tcp", net.JoinHostPort(o.HostIP, fmt.Sprint(o.GuestPort)), o.Timeout)
			if err == nil {
				_ = c.Close()
			}
			return err
		})
		if err != nil {
			step = Step{Name: "control", Status: Fail, Detail: err.Error(),
				Fix: "guest control port unreachable from VMs — INPUT ACCEPT rules missing; run serve as root so it installs them"}
		}
		add(step)
	}

	var resolved net.IP
	dnsStep := Step{Name: "dns", Status: OK}
	if err := inNS(func() error {
		addr, err := queryA(o.DNS, o.ProbeHost, o.Timeout)
		if err != nil {
			return err
		}
		resolved = addr
		return nil
	}); err != nil {
		dnsStep = Step{Name: "dns", Status: Fail, Detail: fmt.Sprintf("%s via %s: %v", o.ProbeHost, o.DNS, err),
			Fix: "DNS is unreachable from the VM subnet — an upstream firewall, ufw, or cloud security group blocks forwarded egress; check iptables -L FORWARD -nv and the provider firewall"}
	} else {
		dnsStep.Detail = fmt.Sprintf("%s -> %s via %s", o.ProbeHost, resolved, o.DNS)
	}
	add(dnsStep)

	target := resolved
	if target == nil {
		// DNS failed; still measure raw TCP egress to separate the causes.
		target = net.ParseIP(strings.Split(o.DNS, ":")[0])
	}
	tcpStep := Step{Name: "tcp443", Status: OK, Detail: fmt.Sprintf("tcp %s:443 reachable", target)}
	if err := inNS(func() error {
		c, err := net.DialTimeout("tcp", net.JoinHostPort(target.String(), "443"), o.Timeout)
		if err == nil {
			_ = c.Close()
		}
		return err
	}); err != nil {
		tcpStep = Step{Name: "tcp443", Status: Fail, Detail: fmt.Sprintf("%s:443: %v", target, err),
			Fix: "TCP 443 egress from the VM subnet is blocked — check iptables FORWARD counters, ufw route rules, and the provider firewall"}
	}
	add(tcpStep)
	return rep
}

// dhcpProbe broadcasts a discover with no source IP and expects the
// daemon's server to offer want.
func dhcpProbe(mac net.HardwareAddr, want net.IP, timeout time.Duration) error {
	conn, err := netbridge.ListenDHCPClient(vethNS)
	if err != nil {
		return err
	}
	defer conn.Close()
	var xb [4]byte
	_, _ = rand.Read(xb[:])
	xid := binary.BigEndian.Uint32(xb[:])
	pkt := netbridge.BuildDiscover(mac, xid)
	dst := &net.UDPAddr{IP: net.IPv4bcast, Port: 67}
	deadline := time.Now().Add(timeout)
	for attempt := 0; time.Now().Before(deadline); attempt++ {
		if _, err := conn.WriteToUDP(pkt, dst); err != nil {
			return fmt.Errorf("send discover: %w", err)
		}
		_ = conn.SetReadDeadline(time.Now().Add(time.Second))
		buf := make([]byte, 1500)
		n, _, err := conn.ReadFromUDP(buf)
		if err != nil {
			continue
		}
		got, msgType, perr := netbridge.ParseReply(buf[:n], xid)
		if perr != nil {
			continue
		}
		if msgType != 2 {
			return fmt.Errorf("expected offer, got dhcp message type %d", msgType)
		}
		if !got.Equal(want) {
			return fmt.Errorf("offered %s, expected %s", got, want)
		}
		return nil
	}
	return fmt.Errorf("no offer within %s", timeout)
}

// inNS runs fn with the current thread switched into the probe netns. New
// sockets inherit the thread's namespace at creation, so every dial inside
// fn originates from the bridge like VM traffic.
func inNS(fn func() error) error {
	errCh := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		orig, err := os.Open("/proc/thread-self/ns/net")
		if err != nil {
			errCh <- err
			return
		}
		defer orig.Close()
		h, err := os.Open("/run/netns/" + nsName)
		if err != nil {
			errCh <- err
			return
		}
		defer h.Close()
		if err := unix.Setns(int(h.Fd()), unix.CLONE_NEWNET); err != nil {
			errCh <- fmt.Errorf("setns: %w", err)
			return
		}
		res := fn()
		if err := unix.Setns(int(orig.Fd()), unix.CLONE_NEWNET); err == nil {
			runtime.UnlockOSThread()
		}
		// On restore failure the thread stays locked and dies with this
		// goroutine instead of rejoining the pool in the wrong netns.
		errCh <- res
	}()
	return <-errCh
}

func ipCmd(args ...string) error {
	out, err := exec.Command("ip", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("ip %s: %s (%w)", strings.Join(args, " "), strings.TrimSpace(string(out)), err)
	}
	return nil
}
