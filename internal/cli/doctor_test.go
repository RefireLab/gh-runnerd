package cli

import (
	"strings"
	"testing"

	"github.com/RefireLab/gh-runnerd/internal/config"
	"github.com/RefireLab/gh-runnerd/internal/doctor"
)

func TestDoctorConfigCheckNamesLoadedFile(t *testing.T) {
	t.Parallel()
	c := configCheck("/etc/gh-runnerd/config.toml", config.Defaults())
	if c.Severity != doctor.OK || c.Message != "/etc/gh-runnerd/config.toml" {
		t.Fatalf("loaded config must be reported verbatim: %+v", c)
	}
}

func TestDoctorConfigCheckWarnsAboutDefaults(t *testing.T) {
	t.Parallel()
	c := configCheck("", config.Defaults())
	if c.Severity != doctor.Warn {
		t.Fatalf("missing config must warn, got %+v", c)
	}
	if !strings.Contains(c.Message, "built-in defaults") {
		t.Fatalf("warning must say the checks use defaults: %s", c.Message)
	}
}
