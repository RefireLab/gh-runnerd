package images

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestCatalogImportActivateValidate(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	src := filepath.Join(dir, "src.qcow2")
	payload := make([]byte, 2048)
	copy(payload, []byte("qcow2-fake-header"))
	if err := os.WriteFile(src, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	cat := Catalog{Dir: filepath.Join(dir, "runner")}
	img, err := cat.Import(src, "company")
	if err != nil {
		t.Fatal(err)
	}
	if img.SHA256 == "" {
		t.Fatal("missing checksum")
	}
	if err := cat.Activate("company"); err != nil {
		t.Fatal(err)
	}
	active, err := cat.Active()
	if err != nil {
		t.Fatal(err)
	}
	if active.Name != "company" {
		t.Fatalf("active %s", active.Name)
	}
	if err := cat.Validate("company"); err != nil {
		t.Fatal(err)
	}
	// Corrupt checksum
	m, err := cat.load()
	if err != nil {
		t.Fatal(err)
	}
	m.Images[0].SHA256 = "deadbeef"
	if err := cat.save(m); err != nil {
		t.Fatal(err)
	}
	if err := cat.Validate("company"); err == nil {
		t.Fatal("expected checksum error")
	}
}

// Import must replace an existing image atomically: running VMs keep the
// old file open as their overlays' backing image, so the old inode must
// survive the replace (no in-place truncation) and no temp file may leak.
func TestImportReplacesImageAtomically(t *testing.T) {
	dir := t.TempDir()
	cat := Catalog{Dir: filepath.Join(dir, "runner")}

	oldSrc := filepath.Join(dir, "old.qcow2")
	if err := os.WriteFile(oldSrc, []byte("old-image-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := cat.Import(oldSrc, "ubuntu-test"); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(cat.Dir, "ubuntu-test.qcow2")
	held, err := os.Open(dst) // a running QEMU holding the backing file
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()

	newSrc := filepath.Join(dir, "new.qcow2")
	if err := os.WriteFile(newSrc, []byte("NEW-image-bytes!"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := cat.Import(newSrc, "ubuntu-test"); err != nil {
		t.Fatal(err)
	}

	got, err := io.ReadAll(held)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "old-image-bytes" {
		t.Fatalf("open handle must keep the pre-replace inode intact, read %q", got)
	}
	onDisk, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(onDisk) != "NEW-image-bytes!" {
		t.Fatalf("path must serve the new image, read %q", onDisk)
	}
	if _, err := os.Stat(dst + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("temp file must not leak: %v", err)
	}
}
