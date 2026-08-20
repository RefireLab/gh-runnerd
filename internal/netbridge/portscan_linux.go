//go:build linux

package netbridge

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// WhoBindsUDP names the processes holding UDP port (e.g. a dnsmasq that
// owns :67 and blocks the daemon's DHCP server). Best effort: sockets of
// other users are only resolvable as root; unknown holders yield "".
func WhoBindsUDP(port int) string {
	inodes := map[string]bool{}
	for _, f := range []string{"/proc/net/udp", "/proc/net/udp6"} {
		raw, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		lines := strings.Split(string(raw), "\n")
		for _, line := range lines[1:] {
			fields := strings.Fields(line)
			if len(fields) < 10 {
				continue
			}
			local := fields[1]
			i := strings.LastIndex(local, ":")
			if i < 0 {
				continue
			}
			p, err := strconv.ParseUint(local[i+1:], 16, 32)
			if err != nil || int(p) != port {
				continue
			}
			inodes[fields[9]] = true
		}
	}
	if len(inodes) == 0 {
		return ""
	}
	var out []string
	procs, err := os.ReadDir("/proc")
	if err != nil {
		return ""
	}
	for _, pe := range procs {
		pid := pe.Name()
		if _, err := strconv.Atoi(pid); err != nil {
			continue
		}
		fds, err := os.ReadDir(filepath.Join("/proc", pid, "fd"))
		if err != nil {
			continue
		}
		for _, fd := range fds {
			link, err := os.Readlink(filepath.Join("/proc", pid, "fd", fd.Name()))
			if err != nil || !strings.HasPrefix(link, "socket:[") {
				continue
			}
			inode := strings.TrimSuffix(strings.TrimPrefix(link, "socket:["), "]")
			if !inodes[inode] {
				continue
			}
			comm, _ := os.ReadFile(filepath.Join("/proc", pid, "comm"))
			out = append(out, fmt.Sprintf("%s (pid %s)", strings.TrimSpace(string(comm)), pid))
			break
		}
	}
	return strings.Join(out, ", ")
}
