// Package sysinstall installs gh-runnerd as a system service: binaries in
// /usr/local/bin and a systemd unit that starts on boot.
package sysinstall

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// UnitPath is where the wizard writes the systemd service.
const UnitPath = "/etc/systemd/system/gh-runnerd.service"

// BinDir is where the wizard installs the binaries.
const BinDir = "/usr/local/bin"

// UnitContent renders the systemd unit for the given binary and config.
func UnitContent(execPath, configPath string) string {
	return fmt.Sprintf(`[Unit]
Description=gh-runnerd ephemeral GitHub Actions runners
Documentation=https://github.com/RefireLab/gh-runnerd
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=%s serve --config %s
Restart=on-failure
RestartSec=5
User=root
LimitNOFILE=1048576
DeviceAllow=/dev/kvm rw
DeviceAllow=/dev/vhost-vsock rw
DeviceAllow=/dev/net/tun rw
AmbientCapabilities=CAP_NET_ADMIN CAP_NET_RAW
NoNewPrivileges=false

[Install]
WantedBy=multi-user.target
`, execPath, configPath)
}

// InstallBinaries copies the running gh-runnerd executable and the guest
// agent into destDir. Paths already inside destDir are left as-is.
func InstallBinaries(guestSrc, destDir string) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return "", err
	}
	mainDst := filepath.Join(destDir, "gh-runnerd")
	if err := copyExecutable(exe, mainDst); err != nil {
		return "", err
	}
	if guestSrc != "" {
		if err := copyExecutable(guestSrc, filepath.Join(destDir, "gh-runnerd-guest")); err != nil {
			return "", err
		}
	}
	return mainDst, nil
}

func copyExecutable(src, dst string) error {
	srcAbs, _ := filepath.Abs(src)
	dstAbs, _ := filepath.Abs(dst)
	if srcAbs == dstAbs {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}

// InstallUnit writes the systemd unit and reloads systemd.
func InstallUnit(execPath, configPath string) error {
	if err := os.WriteFile(UnitPath, []byte(UnitContent(execPath, configPath)), 0o644); err != nil {
		return err
	}
	return systemctl("daemon-reload")
}

// EnableNow enables the service at boot and starts it immediately.
func EnableNow() error {
	return systemctl("enable", "--now", "gh-runnerd")
}

func systemctl(args ...string) error {
	out, err := exec.Command("systemctl", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl %s: %s (%w)", strings.Join(args, " "), strings.TrimSpace(string(out)), err)
	}
	return nil
}

// HaveSystemd reports whether systemctl is usable on this host.
func HaveSystemd() bool {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return false
	}
	return exec.Command("systemctl", "is-system-running", "--quiet").Run() == nil ||
		exec.Command("systemctl", "list-units", "--no-pager", "--no-legend", "--type=service", "systemd-journald.service").Run() == nil
}
