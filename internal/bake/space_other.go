//go:build !linux

package bake

import "fmt"

// freeGB is unavailable off Linux; the preflight check is skipped.
func freeGB(dir string) (int, error) {
	return 0, fmt.Errorf("free-space check unsupported on this OS")
}
