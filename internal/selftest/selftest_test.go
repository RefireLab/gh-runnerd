package selftest

import (
	"log/slog"
	"net"
	"testing"
)

func TestProbeIPPicksLastUsable(t *testing.T) {
	t.Parallel()
	ip, prefix, err := probeIP("10.87.0.0/16", "10.87.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if ip.String() != "10.87.255.254" || prefix != 16 {
		t.Fatalf("got %s/%d", ip, prefix)
	}
	ip, _, err = probeIP("10.99.7.0/24", "10.99.7.1")
	if err != nil {
		t.Fatal(err)
	}
	if ip.String() != "10.99.7.254" {
		t.Fatalf("got %s", ip)
	}
	// Host on the last usable address must not collide with the probe.
	ip, _, err = probeIP("10.99.7.0/24", "10.99.7.254")
	if err != nil {
		t.Fatal(err)
	}
	if ip.String() != "10.99.7.253" {
		t.Fatalf("got %s", ip)
	}
	if _, _, err := probeIP("bogus", "10.87.0.1"); err == nil {
		t.Fatal("bad cidr must error")
	}
}

func TestReportEgressBroken(t *testing.T) {
	t.Parallel()
	r := Report{Steps: []Step{{Name: "dns", Status: OK}, {Name: "tcp443", Status: Fail, Fix: "x"}}}
	if !r.EgressBroken() {
		t.Fatal("fail step must break egress")
	}
	if got := r.FailedSteps(); len(got) != 1 || got[0] != "tcp443" {
		t.Fatalf("failed steps %v", got)
	}
	skipped := Report{Steps: []Step{{Name: "setup", Status: Skip}}}
	if skipped.EgressBroken() {
		t.Fatal("skipped probe must not block the daemon")
	}
	r.Log(slog.Default())
	if r.String() == "" {
		t.Fatal("report must render")
	}
}

func TestDNSQueryRoundTrip(t *testing.T) {
	t.Parallel()
	// Serve one canned DNS answer on localhost and resolve through it.
	pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	go func() {
		buf := make([]byte, 512)
		n, addr, err := pc.ReadFrom(buf)
		if err != nil {
			return
		}
		q := buf[:n]
		resp := append([]byte{}, q...)
		resp[2] = 0x81 // response, recursion desired
		resp[3] = 0x80 // recursion available, rcode 0
		resp[7] = 1    // one answer
		// answer: pointer to name at 0x0c, type A, class IN, ttl 60, rdlen 4, 1.2.3.4
		resp = append(resp, 0xc0, 0x0c, 0, 1, 0, 1, 0, 0, 0, 60, 0, 4, 1, 2, 3, 4)
		_, _ = pc.WriteTo(resp, addr)
	}()
	ip, err := queryA(pc.LocalAddr().String(), "api.github.com", 2e9)
	if err != nil {
		t.Fatal(err)
	}
	if ip.String() != "1.2.3.4" {
		t.Fatalf("resolved %s", ip)
	}
}
