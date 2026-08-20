//go:build !linux

package selftest

// Run is Linux-only; other platforms report a skipped probe.
func Run(o Options) Report {
	return Report{Steps: []Step{{Name: "setup", Status: Skip, Detail: "egress probe requires linux"}}}
}
