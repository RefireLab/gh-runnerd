package images

import (
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
