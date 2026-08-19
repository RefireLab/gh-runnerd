package layout

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureCreatesLayout(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	d := New(filepath.Join(root, "gh-runnerd-data"))
	if err := d.Ensure(); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{d.Runner, d.Containers, d.Imports, d.CacheOCI, d.CA} {
		st, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("stat %s: %v", dir, err)
		}
		if !st.IsDir() {
			t.Fatalf("%s is not a directory", dir)
		}
	}
}
