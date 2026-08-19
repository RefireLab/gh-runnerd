package ansi

import (
	"os"
	"strings"
	"testing"
)

func TestWrappers(t *testing.T) {
	t.Parallel()
	if got := Red("boom"); !strings.Contains(got, "boom") || !strings.HasSuffix(got, reset) {
		t.Fatalf("%q", got)
	}
	if got := Green("ok"); !strings.HasPrefix(got, green) {
		t.Fatalf("%q", got)
	}
	if got := Yellow("warn"); !strings.HasPrefix(got, yellow) {
		t.Fatalf("%q", got)
	}
}

func TestEnabledPipeIsFalse(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	if Enabled(w) {
		t.Fatal("a pipe must not enable colors")
	}
	if Enabled(nil) {
		t.Fatal("nil must not enable colors")
	}
}

func TestNoColorEnvWins(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	if Enabled(os.Stdout) {
		t.Fatal("NO_COLOR must disable colors")
	}
}
