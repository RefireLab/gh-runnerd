package sysinstall

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUnitContent(t *testing.T) {
	u := UnitContent("/usr/local/bin/gh-runnerd", "/etc/gh-runnerd/config.toml")
	for _, want := range []string{
		"ExecStart=/usr/local/bin/gh-runnerd serve --config /etc/gh-runnerd/config.toml",
		"WantedBy=multi-user.target",
		"DeviceAllow=/dev/kvm rw",
	} {
		if !strings.Contains(u, want) {
			t.Fatalf("unit missing %q:\n%s", want, u)
		}
	}
}

func TestCopyExecutable(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	if err := os.WriteFile(src, []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "sub", "dst")
	if err := copyExecutable(src, dst); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm()&0o111 == 0 {
		t.Fatal("copied binary is not executable")
	}
	if err := copyExecutable(src, src); err != nil {
		t.Fatalf("self-copy must be a no-op: %v", err)
	}
}
