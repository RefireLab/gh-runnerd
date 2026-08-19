package bake

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestInstallScriptContent(t *testing.T) {
	s := installScript("2.336.0", "x64", false)
	for _, want := range []string{
		"actions-runner-linux-x64-2.336.0.tar.gz",
		"get.docker.com",
		"gh-runnerd-guest.service",
		"touch \"$SEED/BAKE_OK\"",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("install script missing %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "runnerfile-runs.sh") {
		t.Fatal("no extra runs expected")
	}
	s = installScript("2.336.0", "arm64", true)
	if !strings.Contains(s, "actions-runner-linux-arm64-2.336.0.tar.gz") {
		t.Fatal("arm64 runner url missing")
	}
	if !strings.Contains(s, "runnerfile-runs.sh") {
		t.Fatal("extra runs hook missing")
	}
}

func TestUserDataMarksFailure(t *testing.T) {
	ud := userData()
	for _, want := range []string{"#cloud-config", "BAKE_FAIL", "power_state", "mount -t 9p"} {
		if !strings.Contains(ud, want) {
			t.Fatalf("user-data missing %q", want)
		}
	}
}

func TestSeedAssetsEmbedded(t *testing.T) {
	for src := range seedAssets {
		raw, err := assetsFS.ReadFile(src)
		if err != nil {
			t.Fatalf("embed %s: %v", src, err)
		}
		if len(raw) == 0 {
			t.Fatalf("embed %s is empty", src)
		}
	}
}

func TestWriteSeed(t *testing.T) {
	dir := t.TempDir()
	guest := filepath.Join(dir, "guest-bin")
	if err := os.WriteFile(guest, []byte("fake"), 0o755); err != nil {
		t.Fatal(err)
	}
	seed := filepath.Join(dir, "seed")
	o := withDefaults(Options{CACertPEM: []byte("PEM"), ExtraRuns: []string{"echo hi"}})
	if err := writeSeed(seed, guest, o); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{
		"gh-runnerd-guest", "gh-runnerd-guest.service", "hosts", "daemon.json",
		"hosts.toml.docker", "hosts.toml.ghcr", "hosts.toml.quay",
		"gh-runnerd-ca.crt", "install.sh", "runnerfile-runs.sh",
	} {
		if _, err := os.Stat(filepath.Join(seed, f)); err != nil {
			t.Fatalf("seed missing %s: %v", f, err)
		}
	}
}

func TestCheckGuestArchAgainstSelf(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("ELF check is linux-only")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Skip("no executable path")
	}
	if err := CheckGuestArch(exe, runtime.GOARCH); err != nil {
		t.Fatalf("self arch: %v", err)
	}
	other := "arm64"
	if runtime.GOARCH == "arm64" {
		other = "amd64"
	}
	if err := CheckGuestArch(exe, other); err == nil {
		t.Fatal("mismatched arch must fail")
	}
}

func TestBakeToolsAndPackages(t *testing.T) {
	tools := BakeTools("amd64")
	pkgs := AptPackages(tools)
	joined := strings.Join(pkgs, " ")
	for _, want := range []string{"qemu-system-x86", "qemu-utils", "cloud-image-utils"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("packages missing %s: %v", want, pkgs)
		}
	}
	if got := BakeTools("arm64")[0].Bin; got != "qemu-system-aarch64" {
		t.Fatalf("arm64 qemu: %s", got)
	}
}

func TestCheckBakeResult(t *testing.T) {
	dir := t.TempDir()
	if err := checkBakeResult(dir, "console.log"); err == nil {
		t.Fatal("missing BAKE_OK must fail")
	}
	if err := os.WriteFile(filepath.Join(dir, "BAKE_FAIL"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := checkBakeResult(dir, "console.log"); err == nil {
		t.Fatal("BAKE_FAIL must fail")
	}
	if err := os.Remove(filepath.Join(dir, "BAKE_FAIL")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "BAKE_OK"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := checkBakeResult(dir, "console.log"); err != nil {
		t.Fatalf("BAKE_OK: %v", err)
	}
}
