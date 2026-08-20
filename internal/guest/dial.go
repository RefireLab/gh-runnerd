package guest

import (
	"fmt"
	"net"
	"os"
	"time"

	"github.com/mdlayher/vsock"
)

// vsockDial is the vsock path (overridable in tests). Existing golden
// images dial vsock first with no deadline; a missing host listener then
// hangs past vm.boot_timeout. New agents try TCP first.
var vsockDial = func(cid, port uint32, timeout time.Duration) (net.Conn, error) {
	return withTimeout(timeout, func() (net.Conn, error) {
		return vsock.Dial(cid, port, nil)
	})
}

func dialHost(hostIP string, port int) (net.Conn, error) {
	addr := net.JoinHostPort(hostIP, fmt.Sprintf("%d", port))
	if c, err := net.DialTimeout("tcp", addr, 5*time.Second); err == nil {
		return c, nil
	}
	return vsockDial(2, uint32(port), 2*time.Second)
}

func withTimeout(d time.Duration, dial func() (net.Conn, error)) (net.Conn, error) {
	type result struct {
		c   net.Conn
		err error
	}
	ch := make(chan result, 1)
	go func() {
		c, err := dial()
		ch <- result{c, err}
	}()
	select {
	case r := <-ch:
		return r.c, r.err
	case <-time.After(d):
		go func() {
			r := <-ch
			if r.c != nil {
				_ = r.c.Close()
			}
		}()
		return nil, os.ErrDeadlineExceeded
	}
}
