package netbridge

import (
	"net"
	"testing"
)

func TestDHCPOfferForRegisteredMAC(t *testing.T) {
	t.Parallel()
	d := NewDHCP(nil)
	d.Set(Lease{
		MAC:    "52:54:00:87:00:01",
		IP:     net.ParseIP("10.87.0.3").To4(),
		Mask:   net.CIDRMask(16, 32),
		Router: net.ParseIP("10.87.0.1").To4(),
	})
	req := make([]byte, 240)
	req[0] = 1
	req[2] = 6
	mac, _ := net.ParseMAC("52:54:00:87:00:01")
	copy(req[28:], mac)
	req[236] = 0x63
	req[237] = 0x82
	req[238] = 0x53
	req[239] = 0x63
	resp, err := d.handle(req)
	if err != nil {
		t.Fatal(err)
	}
	ip := net.IP(resp[16:20])
	if !ip.Equal(net.ParseIP("10.87.0.3")) {
		t.Fatalf("yiaddr %s", ip)
	}
}

func TestDHCPUnknownMAC(t *testing.T) {
	t.Parallel()
	d := NewDHCP(nil)
	req := make([]byte, 240)
	req[0] = 1
	req[236] = 0x63
	req[237] = 0x82
	req[238] = 0x53
	req[239] = 0x63
	if _, err := d.handle(req); err == nil {
		t.Fatal("expected error")
	}
}
