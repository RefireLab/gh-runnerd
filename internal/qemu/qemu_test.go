package qemu

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestCreateOverlayNeverShrinksBacking guards against truncated runtime
// disks: an essential/full golden image is bigger than vm.disk, and a
// smaller overlay silently corrupts the guest filesystem.
func TestCreateOverlayNeverShrinksBacking(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("qemu-img"); err != nil {
		t.Skip("qemu-img not installed")
	}
	dir := t.TempDir()
	backing := filepath.Join(dir, "base.qcow2")
	if out, err := exec.Command("qemu-img", "create", "-f", "qcow2", backing, "8G").CombinedOutput(); err != nil {
		t.Fatalf("create backing: %v: %s", err, out)
	}
	overlay := filepath.Join(dir, "vm.qcow2")
	// Ask for 2G over an 8G backing: must inherit 8G, not truncate.
	if err := CreateOverlay(backing, overlay, 2); err != nil {
		t.Fatal(err)
	}
	got, err := virtualSizeGB(overlay)
	if err != nil {
		t.Fatal(err)
	}
	if got != 8 {
		t.Fatalf("overlay virtual size = %dG, want 8G (backing size)", got)
	}
	// Asking for more than the backing must still grow.
	overlay2 := filepath.Join(dir, "vm2.qcow2")
	if err := CreateOverlay(backing, overlay2, 12); err != nil {
		t.Fatal(err)
	}
	if got, err = virtualSizeGB(overlay2); err != nil || got != 12 {
		t.Fatalf("overlay2 virtual size = %dG (%v), want 12G", got, err)
	}
}

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

func TestApplyVsockAvailabilityDisablesWhenHostCannotListen(t *testing.T) {
	t.Parallel()
	if HostVsockAvailable() {
		t.Skip("/dev/vsock is present on this host")
	}
	spec := applyVsockAvailability(Spec{CID: 3})
	if !spec.DisableVsock {
		t.Fatal("expected DisableVsock when /dev/vsock is missing")
	}
}

func TestCmdLineOmitsVsockWhenDisabled(t *testing.T) {
	t.Parallel()
	args := CmdLine(Spec{
		Name:         "job-1",
		Overlay:      "o.qcow2",
		CPUs:         2,
		MemoryMB:     4096,
		CID:          3,
		MAC:          "52:54:00:12:34:56",
		TAP:          "tap-ghrd0",
		DisableVsock: true,
	})
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "vhost-vsock") {
		t.Fatalf("vsock must be omitted when host cannot listen: %s", joined)
	}
}
