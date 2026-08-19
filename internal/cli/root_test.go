package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRootHelp(t *testing.T) {
	cmd := Root()
	cmd.SetArgs([]string{"--help"})
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, s := range []string{"init", "doctor", "serve", "image", "runner-image"} {
		if !strings.Contains(out, s) {
			t.Fatalf("help missing %s:\n%s", s, out)
		}
	}
}

func TestInitCreatesLayoutAndCA(t *testing.T) {
	dir := t.TempDir()
	cmd := Root()
	cfg := filepath.Join(dir, "config.toml")
	cmd.SetArgs([]string{
		"init",
		"--data-dir", filepath.Join(dir, "data"),
		"--config", cfg,
		"--owner", "acme",
		"--repo", "app",
		"--token", "ghs_test",
	})
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "data", "state", "ca", "ca.crt")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "data", "imports")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "Initialized") {
		t.Fatalf("%s", buf.String())
	}
}

func TestImageHelp(t *testing.T) {
	cmd := Root()
	cmd.SetArgs([]string{"image", "--help"})
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "import") {
		t.Fatalf("%s", buf.String())
	}
}
