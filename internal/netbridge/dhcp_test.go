package netbridge

import (
	"net"
	"testing"
)

func TestRequestGetsACK(t *testing.T) {
	t.Parallel()
	d := NewDHCP(nil)
	d.Set(Lease{
		MAC:    "52:54:00:87:00:01",
		IP:     net.ParseIP("10.87.0.3").To4(),
		Mask:   net.CIDRMask(16, 32),
		Router: net.ParseIP("10.87.0.1").To4(),
	})
	req := make([]byte, 244)
	req[0] = 1
	req[2] = 6
	mac, _ := net.ParseMAC("52:54:00:87:00:01")
	copy(req[28:], mac)
	req[236] = 0x63
	req[237] = 0x82
	req[238] = 0x53
	req[239] = 0x63
	copy(req[240:], []byte{53, 1, 3, 255}) // DHCPREQUEST
	resp, err := d.handle(req)
	if err != nil {
		t.Fatal(err)
	}
	ip := net.IP(resp[16:20])
	if !ip.Equal(net.ParseIP("10.87.0.3")) {
		t.Fatalf("yiaddr %s", ip)
	}
	// A REQUEST must be answered with ACK (5); answering with 3 makes
	// clients ignore the reply and never configure the address.
	if resp[240] != 53 || resp[242] != 5 {
		t.Fatalf("expected ACK (5), got option %d value %d", resp[240], resp[242])
	}
	if got := replyTypeName(resp); got != "ack" {
		t.Fatalf("reply type name %q", got)
	}
}

func TestPacketWithoutMessageTypeIgnored(t *testing.T) {
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
	if _, err := d.handle(req); err == nil {
		t.Fatal("a request without option 53 must be ignored")
	}
}

func TestDiscoverOfferRoundTrip(t *testing.T) {
	t.Parallel()
	d := NewDHCP(nil)
	d.Set(Lease{
		MAC:    "52:54:00:87:ff:fe",
		IP:     net.ParseIP("10.87.255.254").To4(),
		Mask:   net.CIDRMask(16, 32),
		Router: net.ParseIP("10.87.0.1").To4(),
	})
	mac, _ := net.ParseMAC("52:54:00:87:ff:fe")
	req := BuildDiscover(mac, 0xdeadbeef)
	resp, err := d.handle(req)
	if err != nil {
		t.Fatal(err)
	}
	ip, msgType, err := ParseReply(resp, 0xdeadbeef)
	if err != nil {
		t.Fatal(err)
	}
	if msgType != 2 {
		t.Fatalf("expected offer (2), got %d", msgType)
	}
	if !ip.Equal(net.ParseIP("10.87.255.254")) {
		t.Fatalf("yiaddr %s", ip)
	}
	if _, _, err := ParseReply(resp, 0x1111); err == nil {
		t.Fatal("xid mismatch must be rejected")
	}
}

func TestDHCPErrTracksBindFailure(t *testing.T) {
	t.Parallel()
	d := NewDHCP(nil)
	if err := d.Err(); err != nil {
		t.Fatalf("fresh server must have no error: %v", err)
	}
	d.setErr(net.ErrClosed)
	if err := d.Err(); err == nil {
		t.Fatal("recorded error must surface")
	}
	d.setErr(nil)
	if err := d.Err(); err != nil {
		t.Fatalf("cleared error must be nil: %v", err)
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
