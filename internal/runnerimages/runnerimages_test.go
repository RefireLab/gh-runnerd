package runnerimages

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFamilyHelpers(t *testing.T) {
	if !ValidFamily("ubuntu-24.04") || !ValidFamily("ubuntu-26.04") {
		t.Fatal("valid families rejected")
	}
	for _, bad := range []string{"ubuntu24.04", "ubuntu-latest", "debian-12", "ubuntu-24.04-arm64", ""} {
		if ValidFamily(bad) {
			t.Fatalf("family %q should be invalid", bad)
		}
	}
	if got := UbuntuVersion("ubuntu-26.04"); got != "26.04" {
		t.Fatalf("UbuntuVersion = %q", got)
	}
	if got := ImageOS("ubuntu-24.04"); got != "ubuntu24" {
		t.Fatalf("ImageOS = %q", got)
	}
	if got := TagPrefix("ubuntu-24.04", "amd64"); got != "ubuntu24/" {
		t.Fatalf("TagPrefix amd64 = %q", got)
	}
	if got := TagPrefix("ubuntu-26.04", "arm64"); got != "ubuntu26-arm64/" {
		t.Fatalf("TagPrefix arm64 = %q", got)
	}
	if got := TemplateFile("ubuntu-24.04", "amd64"); got != "build.ubuntu-24_04.pkr.hcl" {
		t.Fatalf("TemplateFile = %q", got)
	}
	if got := TemplateFile("ubuntu-24.04", "arm64"); got != "build.ubuntu-24_04-arm64.pkr.hcl" {
		t.Fatalf("TemplateFile arm64 = %q", got)
	}
	if got := ToolsetFile("ubuntu-26.04", "amd64"); got != "toolset-2604.json" {
		t.Fatalf("ToolsetFile = %q", got)
	}
	if got := ToolsetFile("ubuntu-22.04", "arm64"); got != "toolset-2204-arm64.json" {
		t.Fatalf("ToolsetFile arm64 = %q", got)
	}
}

func TestParseFlavor(t *testing.T) {
	for in, want := range map[string]Flavor{
		"":          FlavorMinimal,
		"minimal":   FlavorMinimal,
		"Essential": FlavorEssential,
		" full ":    FlavorFull,
	} {
		got, err := ParseFlavor(in)
		if err != nil || got != want {
			t.Fatalf("ParseFlavor(%q) = %v, %v", in, got, err)
		}
	}
	if _, err := ParseFlavor("kitchen-sink"); err == nil {
		t.Fatal("bad flavor accepted")
	}
}

func TestMatchLatestTags(t *testing.T) {
	tags := []string{
		"win25/20260818.232",
		"ubuntu26/20260817.108",
		"ubuntu26-arm64/20260817.95",
		"ubuntu24/20260816.277",
		"ubuntu24/20260810.271", // older, must not win
		"ubuntu22/20260817.266",
	}
	got := matchLatestTags(tags, "amd64")
	if got["ubuntu-24.04"] != "ubuntu24/20260816.277" {
		t.Fatalf("ubuntu-24.04 tag = %q", got["ubuntu-24.04"])
	}
	if got["ubuntu-26.04"] != "ubuntu26/20260817.108" {
		t.Fatalf("ubuntu-26.04 tag = %q", got["ubuntu-26.04"])
	}
	arm := matchLatestTags(tags, "arm64")
	if arm["ubuntu-26.04"] != "ubuntu26-arm64/20260817.95" {
		t.Fatalf("ubuntu-26.04 arm tag = %q", arm["ubuntu-26.04"])
	}
	if _, ok := arm["ubuntu-24.04"]; ok {
		t.Fatal("no arm64 ubuntu-24.04 tag in the list, but one was matched")
	}
}

func TestReleaseVersion(t *testing.T) {
	if v := ReleaseVersion("ubuntu24/20260816.277"); v != "20260816.277" {
		t.Fatalf("ReleaseVersion = %q", v)
	}
	if v := ReleaseVersion("main"); v != "dev" {
		t.Fatalf("ReleaseVersion(main) = %q", v)
	}
}

func TestRecommendedResources(t *testing.T) {
	if RecommendedDiskGB(FlavorFull) <= RecommendedDiskGB(FlavorEssential) {
		t.Fatal("full must need more disk than essential")
	}
	if RecommendedTimeout(FlavorEssential) < time.Hour {
		t.Fatal("essential timeout too small")
	}
	if RecommendedDiskGB(FlavorMinimal) != 0 {
		t.Fatal("minimal must not override the configured disk")
	}
}

// fakeTree builds a runner-images checkout with the vendored real
// templates and the script/toolset files the plan expects.
func fakeTree(t *testing.T, scripts ...string) string {
	t.Helper()
	root := t.TempDir()
	tpl := filepath.Join(root, "images", "ubuntu", "templates")
	build := filepath.Join(root, "images", "ubuntu", "scripts", "build")
	toolsets := filepath.Join(root, "images", "ubuntu", "toolsets")
	for _, d := range []string{tpl, build, toolsets} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{"build.ubuntu-26_04.pkr.hcl", "variable.ubuntu.pkr.hcl"} {
		raw, err := os.ReadFile(filepath.Join("testdata", f))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(tpl, f), raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(toolsets, "toolset-2604.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, s := range scripts {
		if err := os.WriteFile(filepath.Join(build, s), []byte("#!/bin/bash -e\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// upstream26Scripts is every script the vendored 26.04 template references.
func upstream26Scripts(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "build.ubuntu-26_04.pkr.hcl"))
	if err != nil {
		t.Fatal(err)
	}
	varsRaw, err := os.ReadFile(filepath.Join("testdata", "variable.ubuntu.pkr.hcl"))
	if err != nil {
		t.Fatal(err)
	}
	steps, err := ParseTemplate(string(raw), parseVariableDefaults(string(varsRaw)))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, s := range steps {
		names = append(names, s.Scripts...)
	}
	return names
}

func TestParseTemplateRealUpstream(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "build.ubuntu-26_04.pkr.hcl"))
	if err != nil {
		t.Fatal(err)
	}
	varsRaw, err := os.ReadFile(filepath.Join("testdata", "variable.ubuntu.pkr.hcl"))
	if err != nil {
		t.Fatal(err)
	}
	vars := parseVariableDefaults(string(varsRaw))
	if vars["helper_script_folder"] != "/imagegeneration/helpers" {
		t.Fatalf("helper_script_folder default = %q", vars["helper_script_folder"])
	}
	vars["image_version"] = "20260817.108"
	vars["image_os"] = "ubuntu26"

	steps, err := ParseTemplate(string(raw), vars)
	if err != nil {
		t.Fatal(err)
	}

	var all []string
	pwshScripts := map[string]bool{}
	for _, s := range steps {
		for _, name := range s.Scripts {
			all = append(all, name)
			if s.Pwsh {
				pwshScripts[name] = true
			}
		}
	}
	if len(all) < 40 {
		t.Fatalf("expected the full upstream script list, got %d: %v", len(all), all)
	}
	if all[0] != "configure-apt-mock.sh" {
		t.Fatalf("first script = %q", all[0])
	}
	if all[len(all)-1] != "post-build-validation.sh" {
		t.Fatalf("last script = %q", all[len(all)-1])
	}
	for _, want := range []string{"install-git.sh", "install-nodejs.sh", "install-python.sh", "install-docker.sh", "cleanup.sh"} {
		found := false
		for _, got := range all {
			if got == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("script %s missing from parse: %v", want, all)
		}
	}
	// Inline-only provisioners (mkdir, mv, reboot, waagent deprovision)
	// must not leak in as steps.
	for _, got := range all {
		if strings.Contains(got, "reboot") || strings.Contains(got, "waagent") {
			t.Fatalf("inline provisioner leaked into steps: %q", got)
		}
	}
	if !pwshScripts["Install-PowerShellModules.ps1"] || !pwshScripts["Install-Toolset.ps1"] {
		t.Fatalf("pwsh steps not detected: %v", pwshScripts)
	}
	if pwshScripts["install-git.sh"] {
		t.Fatal("shell step misdetected as pwsh")
	}

	// Environment resolution: find the configure-environment step and
	// check its vars resolved through variable defaults and overrides.
	found := false
	for _, s := range steps {
		for _, name := range s.Scripts {
			if name != "configure-environment.sh" {
				continue
			}
			found = true
			env := strings.Join(s.Env, " ")
			if !strings.Contains(env, "HELPER_SCRIPTS=/imagegeneration/helpers") {
				t.Fatalf("configure-environment env missing helpers: %v", s.Env)
			}
			if !strings.Contains(env, "IMAGE_OS=ubuntu26") || !strings.Contains(env, "IMAGE_VERSION=20260817.108") {
				t.Fatalf("configure-environment env missing image vars: %v", s.Env)
			}
		}
	}
	if !found {
		t.Fatal("configure-environment.sh not found")
	}
}

func TestBuildPlanFullAndEssential(t *testing.T) {
	scripts := upstream26Scripts(t)
	root := fakeTree(t, scripts...)

	full, err := BuildPlan(root, "ubuntu-26.04", "amd64", FlavorFull, "ubuntu26/20260817.108", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if full.ScriptCount() != len(scripts) {
		t.Fatalf("full plan has %d scripts, upstream has %d", full.ScriptCount(), len(scripts))
	}
	if len(full.Dropped) != 0 {
		t.Fatalf("full plan dropped: %v", full.Dropped)
	}

	ess, err := BuildPlan(root, "ubuntu-26.04", "amd64", FlavorEssential, "ubuntu26/20260817.108", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if ess.ScriptCount() >= full.ScriptCount() {
		t.Fatal("essential must be a strict subset of full")
	}
	names := ess.ScriptNames()
	for _, banned := range []string{"install-android-sdk.sh", "install-haskell.sh", "install-codeql-bundle.sh", "install-google-chrome.sh", "Install-Toolset.ps1"} {
		for _, got := range names {
			if got == banned {
				t.Fatalf("essential includes heavyweight script %s", banned)
			}
		}
	}
	for _, want := range []string{"install-git.sh", "install-github-cli.sh", "install-nodejs.sh", "install-python.sh", "install-docker.sh", "cleanup.sh", "configure-system.sh"} {
		found := false
		for _, got := range names {
			if got == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("essential misses %s: %v", want, names)
		}
	}
	// Order preserved from the template: configure-apt-mock first.
	if names[0] != "configure-apt-mock.sh" {
		t.Fatalf("essential first script = %q", names[0])
	}

	// skip filter
	skipped, err := BuildPlan(root, "ubuntu-26.04", "amd64", FlavorEssential, "main", []string{"install-nodejs.sh"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, got := range skipped.ScriptNames() {
		if got == "install-nodejs.sh" {
			t.Fatal("skip filter ignored")
		}
	}

	// only filter (used by smoke tests)
	only, err := BuildPlan(root, "ubuntu-26.04", "amd64", FlavorFull, "main", nil, []string{"install-git.sh", "configure-apt.sh"})
	if err != nil {
		t.Fatal(err)
	}
	if only.ScriptCount() != 2 {
		t.Fatalf("only filter kept %d scripts: %v", only.ScriptCount(), only.ScriptNames())
	}

	// missing arch template
	if _, err := BuildPlan(root, "ubuntu-24.04", "amd64", FlavorFull, "main", nil, nil); err == nil {
		t.Fatal("missing template must error")
	}
}

func TestSetupScript(t *testing.T) {
	scripts := upstream26Scripts(t)
	root := fakeTree(t, scripts...)
	plan, err := BuildPlan(root, "ubuntu-26.04", "amd64", FlavorEssential, "ubuntu26/20260817.108", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	sh := plan.SetupScript()

	for _, want := range []string{
		"cp \"$SRC/toolsets/toolset-2604.json\" \"$IMG/installers/toolset.json\"",
		"/etc/waagent.conf",
		"run_step 1 ",
		"'configure-apt-mock.sh'",
		"'HELPER_SCRIPTS=/imagegeneration/helpers'",
		"post-generation",
		"/etc/gh-runnerd-image",
		"PARITY_FAILED",
	} {
		if !strings.Contains(sh, want) {
			t.Fatalf("setup script missing %q\n%s", want, sh)
		}
	}
	// Critical scripts marked, non-critical not.
	if !strings.Contains(sh, "shell yes 'install-docker.sh'") {
		t.Fatal("install-docker.sh not marked critical")
	}
	if strings.Contains(sh, "yes 'install-git.sh'") {
		t.Fatal("install-git.sh wrongly marked critical")
	}
	// Essential stubs invoke_tests right after configure-environment.
	envIdx := strings.Index(sh, "'configure-environment.sh'")
	stubIdx := strings.Index(sh, "printf '#!/bin/sh\\nexit 0\\n' > /usr/local/bin/invoke_tests")
	if envIdx < 0 || stubIdx < 0 || stubIdx < envIdx {
		t.Fatalf("invoke_tests stub not placed after configure-environment (env=%d stub=%d)", envIdx, stubIdx)
	}

	fullPlan, err := BuildPlan(root, "ubuntu-26.04", "amd64", FlavorFull, "main", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	fullSh := fullPlan.SetupScript()
	if strings.Contains(fullSh, "exit 0\\n' > /usr/local/bin/invoke_tests") {
		t.Fatal("full flavor must keep real invoke_tests")
	}
	if !strings.Contains(fullSh, "pwsh -f") {
		t.Fatal("full flavor must run pwsh steps")
	}
}

func TestDiscoverFamilies(t *testing.T) {
	root := fakeTree(t)
	fams, err := DiscoverFamilies(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(fams) != 1 || fams[0] != "ubuntu-26.04" {
		t.Fatalf("families = %v", fams)
	}
}

func TestShellQuote(t *testing.T) {
	if got := shellQuote("A=b c"); got != "'A=b c'" {
		t.Fatalf("quote = %q", got)
	}
	if got := shellQuote("it's"); got != `'it'\''s'` {
		t.Fatalf("quote = %q", got)
	}
}
