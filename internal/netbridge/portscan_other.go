//go:build !linux

package netbridge

// WhoBindsUDP is Linux-only.
func WhoBindsUDP(int) string { return "" }
