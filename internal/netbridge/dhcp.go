package netbridge

import (
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
	mu     sync.RWMutex
	leases map[string]Lease
	conn   *net.UDPConn
	log    *slog.Logger
}

func NewDHCP(log *slog.Logger) *DHCP {
	return &DHCP{leases: map[string]Lease{}, log: log}
}

// Set registers or replaces a lease keyed by MAC.
func (d *DHCP) Set(l Lease) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.leases[normalizeMAC(l.MAC)] = l
}

func (d *DHCP) Delete(mac string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.leases, normalizeMAC(mac))
}

func (d *DHCP) lookup(mac []byte) (Lease, bool) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	l, ok := d.leases[normalizeMAC(net.HardwareAddr(mac).String())]
	return l, ok
}

// ListenAndServe binds UDP 67 on hostIP.
func (d *DHCP) ListenAndServe(hostIP string) error {
	addr, err := net.ResolveUDPAddr("udp4", hostIP+":67")
	if err != nil {
		return err
	}
	conn, err := net.ListenUDP("udp4", addr)
	if err != nil {
		return err
	}
	d.conn = conn
	buf := make([]byte, 1500)
	for {
		n, src, err := conn.ReadFromUDP(buf)
		if err != nil {
			return err
		}
		resp, err := d.handle(buf[:n])
		if err != nil || resp == nil {
			continue
		}
		dst := &net.UDPAddr{IP: net.IPv4bcast, Port: 68}
		if src != nil && !src.IP.IsUnspecified() && !src.IP.Equal(net.IPv4zero) {
			dst = &net.UDPAddr{IP: src.IP, Port: 68}
		}
		_, _ = conn.WriteToUDP(resp, dst)
	}
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
	msgType := byte(3) // request default -> ACK
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
			if opts[i+2] == 1 {
				msgType = 2 // offer
			}
		}
		i += 2 + l
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
