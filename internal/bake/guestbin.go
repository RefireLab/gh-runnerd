package bake

import (
	"debug/elf"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// LocateGuest finds the gh-runnerd-guest binary that gets installed inside
// the golden VM. It ships in the same release archive as gh-runnerd.
func LocateGuest(explicit string) (string, error) {
	if explicit != "" {
		if _, err := os.Stat(explicit); err != nil {
			return "", fmt.Errorf("guest binary %s: %w", explicit, err)
		}
		return explicit, nil
	}
	var candidates []string
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), "gh-runnerd-guest"))
	}
	candidates = append(candidates,
		"gh-runnerd-guest",
		filepath.Join("bin", "gh-runnerd-guest"),
		"/usr/local/bin/gh-runnerd-guest",
		"/usr/bin/gh-runnerd-guest",
	)
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			abs, err := filepath.Abs(c)
			if err != nil {
				return c, nil
			}
			return abs, nil
		}
	}
	if p, err := exec.LookPath("gh-runnerd-guest"); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("gh-runnerd-guest not found next to gh-runnerd — it ships in the same release archive; keep both files together (or pass --guest /path/to/gh-runnerd-guest)")
}

// CheckGuestArch verifies the guest ELF matches the VM architecture so an
// amd64 agent is never baked into an arm64 image.
func CheckGuestArch(path, arch string) error {
	f, err := elf.Open(path)
	if err != nil {
		return fmt.Errorf("%s is not a Linux executable: %w", path, err)
	}
	defer f.Close()
	want := map[string]elf.Machine{
		"amd64": elf.EM_X86_64,
		"arm64": elf.EM_AARCH64,
	}[arch]
	if want == elf.Machine(0) {
		return fmt.Errorf("unsupported architecture %q", arch)
	}
	if f.Machine != want {
		return fmt.Errorf("%s is built for %s but the runner image is %s — download the %s release archive", path, f.Machine, arch, arch)
	}
	return nil
}
