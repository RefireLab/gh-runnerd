package cli

import (
	"fmt"
	"net"
	"testing"
)

func TestParseHostCIDR(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in, host, cidr string
		wantErr        bool
	}{
		{in: "10.87.0.1/16", host: "10.87.0.1", cidr: "10.87.0.0/16"},
		{in: "10.99.0.1", host: "10.99.0.1", cidr: "10.99.0.0/16"},
		{in: "192.168.200.0/24", host: "192.168.200.1", cidr: "192.168.200.0/24"},
		{in: "10.5.7.9/8", host: "10.5.7.9", cidr: "10.0.0.0/8"},
		{in: "10.87.0.1/30", wantErr: true},
		{in: "not-an-ip", wantErr: true},
		{in: "fd00::1/64", wantErr: true},
	}
	for _, c := range cases {
		host, cidr, err := parseHostCIDR(c.in)
		if c.wantErr {
			if err == nil {
				t.Fatalf("%s: expected error, got %s %s", c.in, host, cidr)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: %v", c.in, err)
		}
		if host != c.host || cidr != c.cidr {
			t.Fatalf("%s: got %s %s want %s %s", c.in, host, cidr, c.host, c.cidr)
		}
	}
}

func TestProbePortAndFirstFree(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	busy := ln.Addr().(*net.TCPAddr).Port
	if err := probePort(busy); err == nil {
		t.Fatalf("port %d is held by the test, probe must fail", busy)
	}
	free := firstFreePort(busy)
	if free == busy {
		t.Fatal("firstFreePort returned the busy port")
	}
	if err := probePort(free); err != nil {
		t.Fatalf("firstFreePort returned busy port %d: %v", free, err)
	}
}

func TestSuggestSubnetIsParseable(t *testing.T) {
	t.Parallel()
	s := suggestSubnet("br-ghrunnerd")
	host, cidr, err := parseHostCIDR(s)
	if err != nil {
		t.Fatalf("suggestion %q: %v", s, err)
	}
	if host == "" || cidr == "" {
		t.Fatalf("suggestion %q parsed empty", s)
	}
	fmt.Println("suggested:", s)
}
