package selftest

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"net"
	"strings"
	"time"
)

// queryA resolves host's first A record against server (host:port) with a
// hand-rolled UDP query. The stdlib resolver hops goroutines, which would
// escape the probe's network namespace; this stays on the calling thread.
func queryA(server, host string, timeout time.Duration) (net.IP, error) {
	msg, id, err := buildQuery(host)
	if err != nil {
		return nil, err
	}
	conn, err := net.DialTimeout("udp", server, timeout)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	if _, err := conn.Write(msg); err != nil {
		return nil, err
	}
	buf := make([]byte, 1500)
	n, err := conn.Read(buf)
	if err != nil {
		return nil, err
	}
	return parseA(buf[:n], id)
}

func buildQuery(host string) ([]byte, uint16, error) {
	var idb [2]byte
	_, _ = rand.Read(idb[:])
	id := binary.BigEndian.Uint16(idb[:])
	var b []byte
	b = binary.BigEndian.AppendUint16(b, id)
	b = binary.BigEndian.AppendUint16(b, 0x0100) // recursion desired
	b = binary.BigEndian.AppendUint16(b, 1)      // one question
	b = append(b, 0, 0, 0, 0, 0, 0)              // an/ns/ar counts
	for _, label := range strings.Split(strings.TrimSuffix(host, "."), ".") {
		if len(label) == 0 || len(label) > 63 {
			return nil, 0, fmt.Errorf("bad dns label %q in %q", label, host)
		}
		b = append(b, byte(len(label)))
		b = append(b, label...)
	}
	b = append(b, 0)
	b = binary.BigEndian.AppendUint16(b, 1) // type A
	b = binary.BigEndian.AppendUint16(b, 1) // class IN
	return b, id, nil
}

func parseA(pkt []byte, id uint16) (net.IP, error) {
	if len(pkt) < 12 {
		return nil, fmt.Errorf("short dns reply")
	}
	if binary.BigEndian.Uint16(pkt[0:2]) != id {
		return nil, fmt.Errorf("dns id mismatch")
	}
	if rcode := pkt[3] & 0x0f; rcode != 0 {
		return nil, fmt.Errorf("dns rcode %d", rcode)
	}
	qd := int(binary.BigEndian.Uint16(pkt[4:6]))
	an := int(binary.BigEndian.Uint16(pkt[6:8]))
	off := 12
	for i := 0; i < qd; i++ {
		end, err := skipName(pkt, off)
		if err != nil {
			return nil, err
		}
		off = end + 4
	}
	for i := 0; i < an; i++ {
		end, err := skipName(pkt, off)
		if err != nil {
			return nil, err
		}
		off = end
		if off+10 > len(pkt) {
			return nil, fmt.Errorf("truncated dns answer")
		}
		typ := binary.BigEndian.Uint16(pkt[off : off+2])
		rdlen := int(binary.BigEndian.Uint16(pkt[off+8 : off+10]))
		off += 10
		if off+rdlen > len(pkt) {
			return nil, fmt.Errorf("truncated dns rdata")
		}
		if typ == 1 && rdlen == 4 {
			ip := make(net.IP, 4)
			copy(ip, pkt[off:off+4])
			return ip, nil
		}
		off += rdlen
	}
	return nil, fmt.Errorf("no A record in reply")
}

func skipName(pkt []byte, off int) (int, error) {
	for {
		if off >= len(pkt) {
			return 0, fmt.Errorf("truncated dns name")
		}
		l := int(pkt[off])
		switch {
		case l == 0:
			return off + 1, nil
		case l&0xc0 == 0xc0:
			return off + 2, nil
		default:
			off += 1 + l
		}
	}
}
