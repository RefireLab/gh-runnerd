package doctor

import (
	"strings"
	"testing"

	"github.com/RefireLab/gh-runnerd/internal/config"
	"github.com/RefireLab/gh-runnerd/internal/host"
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
