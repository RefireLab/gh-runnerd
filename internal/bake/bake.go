// Package bake builds the golden Ubuntu 24.04 runner qcow2 entirely from
// the gh-runnerd binary: download the Ubuntu cloud image, boot it once
// under QEMU/KVM with a cloud-init seed that installs Docker, the official
// GitHub Actions runner, and the gh-runnerd guest agent, then compress the
// result. This replaces the old images/runner/bake.sh script.
package bake

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Options control one bake run. Zero values fall back to defaults.
type Options struct {
	Arch          string // amd64 or arm64; must match the host (KVM)
	Name          string // template name, default ubuntu-24.04-<arch>
	UbuntuVersion string // Ubuntu release of the base cloud image, default 24.04
	RunnerVersion string // GitHub Actions runner release
	GuestBinary   string // explicit path to gh-runnerd-guest
	CACertPEM     []byte // host CA baked into the VM trust store
	HostIP        string // bridge address VMs dial (default 10.87.0.1)
	OutPath       string // destination compressed qcow2 (required)
	CacheDir      string // where downloaded cloud images are kept
	WorkDir       string // scratch dir; default: os.MkdirTemp
	DiskGB        int    // default 40
	MemoryMB      int    // default 4096
	CPUs          int    // default 2
	MinFreeGB     int    // host free-space preflight; 0 skips the check
	ExtraRuns     []string
	// HostedTree/HostedSetup provision GitHub hosted-runner software from
	// an extracted actions/runner-images checkout: the tree is shared with
	// the VM and the setup script runs before the core install.
	HostedTree  string
	HostedSetup string
	Fresh       bool // re-download the Ubuntu cloud image
	Verbose     bool // stream the VM console to Out
	Timeout     time.Duration
	Out         io.Writer
}

// Tool is a host binary bake or serve depends on, with its apt package.
type Tool struct {
	Bin string
	Apt string
}

// BakeTools lists host binaries required to bake a runner image.
func BakeTools(arch string) []Tool {
	tools := []Tool{
		{Bin: "qemu-img", Apt: "qemu-utils"},
		{Bin: "cloud-localds", Apt: "cloud-image-utils"},
	}
	if arch == "arm64" {
		tools = append([]Tool{{Bin: "qemu-system-aarch64", Apt: "qemu-system-arm"}}, tools...)
	} else {
		tools = append([]Tool{{Bin: "qemu-system-x86_64", Apt: "qemu-system-x86"}}, tools...)
	}
	return tools
}

// ServeTools lists host binaries the daemon needs at runtime.
func ServeTools(arch string) []Tool {
	tools := BakeTools(arch)
	return append(tools, Tool{Bin: "iptables", Apt: "iptables"})
}

// MissingTools filters tools that are not in PATH.
func MissingTools(tools []Tool) []Tool {
	var missing []Tool
	for _, t := range tools {
		if _, err := exec.LookPath(t.Bin); err != nil {
			missing = append(missing, t)
		}
	}
	return missing
}

// AptPackages returns the deduplicated apt package list for tools.
func AptPackages(tools []Tool) []string {
	seen := map[string]bool{}
	var pkgs []string
	for _, t := range tools {
		if !seen[t.Apt] {
			seen[t.Apt] = true
			pkgs = append(pkgs, t.Apt)
		}
	}
	return pkgs
}

func (o Options) say(format string, args ...any) {
	if o.Out != nil {
		fmt.Fprintf(o.Out, format+"\n", args...)
	}
}

func withDefaults(o Options) Options {
	if o.Arch == "" {
		o.Arch = runtime.GOARCH
	}
	if o.UbuntuVersion == "" {
		o.UbuntuVersion = "24.04"
	}
	if o.Name == "" {
		o.Name = "ubuntu-" + o.UbuntuVersion + "-" + o.Arch
	}
	if o.RunnerVersion == "" {
		o.RunnerVersion = DefaultRunnerVersion
	}
	if o.DiskGB <= 0 {
		o.DiskGB = 40
	}
	if o.MemoryMB <= 0 {
		o.MemoryMB = 4096
	}
	if o.CPUs <= 0 {
		o.CPUs = 2
	}
	if o.Timeout <= 0 {
		o.Timeout = 45 * time.Minute
	}
	if o.CacheDir == "" {
		o.CacheDir = filepath.Join(os.TempDir(), "gh-runnerd-bake-cache")
	}
	if o.HostIP == "" {
		o.HostIP = "10.87.0.1"
	}
	return o
}

// CheckKVM returns a friendly error when /dev/kvm is missing or not
// accessible to this process.
func CheckKVM() error {
	if _, err := os.Stat("/dev/kvm"); err != nil {
		return fmt.Errorf("/dev/kvm is missing — this machine has no KVM virtualization (enable VT-x/AMD-V in BIOS, or on a VPS pick a plan with nested virtualization)")
	}
	f, err := os.OpenFile("/dev/kvm", os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("no permission to use /dev/kvm — run with sudo")
	}
	f.Close()
	return nil
}

// Run bakes the golden runner image into o.OutPath.
func Run(ctx context.Context, o Options) error {
	o = withDefaults(o)
	if o.OutPath == "" {
		return fmt.Errorf("bake: OutPath is required")
	}
	if o.Arch != "amd64" && o.Arch != "arm64" {
		return fmt.Errorf("unsupported architecture %q (amd64 or arm64)", o.Arch)
	}
	if o.Arch != runtime.GOARCH {
		return fmt.Errorf("cannot bake a %s image on a %s host: baking needs KVM for the same architecture", o.Arch, runtime.GOARCH)
	}
	if accel() == "kvm" {
		if err := CheckKVM(); err != nil {
			return err
		}
	}
	if missing := MissingTools(BakeTools(o.Arch)); len(missing) > 0 {
		var bins []string
		for _, t := range missing {
			bins = append(bins, t.Bin)
		}
		return fmt.Errorf("missing tools: %s — install them with:\n  sudo apt-get install -y %s",
			strings.Join(bins, ", "), strings.Join(AptPackages(missing), " "))
	}

	guest, err := LocateGuest(o.GuestBinary)
	if err != nil {
		return err
	}
	if err := CheckGuestArch(guest, o.Arch); err != nil {
		return err
	}

	work := o.WorkDir
	if work == "" {
		work, err = os.MkdirTemp("", "gh-runnerd-bake-*")
		if err != nil {
			return err
		}
		defer os.RemoveAll(work)
	} else if err := os.MkdirAll(work, 0o755); err != nil {
		return err
	}

	if err := checkFreeSpace(o, work); err != nil {
		return err
	}

	cloudImg, err := fetchCloudImage(ctx, o)
	if err != nil {
		return err
	}

	base := filepath.Join(work, "base.qcow2")
	o.say(">> preparing %dG base disk", o.DiskGB)
	if err := runCmd(ctx, "qemu-img", "convert", "-O", "qcow2", cloudImg, base); err != nil {
		return err
	}
	if err := runCmd(ctx, "qemu-img", "resize", base, fmt.Sprintf("%dG", o.DiskGB)); err != nil {
		return err
	}

	seedDir := filepath.Join(work, "seed")
	if err := writeSeed(seedDir, guest, o); err != nil {
		return err
	}
	seedISO := filepath.Join(work, "seed.iso")
	if err := os.WriteFile(filepath.Join(work, "user-data"), []byte(userData()), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(work, "meta-data"), []byte(metaData()), 0o644); err != nil {
		return err
	}
	if err := runCmd(ctx, "cloud-localds", seedISO,
		filepath.Join(work, "user-data"), filepath.Join(work, "meta-data")); err != nil {
		return err
	}

	consoleLog := filepath.Join(work, "console.log")
	if err := bootBakeVM(ctx, o, base, seedISO, seedDir, consoleLog); err != nil {
		return err
	}
	if err := checkBakeResult(seedDir, consoleLog); err != nil {
		return err
	}
	reportHostedFailures(o, seedDir)

	o.say(">> compressing image (this shrinks it a lot)")
	if err := os.MkdirAll(filepath.Dir(o.OutPath), 0o755); err != nil {
		return err
	}
	if err := runCmd(ctx, "qemu-img", "convert", "-c", "-O", "qcow2", base, o.OutPath); err != nil {
		return err
	}
	o.say(">> baked %s", o.OutPath)
	return nil
}

// reportHostedFailures surfaces non-critical upstream script failures
// recorded by the hosted setup script. The bake still succeeded; the
// operator decides whether the missing tools matter.
func reportHostedFailures(o Options, seedDir string) {
	raw, err := os.ReadFile(filepath.Join(seedDir, "PARITY_FAILED"))
	if err != nil {
		return
	}
	failed := strings.TrimPrefix(strings.TrimSpace(string(raw)), "failed:")
	if failed == "" {
		return
	}
	o.say("!! some hosted-software scripts failed (image still built): %s", failed)
	o.say("   details: /imagegeneration/logs/*.log inside the image; rebuild with")
	o.say("   --skip-scripts %s to silence, or retry later", strings.Join(strings.Fields(failed), ","))
}

// checkFreeSpace fails early when the work or output filesystem clearly
// cannot hold the image about to be built. GH_RUNNERD_SKIP_DISK_CHECK=1
// bypasses it.
func checkFreeSpace(o Options, work string) error {
	if o.MinFreeGB <= 0 || os.Getenv("GH_RUNNERD_SKIP_DISK_CHECK") == "1" {
		return nil
	}
	for _, dir := range []string{work, filepath.Dir(o.OutPath)} {
		free, err := freeGB(dir)
		if err != nil {
			continue
		}
		if free < o.MinFreeGB {
			return fmt.Errorf("not enough disk for this bake: %s has %d GB free, need about %d GB — free space or bake a smaller flavor (GH_RUNNERD_SKIP_DISK_CHECK=1 overrides)",
				dir, free, o.MinFreeGB)
		}
	}
	return nil
}

func writeSeed(seedDir, guest string, o Options) error {
	if err := os.MkdirAll(seedDir, 0o755); err != nil {
		return err
	}
	guestRaw, err := os.ReadFile(guest)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(seedDir, "gh-runnerd-guest"), guestRaw, 0o755); err != nil {
		return err
	}
	for src, dst := range seedAssets {
		raw, err := assetsFS.ReadFile(src)
		if err != nil {
			return fmt.Errorf("embedded asset %s: %w", src, err)
		}
		// The guest /etc/hosts entry and the agent's GH_RUNNERD_HOST must
		// point at the configured bridge address, not the default.
		raw = []byte(strings.ReplaceAll(string(raw), "10.87.0.1", o.HostIP))
		if err := os.WriteFile(filepath.Join(seedDir, dst), raw, 0o644); err != nil {
			return err
		}
	}
	if err := os.WriteFile(filepath.Join(seedDir, "gh-runnerd-ca.crt"), o.CACertPEM, 0o644); err != nil {
		return err
	}
	runnerArch := "x64"
	if o.Arch == "arm64" {
		runnerArch = "arm64"
	}
	script := installScript(o.RunnerVersion, runnerArch, len(o.ExtraRuns) > 0, o.HostedSetup != "")
	if err := os.WriteFile(filepath.Join(seedDir, "install.sh"), []byte(script), 0o755); err != nil {
		return err
	}
	if len(o.ExtraRuns) > 0 {
		if err := os.WriteFile(filepath.Join(seedDir, "runnerfile-runs.sh"), []byte(runsScript(o.ExtraRuns)), 0o755); err != nil {
			return err
		}
	}
	if o.HostedSetup != "" {
		if o.HostedTree == "" {
			return fmt.Errorf("bake: HostedSetup requires HostedTree")
		}
		if err := copyTree(o.HostedTree, filepath.Join(seedDir, "runner-images")); err != nil {
			return fmt.Errorf("copy runner-images tree into seed: %w", err)
		}
		if err := os.WriteFile(filepath.Join(seedDir, "hosted-setup.sh"), []byte(o.HostedSetup), 0o755); err != nil {
			return err
		}
	}
	return nil
}

// copyTree copies a directory of regular files (the runner-images
// checkout) preserving the executable bit. Symlinks and specials are
// skipped — the upstream repo has none we need.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		mode := os.FileMode(0o644)
		if info.Mode()&0o111 != 0 {
			mode = 0o755
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, raw, mode)
	})
}

// accel returns the QEMU accelerator. GH_RUNNERD_BAKE_ACCEL=tcg is an
// escape hatch for CI smoke tests on hosts without working KVM; real
// deployments always use KVM.
func accel() string {
	if v := os.Getenv("GH_RUNNERD_BAKE_ACCEL"); v != "" {
		return v
	}
	return "kvm"
}

func bootBakeVM(ctx context.Context, o Options, base, seedISO, seedDir, consoleLog string) error {
	acc := accel()
	cpu := "host"
	if acc != "kvm" {
		cpu = "max"
	}
	bin := "qemu-system-x86_64"
	machine := "q35,accel=" + acc
	var extra []string
	if o.Arch == "arm64" {
		bin = "qemu-system-aarch64"
		machine = "virt,accel=" + acc
		fw, err := Arm64Firmware()
		if err != nil {
			return err
		}
		extra = append(extra, "-bios", fw)
	}
	args := []string{
		"-name", "gh-runnerd-bake",
		"-machine", machine,
		"-cpu", cpu,
		"-smp", strconv.Itoa(o.CPUs),
		"-m", strconv.Itoa(o.MemoryMB),
		"-nographic",
		"-device", "virtio-rng-pci",
		"-drive", "if=virtio,format=qcow2,file=" + base,
		"-drive", "if=virtio,format=raw,file=" + seedISO,
		"-netdev", "user,id=net0",
		"-device", "virtio-net-pci,netdev=net0",
		"-fsdev", "local,id=seed,path=" + seedDir + ",security_model=mapped",
		"-device", "virtio-9p-pci,fsdev=seed,mount_tag=seed",
	}
	args = append(args, extra...)

	logf, err := os.Create(consoleLog)
	if err != nil {
		return err
	}
	defer logf.Close()
	var sink io.Writer = logf
	if o.Verbose && o.Out != nil {
		sink = io.MultiWriter(logf, o.Out)
	}

	runCtx, cancel := context.WithTimeout(ctx, o.Timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, bin, args...)
	cmd.Stdout = sink
	cmd.Stderr = sink

	if o.HostedSetup != "" {
		o.say(">> building the VM image with GitHub hosted-runner software — this can take a long time")
	} else {
		o.say(">> building the VM image — usually 10-20 minutes, needs internet")
	}
	if !o.Verbose {
		o.say("   (progress log: %s)", consoleLog)
	}
	start := time.Now()
	done := make(chan struct{})
	if !o.Verbose {
		go func() {
			t := time.NewTicker(time.Minute)
			defer t.Stop()
			warned := false
			for {
				select {
				case <-done:
					return
				case <-t.C:
					msg := fmt.Sprintf("   still building... (%s elapsed)", time.Since(start).Round(time.Second))
					if step := lastLine(filepath.Join(seedDir, "PARITY_PROGRESS")); step != "" {
						msg += " — " + step
					}
					o.say("%s", msg)
					// A healthy guest prints boot output within seconds. A
					// silent console after minutes means the vCPU never ran:
					// classic broken nested KVM (/dev/kvm exists, guests
					// never execute).
					if !warned && acc == "kvm" && time.Since(start) > 3*time.Minute {
						if st, err := os.Stat(consoleLog); err == nil && st.Size() == 0 {
							warned = true
							o.say("!! the VM has produced no console output — KVM may not actually work on this machine (broken nested virtualization?)")
							o.say("   if this ends in a timeout, retry with software emulation: GH_RUNNERD_BAKE_ACCEL=tcg (much slower)")
						}
					}
				}
			}
		}()
	}
	runErr := cmd.Run()
	close(done)
	if runCtx.Err() == context.DeadlineExceeded {
		if st, err := os.Stat(consoleLog); err == nil && st.Size() == 0 && acc == "kvm" {
			return fmt.Errorf("bake timed out after %s and the VM never produced any console output — KVM is present but guests do not run (broken nested virtualization?); retry with GH_RUNNERD_BAKE_ACCEL=tcg (slow) or bake on a host with working KVM", o.Timeout)
		}
		return fmt.Errorf("bake timed out after %s — check your internet connection; console log: %s", o.Timeout, consoleLog)
	}
	if runErr != nil {
		return fmt.Errorf("qemu failed: %w — console log: %s", runErr, consoleLog)
	}
	o.say(">> VM finished in %s", time.Since(start).Round(time.Second))
	return nil
}

func checkBakeResult(seedDir, consoleLog string) error {
	if _, err := os.Stat(filepath.Join(seedDir, "BAKE_FAIL")); err == nil {
		return fmt.Errorf("install failed inside the VM:\n%s", tailFile(filepath.Join(seedDir, "install.log"), 2000))
	}
	if _, err := os.Stat(filepath.Join(seedDir, "BAKE_OK")); err != nil {
		return fmt.Errorf("the VM never completed the install (cloud-init did not run) — console log: %s", consoleLog)
	}
	return nil
}

// Arm64Firmware returns the UEFI firmware path required to boot arm64 VMs.
func Arm64Firmware() (string, error) {
	for _, p := range []string{
		"/usr/share/AAVMF/AAVMF_CODE.fd",
		"/usr/share/qemu-efi-aarch64/QEMU_EFI.fd",
	} {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("arm64 UEFI firmware not found — install it with: sudo apt-get install -y qemu-efi-aarch64")
}

func fetchCloudImage(ctx context.Context, o Options) (string, error) {
	name := fmt.Sprintf("ubuntu-%s-server-cloudimg-%s.img", o.UbuntuVersion, o.Arch)
	dest := filepath.Join(o.CacheDir, name)
	if !o.Fresh {
		if st, err := os.Stat(dest); err == nil && st.Size() > 100<<20 {
			o.say(">> using cached Ubuntu cloud image %s", dest)
			return dest, nil
		}
	}
	if err := os.MkdirAll(o.CacheDir, 0o755); err != nil {
		return "", err
	}
	url := "https://cloud-images.ubuntu.com/releases/" + o.UbuntuVersion + "/release/" + name
	o.say(">> downloading Ubuntu %s cloud image (~600 MB)", o.UbuntuVersion)
	tmp := dest + ".part"
	out, err := os.Create(tmp)
	if err != nil {
		return "", err
	}
	defer out.Close()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("download %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download %s: %s", url, resp.Status)
	}
	pw := &progressWriter{w: out, total: resp.ContentLength, say: o.say}
	if _, err := io.Copy(pw, resp.Body); err != nil {
		return "", fmt.Errorf("download %s: %w", url, err)
	}
	if err := out.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, dest); err != nil {
		return "", err
	}
	o.say("   download complete")
	return dest, nil
}

type progressWriter struct {
	w       io.Writer
	total   int64
	written int64
	lastPct int
	say     func(string, ...any)
}

func (p *progressWriter) Write(b []byte) (int, error) {
	n, err := p.w.Write(b)
	p.written += int64(n)
	if p.total > 0 {
		pct := int(p.written * 100 / p.total)
		if pct >= p.lastPct+10 {
			p.lastPct = pct - pct%10
			p.say("   %d%% (%d/%d MB)", p.lastPct, p.written>>20, p.total>>20)
		}
	}
	return n, err
}

func runCmd(ctx context.Context, bin string, args ...string) error {
	out, err := exec.CommandContext(ctx, bin, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %s (%w)", bin, strings.Join(args, " "), strings.TrimSpace(string(out)), err)
	}
	return nil
}

// lastLine returns the last non-empty line of a small progress file.
func lastLine(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) == 0 {
		return ""
	}
	return strings.TrimSpace(lines[len(lines)-1])
}

func tailFile(path string, max int64) string {
	f, err := os.Open(path)
	if err != nil {
		return "(no install log)"
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "(no install log)"
	}
	if st.Size() > max {
		if _, err := f.Seek(-max, io.SeekEnd); err != nil {
			return "(no install log)"
		}
	}
	raw, err := io.ReadAll(f)
	if err != nil {
		return "(no install log)"
	}
	return string(raw)
}
