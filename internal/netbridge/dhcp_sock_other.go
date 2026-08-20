//go:build !linux

package netbridge

import (
	"fmt"
	"net"
	"syscall"
)

// The daemon only runs on Linux; other platforms compile for unit tests.
func dhcpControl(string) func(network, address string, c syscall.RawConn) error {
	return nil
}

// ListenDHCPClient is Linux-only.
func ListenDHCPClient(string) (*net.UDPConn, error) {
	return nil, fmt.Errorf("dhcp client socket requires linux")
}
