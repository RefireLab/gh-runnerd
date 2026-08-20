// Package selftest verifies the VM datapath before any runner boots: it
// attaches a probe interface to the runner bridge inside a network
// namespace and checks, exactly like a VM would, that DHCP answers, the
// guest control port accepts, DNS resolves, and TCP 443 egress works.
package selftest

import (
	"encoding/binary"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"time"

	"github.com/RefireLab/gh-runnerd/internal/netbridge"
)

// Status of one probe step.
type Status string

const (
	OK   Status = "ok"
	Fail Status = "fail"
	Skip Status = "skip"
)

// Step is one verified stage of the VM datapath.
type Step struct {
	Name   string `json:"name"`
	Status Status `json:"status"`
	Detail string `json:"detail,omitempty"`
	Fix    string `json:"fix,omitempty"`
}

// Report is the outcome of a self-test run.
type Report struct {
	Steps []Step `json:"steps"`
}

// EgressBroken reports whether any step definitively failed. Skipped
// steps (missing tools, no permissions) never block the daemon.
func (r Report) EgressBroken() bool {
	for _, s := range r.Steps {
		if s.Status == Fail {
			return true
		}
	}
	return false
}

// FailedSteps lists the names of failing steps.
func (r Report) FailedSteps() []string {
	var out []string
	for _, s := range r.Steps {
		if s.Status == Fail {
			out = append(out, s.Name)
		}
	}
	return out
}

// Log writes one line per step: what was checked, what was found, and the
// recommended fix on failure.
func (r Report) Log(log *slog.Logger) {
	if log == nil {
		return
	}
	for _, s := range r.Steps {
		switch s.Status {
		case OK:
			log.Info("selftest "+s.Name+" ok", "detail", s.Detail)
		case Fail:
			log.Error("selftest "+s.Name+" FAILED", "detail", s.Detail, "fix", s.Fix)
		case Skip:
			log.Warn("selftest "+s.Name+" skipped", "detail", s.Detail)
		default:
			panic(fmt.Sprintf("unknown selftest status %q", s.Status))
		}
	}
}

func (r Report) String() string {
	var b strings.Builder
	for _, s := range r.Steps {
		fmt.Fprintf(&b, "%-6s %-14s %s", strings.ToUpper(string(s.Status)), s.Name, s.Detail)
		if s.Fix != "" {
			fmt.Fprintf(&b, " — fix: %s", s.Fix)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// LeaseTable is the daemon's in-memory DHCP table; the probe registers a
// temporary lease so the discover gets a real answer.
type LeaseTable interface {
	Set(l netbridge.Lease)
	Delete(mac string)
}

// Options select what to probe.
type Options struct {
	Bridge    string
	CIDR      string
	HostIP    string
	GuestPort int
	// DHCP, when set, enables the end-to-end DHCP probe against the
	// daemon's own server. nil (e.g. from doctor) skips it.
	DHCP LeaseTable
	// Control enables the guest control port dial (requires the daemon
	// to be listening).
	Control bool
	// ProbeHost is resolved and dialed on :443. Default api.github.com.
	ProbeHost string
	// DNS server VMs use, host:port. Default 1.1.1.1:53.
	DNS     string
	Timeout time.Duration
	Log     *slog.Logger
}

func (o Options) withDefaults() Options {
	if o.ProbeHost == "" {
		o.ProbeHost = "api.github.com"
	}
	if o.DNS == "" {
		o.DNS = "1.1.1.1:53"
	}
	if o.Timeout <= 0 {
		o.Timeout = 5 * time.Second
	}
	return o
}

// probeIP returns the last usable address of cidr for the probe NIC, far
// from the pool's low addresses, never colliding with hostIP.
func probeIP(cidr, hostIP string) (net.IP, int, error) {
	_, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return nil, 0, fmt.Errorf("cidr %q: %w", cidr, err)
	}
	v4 := ipnet.IP.To4()
	if v4 == nil {
		return nil, 0, fmt.Errorf("cidr %q is not IPv4", cidr)
	}
	ones, _ := ipnet.Mask.Size()
	base := binary.BigEndian.Uint32(v4)
	bcast := base | (^uint32(0) >> ones)
	cand := bcast - 1
	host := net.ParseIP(hostIP).To4()
	if host != nil && binary.BigEndian.Uint32(host) == cand {
		cand--
	}
	ip := make(net.IP, 4)
	binary.BigEndian.PutUint32(ip, cand)
	return ip, ones, nil
}
