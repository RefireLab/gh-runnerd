package doctor

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/RefireLab/gh-runnerd/internal/config"
	"github.com/RefireLab/gh-runnerd/internal/host"
	"github.com/RefireLab/gh-runnerd/internal/images"
	"github.com/RefireLab/gh-runnerd/internal/qemu"
	"github.com/RefireLab/gh-runnerd/internal/tlsutil"
)

type Severity string

const (
	OK    Severity = "ok"
	Warn  Severity = "warn"
	Error Severity = "error"
)

type Check struct {
	Name     string   `json:"name"`
	Severity Severity `json:"severity"`
	Message  string   `json:"message"`
}

type Report struct {
	Checks []Check `json:"checks"`
}

func (r Report) HasErrors() bool {
	for _, c := range r.Checks {
		if c.Severity == Error {
			return true
		}
	}
	return false
}

func (r Report) String() string {
	var b strings.Builder
	for _, c := range r.Checks {
		fmt.Fprintf(&b, "%-8s %-18s %s\n", strings.ToUpper(string(c.Severity)), c.Name, c.Message)
	}
	return b.String()
}

func Run(cfg config.Config) Report {
	var checks []Check
	add := func(c Check) { checks = append(checks, c) }

	info, err := host.ReadOSRelease()
	if err != nil {
		add(Check{"os", Error, fmt.Sprintf("cannot read /etc/os-release: %v", err)})
	} else if err := host.CheckUbuntu(info); err != nil {
		add(Check{"os", Error, err.Error()})
	} else {
		add(Check{"os", OK, info.Pretty})
	}

	if qemu.HasKVM() {
		add(Check{"kvm", OK, "/dev/kvm present"})
	} else {
		add(Check{"kvm", Error, "/dev/kvm missing — install qemu-system and enable virtualization"})
	}

	bin := qemu.DefaultBinary()
	if p, err := exec.LookPath(bin); err == nil {
		add(Check{"qemu", OK, p})
	} else {
		add(Check{"qemu", Error, bin + " not in PATH"})
	}
	if p, err := exec.LookPath("qemu-img"); err == nil {
		add(Check{"qemu-img", OK, p})
	} else {
		add(Check{"qemu-img", Error, "qemu-img not in PATH"})
	}
	if _, err := exec.LookPath("iptables"); err == nil {
		add(Check{"iptables", OK, "iptables found"})
	} else {
		add(Check{"iptables", Warn, "iptables not in PATH; serve needs it to create the isolated bridge"})
	}

	dirs := cfg.Layout()
	if st, err := os.Stat(dirs.Root); err == nil && st.IsDir() {
		add(Check{"data-dir", OK, dirs.Root})
	} else {
		add(Check{"data-dir", Warn, fmt.Sprintf("%s does not exist yet — run gh-runnerd init", dirs.Root)})
	}

	if tlsutil.Exists(dirs.CA) {
		add(Check{"ca", OK, dirs.CA})
	} else {
		add(Check{"ca", Warn, "internal CA missing — run gh-runnerd init"})
	}

	cat := images.Catalog{Dir: dirs.Runner}
	name := cfg.VM.Template
	if name == "ubuntu-24.04" {
		name = images.DefaultName()
	}
	if img, err := cat.Find(name); err == nil {
		add(Check{"runner-image", OK, img.Path})
	} else if _, err := cat.Active(); err == nil {
		add(Check{"runner-image", OK, "active template present"})
	} else {
		add(Check{"runner-image", Error, fmt.Sprintf("no Ubuntu 24.04 runner image in %s — run images/runner/bake.sh or gh-runnerd runner-image import", dirs.Runner)})
	}

	if cfg.HasGitHubAuth() {
		add(Check{"github-auth", OK, "token or GitHub App configured"})
	} else {
		add(Check{"github-auth", Error, "set github.token or github.app_id + app_private_key_path + installation_id"})
	}

	if strings.ToLower(cfg.GitHub.Scope) == "repo" && (cfg.GitHub.Owner == "" || cfg.GitHub.Repo == "") {
		add(Check{"github-scope", Error, "github.owner and github.repo are required for repo scope"})
	} else if strings.ToLower(cfg.GitHub.Scope) == "org" && cfg.GitHub.Org == "" {
		add(Check{"github-scope", Error, "github.org is required for org scope"})
	} else {
		add(Check{"github-scope", OK, cfg.GitHub.Scope})
	}

	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		add(Check{"arch", Error, "unsupported architecture " + runtime.GOARCH})
	} else {
		add(Check{"arch", OK, runtime.GOARCH})
	}

	if cfg.Webhook.Secret == "" {
		add(Check{"webhook", Warn, "webhook.secret empty — signature checks will fail; poll fallback can still work"})
	} else {
		add(Check{"webhook", OK, "secret configured"})
	}

	if _, err := os.Stat(filepath.Join(dirs.State, "status.json")); err == nil {
		add(Check{"daemon", OK, "status file present"})
	} else {
		add(Check{"daemon", Warn, "daemon does not appear to be running"})
	}

	return Report{Checks: checks}
}
