package runnerimages

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Plan is a resolved, filtered provisioning sequence for one bake.
type Plan struct {
	Family  string // ubuntu-24.04
	Arch    string // amd64 | arm64
	Ref     string // ubuntu24/20260816.277 or main
	Flavor  Flavor
	Toolset string   // toolset JSON basename
	Steps   []Step   // ordered, filtered
	Dropped []string // requested scripts missing upstream (renames)
}

// essentialScripts is the curated FlavorEssential subset: the everyday
// tools most workflows assume, without the multi-gigabyte toolchains
// (Android, Haskell, CodeQL, browsers, clouds...). Order still comes from
// the upstream template; this is only a membership filter.
var essentialScripts = map[string]bool{
	"configure-apt-mock.sh":    true,
	"install-ms-repos.sh":      true,
	"configure-apt-sources.sh": true,
	"configure-apt.sh":         true,
	"configure-limits.sh":      true,
	"configure-image-data.sh":  true,
	"configure-environment.sh": true,
	"install-apt-vital.sh":     true,
	"install-powershell.sh":    true,
	"install-actions-cache.sh": true,
	"install-apt-common.sh":    true,
	"install-cmake.sh":         true,
	"install-git.sh":           true,
	"install-git-lfs.sh":       true,
	"install-github-cli.sh":    true,
	"install-zstd.sh":          true,
	"install-yq.sh":            true,
	"install-ninja.sh":         true,
	"install-nvm.sh":           true,
	"install-nodejs.sh":        true,
	"install-python.sh":        true,
	"install-pipx-packages.sh": true,
	"install-docker.sh":        true,
	"configure-dpkg.sh":        true,
	"list-dpkg.sh":             true,
	"cleanup.sh":               true,
	"configure-system.sh":      true,
}

// criticalScripts must succeed or the image is useless — their failure
// fails the whole bake instead of being reported as a warning.
var criticalScripts = map[string]bool{
	"configure-apt.sh":         true,
	"configure-environment.sh": true,
	"install-apt-vital.sh":     true,
	"install-docker.sh":        true,
	"configure-apt-sources.sh": true,
}

// BuildPlan parses the upstream template for family+arch and filters the
// steps for the flavor. skip and only filter by script basename; only is
// meant for debugging and smoke tests.
func BuildPlan(root, family, arch string, flavor Flavor, ref string, skip, only []string) (Plan, error) {
	if flavor == FlavorMinimal {
		return Plan{}, fmt.Errorf("minimal flavor has no upstream plan")
	}
	if !ValidFamily(family) {
		return Plan{}, fmt.Errorf("unknown image %q (expected e.g. ubuntu-24.04)", family)
	}
	ubuntuDir := filepath.Join(root, "images", "ubuntu")
	tmplPath := filepath.Join(ubuntuDir, "templates", TemplateFile(family, arch))
	tmpl, err := os.ReadFile(tmplPath)
	if err != nil {
		return Plan{}, fmt.Errorf("upstream has no %s image for %s: %w", family, arch, err)
	}
	varsRaw, err := os.ReadFile(filepath.Join(ubuntuDir, "templates", "variable.ubuntu.pkr.hcl"))
	if err != nil {
		return Plan{}, fmt.Errorf("read upstream variables: %w", err)
	}
	vars := parseVariableDefaults(string(varsRaw))
	vars["image_version"] = ReleaseVersion(ref)
	vars["image_os"] = ImageOS(family)

	steps, err := ParseTemplate(string(tmpl), vars)
	if err != nil {
		return Plan{}, err
	}

	toolset := ToolsetFile(family, arch)
	if _, err := os.Stat(filepath.Join(ubuntuDir, "toolsets", toolset)); err != nil {
		return Plan{}, fmt.Errorf("upstream toolset %s missing: %w", toolset, err)
	}

	skipSet := toSet(skip)
	onlySet := toSet(only)
	scriptsDir := filepath.Join(ubuntuDir, "scripts", "build")
	plan := Plan{Family: family, Arch: arch, Ref: ref, Flavor: flavor, Toolset: toolset}
	for _, step := range steps {
		filtered := Step{Pwsh: step.Pwsh, Env: step.Env}
		for _, script := range step.Scripts {
			if flavor == FlavorEssential && !essentialScripts[script] {
				continue
			}
			if skipSet[script] {
				continue
			}
			if len(onlySet) > 0 && !onlySet[script] {
				continue
			}
			if _, err := os.Stat(filepath.Join(scriptsDir, script)); err != nil {
				plan.Dropped = append(plan.Dropped, script)
				continue
			}
			filtered.Scripts = append(filtered.Scripts, script)
		}
		if len(filtered.Scripts) > 0 {
			plan.Steps = append(plan.Steps, filtered)
		}
	}
	if len(plan.Steps) == 0 {
		return Plan{}, fmt.Errorf("nothing to install: every upstream script was filtered out")
	}
	return plan, nil
}

// ScriptCount is the number of individual scripts across all steps.
func (p Plan) ScriptCount() int {
	n := 0
	for _, s := range p.Steps {
		n += len(s.Scripts)
	}
	return n
}

// ScriptNames lists every script in run order.
func (p Plan) ScriptNames() []string {
	var names []string
	for _, s := range p.Steps {
		names = append(names, s.Scripts...)
	}
	return names
}

func toSet(items []string) map[string]bool {
	set := map[string]bool{}
	for _, it := range items {
		it = strings.TrimSpace(it)
		if it != "" {
			set[it] = true
		}
	}
	return set
}
