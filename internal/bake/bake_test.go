package bake

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestInstallScriptContent(t *testing.T) {
	s := installScript("2.336.0", "x64", false, false)
	for _, want := range []string{
		"actions-runner-linux-x64-2.336.0.tar.gz",
		"get.docker.com",
		"gh-runnerd-guest.service",
		"touch \"$SEED/BAKE_OK\"",
		// Runtime NICs sit in a different PCI slot with a different MAC
		// than during bake, so the image must DHCP on any en* interface.
		"rm -f /etc/netplan/50-cloud-init.yaml",
		"/etc/netplan/50-gh-runnerd.yaml",
		"match:\n        name: \"en*\"",
		"dhcp4: true",
		"optional: true",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("install script missing %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "runnerfile-runs.sh") {
		t.Fatal("no extra runs expected")
	}
	if strings.Contains(s, "hosted-setup.sh") {
		t.Fatal("no hosted setup expected")
	}
	s = installScript("2.336.0", "arm64", true, true)
	if !strings.Contains(s, "actions-runner-linux-arm64-2.336.0.tar.gz") {
		t.Fatal("arm64 runner url missing")
	}
	if !strings.Contains(s, "runnerfile-runs.sh") {
		t.Fatal("extra runs hook missing")
	}
	// The hosted setup must run before the core install so the core owns
	// the final Docker config and runner directory.
	hosted := strings.Index(s, "hosted-setup.sh")
	core := strings.Index(s, "get.docker.com")
	if hosted < 0 || core < 0 || hosted > core {
		t.Fatalf("hosted setup must run before the core install (hosted=%d core=%d)", hosted, core)
	}
}

func TestWriteSeedHosted(t *testing.T) {
	dir := t.TempDir()
	guest := filepath.Join(dir, "guest-bin")
	if err := os.WriteFile(guest, []byte("fake"), 0o755); err != nil {
		t.Fatal(err)
	}
	tree := filepath.Join(dir, "tree")
	if err := os.MkdirAll(filepath.Join(tree, "images", "ubuntu", "scripts", "build"), 0o755); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(tree, "images", "ubuntu", "scripts", "build", "install-git.sh")
	if err := os.WriteFile(script, []byte("#!/bin/bash -e\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	seed := filepath.Join(dir, "seed")
	o := withDefaults(Options{CACertPEM: []byte("PEM"), HostedTree: tree, HostedSetup: "#!/bin/bash\nexit 0\n"})
	if err := writeSeed(seed, guest, o); err != nil {
		t.Fatal(err)
	}
	copied := filepath.Join(seed, "runner-images", "images", "ubuntu", "scripts", "build", "install-git.sh")
	st, err := os.Stat(copied)
	if err != nil {
		t.Fatalf("hosted tree not copied: %v", err)
	}
	if st.Mode()&0o111 == 0 {
		t.Fatal("executable bit lost while copying the hosted tree")
	}
	if _, err := os.Stat(filepath.Join(seed, "hosted-setup.sh")); err != nil {
		t.Fatalf("hosted-setup.sh missing: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(seed, "install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "hosted-setup.sh") {
		t.Fatal("install.sh does not invoke the hosted setup")
	}
}

func TestCloudImageNameByVersion(t *testing.T) {
	o := withDefaults(Options{})
	if o.UbuntuVersion != "24.04" {
		t.Fatalf("default ubuntu version = %q", o.UbuntuVersion)
	}
	if o.Name != "ubuntu-24.04-"+runtime.GOARCH {
		t.Fatalf("default name = %q", o.Name)
	}
	o = withDefaults(Options{UbuntuVersion: "26.04"})
	if o.Name != "ubuntu-26.04-"+runtime.GOARCH {
		t.Fatalf("26.04 name = %q", o.Name)
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
