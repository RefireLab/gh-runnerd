//go:build linux

package bake

import "golang.org/x/sys/unix"

// freeGB returns the free space of the filesystem holding dir.
func freeGB(dir string) (int, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return 0, err
	}
	return int(uint64(st.Bavail) * uint64(st.Bsize) >> 30), nil
}
