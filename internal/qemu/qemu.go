package qemu

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Spec describes one disposable runner VM.
type Spec struct {
	Name       string
	Backing    string
	Overlay    string
	CPUs       int
	MemoryMB   int
	DiskGB     int
	CID        uint32
	MAC        string
	TAP        string
	QEMUBinary string
}

// CmdLine is the qemu-system argument vector (without the binary).
func CmdLine(spec Spec) []string {
	binHint := spec.QEMUBinary
	_ = binHint
	args := []string{
		"-name", spec.Name,
		"-machine", "q35,accel=kvm",
		"-cpu", "host",
		"-smp", strconv.Itoa(spec.CPUs),
		"-m", strconv.Itoa(spec.MemoryMB),
		"-nographic",
		"-no-reboot",
		"-device", "virtio-rng-pci",
		"-drive", fmt.Sprintf("if=virtio,format=qcow2,file=%s,discard=unmap", spec.Overlay),
		"-netdev", fmt.Sprintf("tap,id=net0,ifname=%s,script=no,downscript=no", spec.TAP),
		"-device", fmt.Sprintf("virtio-net-pci,netdev=net0,mac=%s", spec.MAC),
	}
	if spec.CID >= 3 {
		args = append(args, "-device", fmt.Sprintf("vhost-vsock-pci,guest-cid=%d", spec.CID))
	}
	return args
}

// CreateOverlay makes a copy-on-write disk backed by the golden qcow2.
func CreateOverlay(backing, overlay string, diskGB int) error {
	if err := os.MkdirAll(filepath.Dir(overlay), 0o700); err != nil {
		return err
	}
	if _, err := os.Stat(backing); err != nil {
		return fmt.Errorf("runner image %s: %w", backing, err)
	}
	args := []string{"create", "-f", "qcow2", "-F", "qcow2", "-b", backing, overlay}
	if diskGB > 0 {
		args = append(args, fmt.Sprintf("%dG", diskGB))
	}
	cmd := exec.Command("qemu-img", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("qemu-img create overlay: %s (%w)", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// Instance is a running QEMU process.
type Instance struct {
	Spec Spec
	cmd  *exec.Cmd
}

// Start launches QEMU. Caller must have already created the TAP device.
func Start(ctx context.Context, spec Spec) (*Instance, error) {
	bin := spec.QEMUBinary
	if bin == "" {
		bin = DefaultBinary()
	}
	if _, err := exec.LookPath(bin); err != nil {
		return nil, fmt.Errorf("%s not found in PATH (install qemu-system-x86 / qemu-system-arm)", bin)
	}
	if _, err := os.Stat("/dev/kvm"); err != nil {
		return nil, fmt.Errorf("/dev/kvm is missing; gh-runnerd requires KVM on Ubuntu")
	}
	cmd := exec.CommandContext(ctx, bin, CmdLine(spec)...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start qemu: %w", err)
	}
	return &Instance{Spec: spec, cmd: cmd}, nil
}

// PID returns the QEMU process id.
func (i *Instance) PID() int {
	if i == nil || i.cmd == nil || i.cmd.Process == nil {
		return 0
	}
	return i.cmd.Process.Pid
}

// Wait blocks until QEMU exits.
func (i *Instance) Wait() error {
	return i.cmd.Wait()
}

// Kill terminates QEMU and removes the overlay disk.
func (i *Instance) Kill() error {
	if i.cmd != nil && i.cmd.Process != nil {
		_ = i.cmd.Process.Signal(syscall.SIGTERM)
		done := make(chan struct{})
		go func() {
			_ = i.cmd.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(8 * time.Second):
			_ = i.cmd.Process.Kill()
		}
	}
	if i.Spec.Overlay != "" {
		_ = os.Remove(i.Spec.Overlay)
	}
	return nil
}

// DefaultBinary picks qemu-system-* for the host arch.
func DefaultBinary() string {
	if _, err := exec.LookPath("qemu-system-x86_64"); err == nil {
		return "qemu-system-x86_64"
	}
	if _, err := exec.LookPath("qemu-system-aarch64"); err == nil {
		return "qemu-system-aarch64"
	}
	return "qemu-system-x86_64"
}

// HasKVM reports whether /dev/kvm exists.
func HasKVM() bool {
	_, err := os.Stat("/dev/kvm")
	return err == nil
}
