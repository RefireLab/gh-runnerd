package netbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
)

// Lease is a DHCP mapping advertised to one VM.
type Lease struct {
	MAC    string
	IP     net.IP
	Mask   net.IPMask
	Router net.IP
	DNS    net.IP
}

// DHCP serves a tiny IPv4 DHCP on the isolated bridge.
type DHCP struct {
	mu      sync.RWMutex
	leases  map[string]Lease
	conn    *net.UDPConn
	log     *slog.Logger
	lastErr error
	iface   string
}

func NewDHCP(log *slog.Logger) *DHCP {
	return &DHCP{leases: map[string]Lease{}, log: log}
}

// Err reports why the server is not serving (bind conflict, socket death),
// or nil while it runs. The daemon folds this into selftest diagnostics.
func (d *DHCP) Err() error {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.lastErr
}

func (d *DHCP) setErr(err error) {
	d.mu.Lock()
	d.lastErr = err
	d.mu.Unlock()
}

// Set registers or replaces a lease keyed by MAC.
func (d *DHCP) Set(l Lease) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.leases[normalizeMAC(l.MAC)] = l
}

func (d *DHCP) Delete(mac string) {
	d.mu.Lock()
	l, had := d.leases[normalizeMAC(mac)]
	iface := d.iface
	delete(d.leases, normalizeMAC(mac))
	d.mu.Unlock()
	if had && iface != "" && l.IP != nil {
		// Drop the pinned neighbor entry from the unicast reply path.
		_ = run([]string{"ip", "neigh", "del", l.IP.String(), "dev", iface})
	}
}

func (d *DHCP) lookup(mac []byte) (Lease, bool) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	l, ok := d.leases[normalizeMAC(net.HardwareAddr(mac).String())]
	return l, ok
}

// ListenAndServe binds UDP :67 scoped to iface (the runner bridge).
//
// DHCPDISCOVER arrives addressed to 255.255.255.255, which a socket bound
// to the bridge's unicast address never receives; the reply also goes to
// 255.255.255.255 because the client has no IP yet, which requires
// SO_BROADCAST. Both are handled by the platform listen config.
func (d *DHCP) ListenAndServe(iface string) error {
	lc := net.ListenConfig{Control: dhcpControl(iface)}
	pc, err := lc.ListenPacket(context.Background(), "udp4", ":67")
	if err != nil {
		err = fmt.Errorf("dhcp listen :67 on %s: %w", iface, err)
		d.setErr(err)
		return err
	}
	conn := pc.(*net.UDPConn)
	d.mu.Lock()
	d.conn = conn
	d.lastErr = nil
	d.iface = iface
	d.mu.Unlock()
	if d.log != nil {
		d.log.Info("dhcp listening", "iface", iface, "port", 67)
	}
	buf := make([]byte, 1500)
	for {
		n, src, err := conn.ReadFromUDP(buf)
		if err != nil {
			d.setErr(err)
			return err
		}
		resp, err := d.handle(buf[:n])
		if err != nil || resp == nil {
			if err != nil && d.log != nil {
				d.log.Warn("dhcp request ignored", "src", src.String(), "err", err)
			}
			continue
		}
		offered := net.IP(resp[16:20])
		mac := net.HardwareAddr(resp[28:34])
		kind := replyTypeName(resp)
		if src != nil && !src.IP.IsUnspecified() && !src.IP.Equal(net.IPv4zero) {
			dst := &net.UDPAddr{IP: src.IP, Port: 68}
			if _, err := conn.WriteToUDP(resp, dst); err != nil && d.log != nil {
				d.log.Warn("dhcp reply", "type", kind, "dst", dst.String(), "err", err)
			} else if d.log != nil {
				d.log.Info("dhcp reply", "type", kind, "ip", offered.String(), "mac", mac.String())
			}
			continue
		}
		// The client has no IP yet. Send the reply both ways: broadcast
		// (classic path), and — dnsmasq-style — as L2 unicast to the
		// client's MAC via an injected neighbor entry, which survives
		// hosts where locally-generated broadcast never reaches bridge
		// ports. Raw-socket clients (systemd-networkd in the VMs) accept
		// either frame.
		_, bcastErr := conn.WriteToUDP(resp, &net.UDPAddr{IP: net.IPv4bcast, Port: 68})
		var uniErr error
		if err := neighReplace(d.iface, offered, mac); err == nil {
			_, uniErr = conn.WriteToUDP(resp, &net.UDPAddr{IP: offered, Port: 68})
		} else {
			uniErr = err
		}
		if d.log != nil {
			if bcastErr != nil && uniErr != nil {
				d.log.Warn("dhcp reply failed", "type", kind, "ip", offered.String(), "mac", mac.String(), "bcast_err", bcastErr, "unicast_err", uniErr)
			} else {
				d.log.Info("dhcp reply", "type", kind, "ip", offered.String(), "mac", mac.String())
			}
		}
	}
}

// replyTypeName decodes option 53 of a reply we built (always first).
func replyTypeName(resp []byte) string {
	if len(resp) > 242 && resp[240] == 53 {
		switch resp[242] {
		case 2:
			return "offer"
		case 5:
			return "ack"
		}
	}
	return "reply"
}

// neighReplace pins ip→mac on iface so a unicast UDP reply reaches a
// client that has not configured its address yet.
func neighReplace(iface string, ip net.IP, mac net.HardwareAddr) error {
	if iface == "" {
		return fmt.Errorf("dhcp iface unknown")
	}
	return run([]string{"ip", "neigh", "replace", ip.String(), "lladdr", mac.String(), "dev", iface})
}

func (d *DHCP) Close() error {
	if d.conn != nil {
		return d.conn.Close()
	}
	return nil
}

func (d *DHCP) handle(pkt []byte) ([]byte, error) {
	if len(pkt) < 240 {
		return nil, fmt.Errorf("short dhcp")
	}
	if binary.BigEndian.Uint32(pkt[236:240]) != 0x63825363 {
		return nil, fmt.Errorf("bad magic")
	}
	op := pkt[0]
	if op != 1 {
		return nil, fmt.Errorf("not a bootrequest")
	}
	mac := pkt[28 : 28+6]
	lease, ok := d.lookup(mac)
	if !ok {
		return nil, fmt.Errorf("no lease for %s", net.HardwareAddr(mac))
	}
	reqType := byte(0)
	opts := pkt[240:]
	for i := 0; i < len(opts); {
		if opts[i] == 255 {
			break
		}
		if opts[i] == 0 {
			i++
			continue
		}
		if i+1 >= len(opts) {
			break
		}
		l := int(opts[i+1])
		if opts[i] == 53 && l >= 1 && i+2 < len(opts) {
			reqType = opts[i+2]
		}
		i += 2 + l
	}
	var msgType byte
	switch reqType {
	case 1: // DISCOVER
		msgType = 2 // OFFER
	case 3, 8: // REQUEST, INFORM
		msgType = 5 // ACK
	default:
		return nil, fmt.Errorf("dhcp message type %d ignored", reqType)
	}
	return buildReply(pkt, lease, msgType), nil
}

func buildReply(req []byte, lease Lease, msgType byte) []byte {
	resp := make([]byte, 548)
	copy(resp, req)
	resp[0] = 2 // bootreply
	copy(resp[16:20], lease.IP.To4())
	copy(resp[20:24], lease.Router.To4())
	binary.BigEndian.PutUint32(resp[236:240], 0x63825363)
	opts := []byte{
		53, 1, msgType,
		1, 4, lease.Mask[0], lease.Mask[1], lease.Mask[2], lease.Mask[3],
		3, 4, lease.Router.To4()[0], lease.Router.To4()[1], lease.Router.To4()[2], lease.Router.To4()[3],
		54, 4, lease.Router.To4()[0], lease.Router.To4()[1], lease.Router.To4()[2], lease.Router.To4()[3],
		51, 4, 0, 1, 0x51, 0x80, // 1 day
		6, 4, 1, 1, 1, 1,
		255,
	}
	copy(resp[240:], opts)
	return resp
}

func normalizeMAC(s string) string {
	hw, err := net.ParseMAC(s)
	if err != nil {
		return strings.ToLower(s)
	}
	return hw.String()
}

// BuildDiscover crafts a minimal DHCPDISCOVER, used by the network
// self-test to probe the server end-to-end over the real bridge.
func BuildDiscover(mac net.HardwareAddr, xid uint32) []byte {
	pkt := make([]byte, 300)
	pkt[0] = 1 // bootrequest
	pkt[1] = 1 // ethernet
	pkt[2] = 6
	binary.BigEndian.PutUint32(pkt[4:8], xid)
	pkt[10] = 0x80 // broadcast flag: client has no IP yet
	copy(pkt[28:34], mac)
	binary.BigEndian.PutUint32(pkt[236:240], 0x63825363)
	copy(pkt[240:], []byte{53, 1, 1, 255})
	return pkt
}

// ParseReply extracts yiaddr and the DHCP message type from a server reply
// matching xid.
func ParseReply(pkt []byte, xid uint32) (net.IP, byte, error) {
	if len(pkt) < 244 {
		return nil, 0, fmt.Errorf("short dhcp reply")
	}
	if pkt[0] != 2 {
		return nil, 0, fmt.Errorf("not a bootreply")
	}
	if binary.BigEndian.Uint32(pkt[4:8]) != xid {
		return nil, 0, fmt.Errorf("xid mismatch")
	}
	if binary.BigEndian.Uint32(pkt[236:240]) != 0x63825363 {
		return nil, 0, fmt.Errorf("bad magic")
	}
	ip := make(net.IP, 4)
	copy(ip, pkt[16:20])
	msgType := byte(0)
	opts := pkt[240:]
	for i := 0; i < len(opts); {
		if opts[i] == 255 {
			break
		}
		if opts[i] == 0 {
			i++
			continue
		}
		if i+1 >= len(opts) {
			break
		}
		l := int(opts[i+1])
		if opts[i] == 53 && l >= 1 && i+2 < len(opts) {
			msgType = opts[i+2]
		}
		i += 2 + l
	}
	return ip, msgType, nil
}
