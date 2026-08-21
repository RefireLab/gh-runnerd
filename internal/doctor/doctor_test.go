package doctor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/RefireLab/gh-runnerd/internal/config"
	"github.com/RefireLab/gh-runnerd/internal/host"
	"github.com/RefireLab/gh-runnerd/internal/images"
)

func TestReportHasErrors(t *testing.T) {
	t.Parallel()
	r := Report{Checks: []Check{{Name: "x", Severity: Error, Message: "no"}}}
	if !r.HasErrors() {
		t.Fatal("expected errors")
	}
	if !strings.Contains(r.String(), "ERROR") {
		t.Fatalf("%s", r.String())
	}
}

func TestUbuntuGateUsedByDoctor(t *testing.T) {
	t.Parallel()
	if err := host.CheckUbuntu(host.Info{ID: "alpine", VersionID: "3.22"}); err == nil {
		t.Fatal("alpine must not pass")
	}
}

func TestDoctorRunsAgainstDefaults(t *testing.T) {
	cfg := config.Defaults()
	cfg.DataDir = t.TempDir()
	r := Run(cfg)
	if len(r.Checks) == 0 {
		t.Fatal("no checks")
	}
}

func findCheck(t *testing.T, r Report, name string) Check {
	t.Helper()
	for _, c := range r.Checks {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("check %q missing in %s", name, r.String())
	return Check{}
}

func importImage(t *testing.T, cfg config.Config, name string) images.RunnerImage {
	t.Helper()
	src := filepath.Join(t.TempDir(), "src.qcow2")
	if err := os.WriteFile(src, []byte("stub-disk"), 0o600); err != nil {
		t.Fatal(err)
	}
	img, err := images.Catalog{Dir: cfg.Layout().Runner}.Import(src, name)
	if err != nil {
		t.Fatal(err)
	}
	return img
}

func TestRunnerImageCheckFindsConfiguredFamily(t *testing.T) {
	cfg := config.Defaults()
	cfg.DataDir = t.TempDir()
	cfg.VM.Template = "ubuntu-26.04"
	img := importImage(t, cfg, "ubuntu-26.04-"+runtime.GOARCH)
	c := findCheck(t, Run(cfg), "runner-image")
	if c.Severity != OK || c.Message != img.Path {
		t.Fatalf("family template must resolve to the arch image: %+v", c)
	}
}

func TestRunnerImageCheckFallsBackToActive(t *testing.T) {
	cfg := config.Defaults()
	cfg.DataDir = t.TempDir()
	name := "ubuntu-26.04-" + runtime.GOARCH
	importImage(t, cfg, name) // first import becomes active; vm.template stays ubuntu-24.04
	c := findCheck(t, Run(cfg), "runner-image")
	if c.Severity != OK {
		t.Fatalf("active image must satisfy doctor: %+v", c)
	}
	if !strings.Contains(c.Message, name) || !strings.Contains(c.Message, `"ubuntu-24.04"`) {
		t.Fatalf("fallback message must name the active image and the configured template: %s", c.Message)
	}
}

func TestRunnerImageCheckNamesConfiguredTemplate(t *testing.T) {
	cfg := config.Defaults()
	cfg.DataDir = t.TempDir()
	cfg.VM.Template = "ubuntu-26.04"
	c := findCheck(t, Run(cfg), "runner-image")
	if c.Severity != Error {
		t.Fatalf("empty catalog must be an error: %+v", c)
	}
	if !strings.Contains(c.Message, `"ubuntu-26.04"`) || strings.Contains(c.Message, "24.04") {
		t.Fatalf("error must name the configured template, not a hardcoded one: %s", c.Message)
	}
}

func TestRunnerImageCheckSuggestsActivate(t *testing.T) {
	cfg := config.Defaults()
	cfg.DataDir = t.TempDir()
	dir := cfg.Layout().Runner
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	m := struct {
		Images []images.RunnerImage `json:"images"`
		Active string               `json:"active"`
	}{Images: []images.RunnerImage{{Name: "ubuntu-26.04-amd64", Path: filepath.Join(dir, "ubuntu-26.04-amd64.qcow2")}}}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "MANIFEST"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	c := findCheck(t, Run(cfg), "runner-image")
	if c.Severity != Error {
		t.Fatalf("images without an active one must be an error: %+v", c)
	}
	if !strings.Contains(c.Message, "ubuntu-26.04-amd64") || !strings.Contains(c.Message, "activate") {
		t.Fatalf("error must list the known images and suggest activate: %s", c.Message)
	}
}
