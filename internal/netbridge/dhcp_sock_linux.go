//go:build linux

package netbridge

import (
	"context"
	"fmt"
	"net"
	"syscall"

	"golang.org/x/sys/unix"
)

// dhcpControl prepares the server socket: reusable across restarts, able
// to send to 255.255.255.255, and scoped to the runner bridge so DHCP on
// other host interfaces is never seen or answered.
func dhcpControl(iface string) func(network, address string, c syscall.RawConn) error {
	return func(_, _ string, c syscall.RawConn) error {
		var serr error
		err := c.Control(func(fd uintptr) {
			if e := unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEADDR, 1); e != nil && serr == nil {
				serr = fmt.Errorf("SO_REUSEADDR: %w", e)
			}
			if e := unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_BROADCAST, 1); e != nil && serr == nil {
				serr = fmt.Errorf("SO_BROADCAST: %w", e)
			}
			if iface != "" {
				if e := unix.BindToDevice(int(fd), iface); e != nil && serr == nil {
					serr = fmt.Errorf("SO_BINDTODEVICE %s: %w", iface, e)
				}
			}
		})
		if err != nil {
			return err
		}
		return serr
	}
}

// ListenDHCPClient opens a broadcast-capable client socket on :68 bound to
// iface. The network self-test uses it to send a discover with no source
// IP, exactly like a booting VM.
func ListenDHCPClient(iface string) (*net.UDPConn, error) {
	lc := net.ListenConfig{Control: dhcpControl(iface)}
	pc, err := lc.ListenPacket(context.Background(), "udp4", ":68")
	if err != nil {
		return nil, err
	}
	return pc.(*net.UDPConn), nil
}
