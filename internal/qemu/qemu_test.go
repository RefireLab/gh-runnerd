package qemu

import (
	"strings"
	"testing"
)

func TestCmdLineContainsIsolationFlags(t *testing.T) {
	t.Parallel()
	args := CmdLine(Spec{
		Name:     "job-1",
		Overlay:  "/var/lib/gh-runnerd/runtime/job-1.qcow2",
		CPUs:     2,
		MemoryMB: 4096,
		CID:      3,
		MAC:      "52:54:00:12:34:56",
		TAP:      "tap-ghrd0",
	})
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"q35,accel=kvm",
		"-cpu host",
		"vhost-vsock-pci,guest-cid=3",
		"tap,id=net0,ifname=tap-ghrd0,script=no,downscript=no",
		"file=/var/lib/gh-runnerd/runtime/job-1.qcow2",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in %s", want, joined)
		}
	}
	if strings.Contains(joined, "0.0.0.0") {
		t.Fatal("must not publish host ports")
	}
}

func TestCmdLineOmitsVsockWhenCIDLow(t *testing.T) {
	t.Parallel()
	args := CmdLine(Spec{Name: "x", Overlay: "o.qcow2", CPUs: 1, MemoryMB: 512, TAP: "tap0", MAC: "52:54:00:00:00:01"})
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "vhost-vsock") {
		t.Fatalf("unexpected vsock: %s", joined)
	}
}
