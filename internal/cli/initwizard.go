package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/RefireLab/gh-runnerd/internal/bake"
	"github.com/RefireLab/gh-runnerd/internal/config"
	"github.com/RefireLab/gh-runnerd/internal/ghapi"
	"github.com/RefireLab/gh-runnerd/internal/githubutil"
	"github.com/RefireLab/gh-runnerd/internal/host"
	"github.com/RefireLab/gh-runnerd/internal/runnerimages"
	"github.com/RefireLab/gh-runnerd/internal/sysinstall"
	"github.com/RefireLab/gh-runnerd/internal/wizard"
)

const systemConfigPath = "/etc/gh-runnerd/config.toml"

func runInitWizard(cmd *cobra.Command, p *wizard.Prompter, preset initPreset) error {
	ctx := cmd.Context()

	p.Say("")
	p.Say("gh-runnerd setup")
	p.Say("================")
	p.Say("This wizard sets up everything: config, host packages, the runner VM")
	p.Say("image, and (optionally) a background service. Re-run it any time.")
	p.Say("")

	// Host checks. Ubuntu is a hard requirement; everything else is fixable.
	if err := host.MustUbuntu(); err != nil {
		return err
	}
	p.Say("[ok] Ubuntu host")
	isRoot := os.Geteuid() == 0
	kvmErr := bake.CheckKVM()
	if kvmErr != nil {
		p.Say("[!!] %v", kvmErr)
	} else {
		p.Say("[ok] KVM virtualization (/dev/kvm)")
	}
	if !isRoot {
		p.Say("[!!] not running as root — packages, the system service, and the VM")
		p.Say("     image build all need root. Best: quit and re-run with sudo.")
		cont, err := p.AskYesNo("Continue anyway (config-only setup)?", false)
		if err != nil {
			return err
		}
		if !cont {
			p.Say("Run: sudo %s init", selfInvocation())
			return nil
		}
	}

	// Install mode: system service vs everything-in-this-folder.
	systemWide := false
	if isRoot && sysinstall.HaveSystemd() {
		var err error
		systemWide, err = p.AskYesNo("Install as a system service? (recommended: starts on boot and keeps running; n = keep all files in the current folder)", true)
		if err != nil {
			return err
		}
	}
	cfgPath := "gh-runnerd.toml"
	dataDirInFile := "gh-runnerd-data"
	if systemWide {
		cfgPath = systemConfigPath
		dataDirInFile = "/var/lib/gh-runnerd"
	} else {
		p.Say("Keeping everything in this folder: ./gh-runnerd.toml and ./gh-runnerd-data/")
	}

	// Host packages (QEMU & friends).
	installPackagesStep(p, isRoot)

	// Existing config: reuse or answer again.
	var cfg config.Config
	reused := false
	if _, err := os.Stat(cfgPath); err == nil {
		p.Say("")
		p.Say("Found an existing config: %s", cfgPath)
		useExisting, err := p.AskYesNo("Keep it? (n = answer the questions again and overwrite)", true)
		if err != nil {
			return err
		}
		if useExisting {
			cfg, err = config.Load(cfgPath)
			if err != nil {
				return fmt.Errorf("existing config is broken (%w) — fix or delete %s", err, cfgPath)
			}
			reused = true
		}
	}
	if !reused {
		var err error
		cfg, err = askConfigQuestions(ctx, p, preset)
		if err != nil {
			return err
		}
		cfg.DataDir = dataDirInFile
	}

	// Write config + directories + CA.
	opsCfg := cfg
	if !filepath.IsAbs(opsCfg.DataDir) {
		wd, err := os.Getwd()
		if err != nil {
			return err
		}
		opsCfg.DataDir = filepath.Join(wd, opsCfg.DataDir)
	}
	if err := opsCfg.Layout().Ensure(); err != nil {
		return err
	}
	if err := ensureCA(opsCfg); err != nil {
		return err
	}
	if !reused {
		if err := config.WriteFile(cfgPath, cfg); err != nil {
			return err
		}
		p.Say("[ok] wrote %s", cfgPath)
	}

	// Runner VM image: the one heavy step.
	bakeOK := offerBake(cmd, p, &cfg, opsCfg, cfgPath, !reused, kvmErr)

	// Service (system mode) or how to run (portable mode).
	if systemWide {
		if err := offerService(p, cfgPath); err != nil {
			p.Say("[!!] service setup failed: %v", err)
			p.Say("     you can still run it by hand: sudo gh-runnerd serve --config %s", cfgPath)
		}
	} else {
		p.Say("")
		p.Say("Start the runner daemon from this folder with:")
		p.Say("  sudo %s serve", selfInvocation())
	}

	p.Say("")
	p.Say("Done! Point a workflow at your new runners:")
	p.Say("")
	runsOn := "gh-runnerd"
	if len(cfg.Runner.Labels) > 0 {
		runsOn = cfg.Runner.Labels[0]
	}
	p.Say("  # .github/workflows/ci.yml")
	p.Say("  jobs:")
	p.Say("    build:")
	p.Say("      runs-on: %s", runsOn)
	p.Say("      steps:")
	p.Say("        - uses: actions/checkout@v4")
	p.Say("        - run: echo it works")
	p.Say("")
	if !bakeOK {
		p.Say("Reminder: the VM image is not built yet. Build it with:")
		p.Say("  sudo gh-runnerd runner-image bake")
	}
	p.Say("Health check any time: gh-runnerd doctor")
	return nil
}

// installPackagesStep checks for QEMU & co and installs them via apt when
// the user agrees.
func installPackagesStep(p *wizard.Prompter, isRoot bool) {
	missing := bake.MissingTools(bake.ServeTools(runtime.GOARCH))
	if runtime.GOARCH == "arm64" {
		if _, err := bake.Arm64Firmware(); err != nil {
			missing = append(missing, bake.Tool{Bin: "arm64-uefi-firmware", Apt: "qemu-efi-aarch64"})
		}
	}
	if len(missing) == 0 {
		p.Say("[ok] host packages (qemu, cloud-image-utils, iptables)")
		return
	}
	pkgs := bake.AptPackages(missing)
	aptCmd := "apt-get install -y " + strings.Join(pkgs, " ")
	if !isRoot {
		p.Say("[!!] missing packages — install them with: sudo %s", aptCmd)
		return
	}
	yes, err := p.AskYesNo(fmt.Sprintf("Missing packages: %s. Install them now with apt?", strings.Join(pkgs, ", ")), true)
	if err != nil || !yes {
		p.Say("skipped — install later with: sudo %s", aptCmd)
		return
	}
	p.Say(">> apt-get install -y %s", strings.Join(pkgs, " "))
	if err := aptInstall(pkgs, p); err != nil {
		p.Say("[!!] apt failed: %v", err)
		p.Say("     install by hand: sudo %s", aptCmd)
		return
	}
	p.Say("[ok] packages installed")
}

func aptInstall(pkgs []string, p *wizard.Prompter) error {
	run := func() error {
		args := append([]string{"install", "-y"}, pkgs...)
		c := exec.Command("apt-get", args...)
		c.Env = append(os.Environ(), "DEBIAN_FRONTEND=noninteractive")
		out, err := c.CombinedOutput()
		if err != nil {
			return fmt.Errorf("%s: %s", err, lastLines(string(out), 5))
		}
		return nil
	}
	if err := run(); err != nil {
		p.Say("   retrying after apt-get update...")
		upd := exec.Command("apt-get", "update")
		upd.Env = append(os.Environ(), "DEBIAN_FRONTEND=noninteractive")
		_ = upd.Run()
		return run()
	}
	return nil
}

// askConfigQuestions collects GitHub credentials, the runner target, and
// capacity, validating against the GitHub API where possible.
func askConfigQuestions(ctx context.Context, p *wizard.Prompter, preset initPreset) (config.Config, error) {
	cfg := config.Defaults()
	cfg.Webhook.Secret = randomSecret()

	token, login := askToken(ctx, p, preset.Token, cfg.GitHub)
	cfg.GitHub.Token = token

	if err := askRunnerTarget(ctx, p, &cfg, preset, login); err != nil {
		return config.Config{}, err
	}

	if err := askLabels(p, &cfg); err != nil {
		return config.Config{}, err
	}
	if err := askRunnerGroup(ctx, p, &cfg); err != nil {
		return config.Config{}, err
	}

	if err := askNetwork(p, &cfg); err != nil {
		return config.Config{}, err
	}

	p.Say("")
	maxConc, err := p.AskInt("How many jobs may run at the same time? (each uses ~4 GB RAM while running)", 2)
	if err != nil {
		return config.Config{}, err
	}
	cfg.Pool.MaxConcurrent = maxConc
	warm, err := p.AskIntRange(
		fmt.Sprintf("How many VMs should stay warmed up waiting for jobs? (0 = boot on demand, ~30-40s per job start; each warm VM holds ~4 GB RAM all the time; up to %d)", maxConc),
		0, 0, maxConc)
	if err != nil {
		return config.Config{}, err
	}
	cfg.Pool.MinIdle = warm
	return cfg, nil
}

// askToken explains where to create a token, reads it hidden, and verifies
// it against the GitHub API. Returns the token and the authenticated login.
func askToken(ctx context.Context, p *wizard.Prompter, presetToken string, ghCfg config.GitHubConfig) (string, string) {
	verify := func(tok string) (string, error) {
		probe := ghCfg
		probe.Token = tok
		return ghapi.New(probe).Viewer(ctx)
	}
	if presetToken != "" {
		if login, err := verify(presetToken); err == nil {
			p.Say("[ok] token from --token/GH_RUNNERD_GITHUB_TOKEN works — hello, %s!", login)
			return presetToken, login
		}
		p.Say("[!!] the token from --token/GH_RUNNERD_GITHUB_TOKEN was rejected by GitHub")
	}
	p.Say("")
	p.Say("GitHub token")
	p.Say("------------")
	p.Say("gh-runnerd needs one fine-grained token to register runners. Create it here:")
	p.Say("  https://github.com/settings/personal-access-tokens/new")
	p.Say("  1. Resource owner: your organization (for org runners) or your account")
	p.Say("  2. Repository access: the repo(s) that will run jobs on these runners")
	p.Say("  3. Repository permissions:")
	p.Say("       Actions:        Read-only        (finds queued jobs)")
	p.Say("       Administration: Read and write   (registers repo-level runners)")
	p.Say("  4. Organization permissions (only for org-level runners):")
	p.Say("       Self-hosted runners: Read and write")
	p.Say("  (classic token: \"repo\" scope; org runners also need \"admin:org\")")
	for i := 0; i < 3; i++ {
		tok, err := p.AskSecret("Paste the token (input is hidden; Enter to skip for now)")
		if err != nil || tok == "" {
			p.Say("skipped — add github.token to the config later")
			return "", ""
		}
		login, verr := verify(tok)
		if verr != nil {
			p.Say("[!!] GitHub rejected the token (%v) — try again", shortErr(verr))
			continue
		}
		p.Say("[ok] token works — hello, %s!", login)
		return tok, login
	}
	p.Say("still no valid token — continuing without one; add github.token later")
	return "", ""
}

// askRunnerTarget figures out where runners register: a repository
// (owner/repo) or an organization. Only the token is truly required up
// front; this resolves the rest interactively.
func askRunnerTarget(ctx context.Context, p *wizard.Prompter, cfg *config.Config, preset initPreset, login string) error {
	if preset.Org != "" {
		cfg.GitHub.Scope = "org"
		cfg.GitHub.Org = preset.Org
		return verifyRunnerTarget(ctx, p, cfg)
	}
	if preset.Owner != "" && preset.Repo != "" {
		cfg.GitHub.Scope = "repo"
		cfg.GitHub.Owner = preset.Owner
		cfg.GitHub.Repo = preset.Repo
		return verifyRunnerTarget(ctx, p, cfg)
	}

	p.Say("")
	p.Say("Where should the runners appear?")
	p.Say("  - a repository:    owner/repo   (e.g. %s/my-project)", exampleOwner(login))
	p.Say("  - an organization: orgname      (runners shared by all its repos)")
	for i := 0; i < 3; i++ {
		ans, err := p.Ask("Repository (owner/repo) or organization", "")
		if err != nil {
			return err
		}
		if ans == "" {
			p.Say("skipped — set github.owner/github.repo (or github.org) in the config later")
			return nil
		}
		if owner, repo, ok := strings.Cut(ans, "/"); ok && owner != "" && repo != "" {
			cfg.GitHub.Scope = "repo"
			cfg.GitHub.Owner = strings.TrimSpace(owner)
			cfg.GitHub.Repo = strings.TrimSpace(repo)
		} else if cfg.GitHub.Token != "" {
			typ, terr := ghapi.New(cfg.GitHub).OwnerType(ctx, ans)
			if terr != nil {
				p.Say("[!!] GitHub does not know %q (%v) — try again", ans, shortErr(terr))
				continue
			}
			if typ == "Organization" {
				cfg.GitHub.Scope = "org"
				cfg.GitHub.Org = ans
			} else {
				p.Say("%s is a personal account; personal runners live in a repository.", ans)
				repo, rerr := p.Ask(fmt.Sprintf("Repository name inside %s", ans), "")
				if rerr != nil || repo == "" {
					continue
				}
				cfg.GitHub.Scope = "repo"
				cfg.GitHub.Owner = ans
				cfg.GitHub.Repo = repo
			}
		} else {
			// No token to look the name up; assume org as typed.
			cfg.GitHub.Scope = "org"
			cfg.GitHub.Org = ans
		}
		if err := verifyRunnerTarget(ctx, p, cfg); err != nil {
			continue
		}
		if strings.EqualFold(cfg.GitHub.Scope, "org") {
			repos, err := p.Ask("Which repos should be watched for jobs? (owner/repo, comma-separated; leave empty if you will configure a webhook)", "")
			if err == nil && repos != "" {
				for _, r := range strings.Split(repos, ",") {
					if r = strings.TrimSpace(r); r != "" {
						cfg.GitHub.PollRepos = append(cfg.GitHub.PollRepos, r)
					}
				}
			}
		}
		return nil
	}
	p.Say("could not confirm runner access — fix github.* in the config later, then run gh-runnerd doctor")
	return nil
}

func askLabels(p *wizard.Prompter, cfg *config.Config) error {
	p.Say("")
	p.Say("Runner labels")
	p.Say("Jobs pick these VMs with runs-on: <label>. Keep gh-runnerd as a unique")
	p.Say("label so they do not steal ordinary self-hosted / Linux jobs.")
	def := strings.Join(cfg.Runner.Labels, ", ")
	if def == "" {
		def = "gh-runnerd"
	}
	raw, err := p.Ask("Labels (comma-separated)", def)
	if err != nil {
		return err
	}
	labels := githubutil.ParseLabelList(raw)
	if len(labels) == 0 {
		labels = []string{"gh-runnerd"}
	}
	cfg.Runner.Labels = labels
	p.Say("[ok] labels: %s", strings.Join(labels, ", "))
	return nil
}

func askRunnerGroup(ctx context.Context, p *wizard.Prompter, cfg *config.Config) error {
	p.Say("")
	p.Say("Runner group (where they appear in the GitHub org/repo settings)")
	var groups []ghapi.RunnerGroup
	if cfg.GitHub.Token != "" && (cfg.GitHub.Org != "" || (cfg.GitHub.Owner != "" && cfg.GitHub.Repo != "")) {
		listed, err := ghapi.New(cfg.GitHub).ListRunnerGroups(ctx)
		if err != nil {
			p.Say("[!!] could not list runner groups (%v) — Default (id 1) is the usual choice", shortErr(err))
		} else {
			groups = listed
			for _, g := range groups {
				mark := ""
				if g.Default {
					mark = " — default"
				}
				p.Say("  %s (id %d)%s", g.Name, g.ID, mark)
			}
		}
	}
	def := "Default"
	for _, g := range groups {
		if g.Default {
			def = g.Name
			break
		}
	}
	for i := 0; i < 3; i++ {
		ans, err := p.Ask("Runner group (name or id)", def)
		if err != nil {
			return err
		}
		g, rerr := ghapi.ResolveRunnerGroup(groups, ans)
		if rerr != nil {
			p.Say("[!!] %v", rerr)
			continue
		}
		cfg.GitHub.RunnerGroupID = g.ID
		p.Say("[ok] runner group %s (id %d)", g.Name, g.ID)
		return nil
	}
	p.Say("using Default (id 1) — change github.runner_group_id in the config later")
	cfg.GitHub.RunnerGroupID = 1
	return nil
}

func verifyRunnerTarget(ctx context.Context, p *wizard.Prompter, cfg *config.Config) error {
	if cfg.GitHub.Token == "" {
		return nil
	}
	if err := ghapi.New(cfg.GitHub).CheckRunnerAccess(ctx); err != nil {
		p.Say("[!!] the token cannot manage runners there (%v)", shortErr(err))
		if strings.EqualFold(cfg.GitHub.Scope, "org") {
			p.Say("     needed: organization permission \"Self-hosted runners: Read and write\";")
			p.Say("     the token's resource owner must be %s and you must be an org admin", cfg.GitHub.Org)
		} else {
			p.Say("     needed on %s/%s: \"Administration: Read and write\" and \"Actions: Read-only\"", cfg.GitHub.Owner, cfg.GitHub.Repo)
		}
		return err
	}
	target := cfg.GitHub.Owner + "/" + cfg.GitHub.Repo
	if strings.EqualFold(cfg.GitHub.Scope, "org") {
		target = "organization " + cfg.GitHub.Org
	}
	p.Say("[ok] runner access confirmed for %s", target)
	return nil
}

// offerBake asks to build the VM image now (and which software set it
// should carry) and reports whether an image is ready afterwards.
func offerBake(cmd *cobra.Command, p *wizard.Prompter, cfg *config.Config, opsCfg config.Config, cfgPath string, persist bool, kvmErr error) bool {
	if hasActiveRunnerImage(opsCfg) {
		p.Say("[ok] runner VM image already present")
		if opsCfg.Network.HostIP != "10.87.0.1" {
			p.Say("     note: if you changed the VM network since this image was built,")
			p.Say("     rebuild it so VMs learn the new address: sudo gh-runnerd runner-image update")
		}
		return true
	}
	p.Say("")
	if kvmErr != nil {
		p.Say("Skipping the VM image build: %v", kvmErr)
		return false
	}
	if missing := bake.MissingTools(bake.BakeTools(runtime.GOARCH)); len(missing) > 0 {
		p.Say("Skipping the VM image build: missing packages (see above)")
		return false
	}
	yes, err := p.AskYesNo("Build the runner VM image now? (one time download + build)", true)
	if err != nil || !yes {
		p.Say("later, run: sudo gh-runnerd runner-image bake")
		return false
	}

	flavor, family := askImageChoice(cmd, p, cfg)
	if string(flavor) != cfg.Image.Flavor || family != cfg.Image.Upstream {
		cfg.Image.Flavor = string(flavor)
		cfg.Image.Upstream = family
		if persist {
			if err := config.WriteFile(cfgPath, *cfg); err != nil {
				p.Say("[!!] could not record the image choice in %s: %v", cfgPath, err)
			}
		} else {
			p.Say("     to keep this choice for runner-image update, add to %s:", cfgPath)
			p.Say("       [image]")
			p.Say("       flavor = %q", flavor)
			p.Say("       upstream = %q", family)
		}
	}
	opsCfg.Image.Flavor = string(flavor)
	opsCfg.Image.Upstream = family

	if err := bakeAndInstall(cmd.Context(), opsCfg, cmd.OutOrStdout(), bakeOverrides{}); err != nil {
		p.Say("[!!] image build failed: %v", err)
		p.Say("     fix the issue and re-run: sudo gh-runnerd runner-image bake")
		return false
	}
	return true
}

// askImageChoice picks the image flavor and, for hosted flavors, the
// upstream Ubuntu image (ubuntu-24.04, ubuntu-26.04, ...).
func askImageChoice(cmd *cobra.Command, p *wizard.Prompter, cfg *config.Config) (runnerimages.Flavor, string) {
	flavorDefault := 0
	switch runnerimages.Flavor(cfg.Image.Flavor) {
	case runnerimages.FlavorEssential:
		flavorDefault = 1
	case runnerimages.FlavorFull:
		flavorDefault = 2
	case runnerimages.FlavorMinimal:
		// keep 0
	}
	idx, err := p.Select("Which software should the runner VMs carry?", []string{
		"minimal   — Docker + runner only (~2 GB image, 10-20 min build)",
		"essential — the everyday tools from GitHub's runner images: git, gh, node, python, cmake, docker... (~10 GB image, ~1-2 h build)",
		"full      — everything GitHub's ubuntu-latest ships: browsers, JDKs, Android, CodeQL... (~60-80 GB image, needs ~130 GB free, many hours)",
	}, flavorDefault)
	if err != nil {
		return runnerimages.FlavorMinimal, firstNonEmptyStr(cfg.Image.Upstream, "ubuntu-24.04")
	}
	flavor := [...]runnerimages.Flavor{runnerimages.FlavorMinimal, runnerimages.FlavorEssential, runnerimages.FlavorFull}[idx]

	family := firstNonEmptyStr(cfg.Image.Upstream, "ubuntu-24.04")
	if flavor == runnerimages.FlavorMinimal {
		return flavor, family
	}

	families := runnerimages.KnownFamilies()
	ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
	defer cancel()
	tags, terr := runnerimages.LatestReleases(ctx, cfg.GitHub.Token, runtime.GOARCH)
	options := make([]string, 0, len(families))
	def := 0
	for i, f := range families {
		label := f
		if terr == nil {
			if tag, ok := tags[f]; ok {
				label += "  (release " + tag + ")"
			} else {
				label += "  (no release for " + runtime.GOARCH + ")"
			}
		}
		if f == family {
			def = i
		}
		options = append(options, label)
	}
	fidx, err := p.Select("Which Ubuntu image should it mirror?", options, def)
	if err != nil {
		return flavor, family
	}
	return flavor, families[fidx]
}

func offerService(p *wizard.Prompter, cfgPath string) error {
	guest, err := bake.LocateGuest("")
	if err != nil {
		p.Say("[!!] %v", err)
		guest = ""
	}
	binPath, err := sysinstall.InstallBinaries(guest, sysinstall.BinDir)
	if err != nil {
		return fmt.Errorf("install binaries into %s: %w", sysinstall.BinDir, err)
	}
	if err := sysinstall.InstallUnit(binPath, cfgPath); err != nil {
		return err
	}
	p.Say("[ok] installed binaries in %s and the systemd service", sysinstall.BinDir)
	start, err := p.AskYesNo("Start gh-runnerd now and on every boot?", true)
	if err != nil || !start {
		p.Say("start later with: sudo systemctl enable --now gh-runnerd")
		return nil
	}
	if err := sysinstall.EnableNow(); err != nil {
		return err
	}
	p.Say("[ok] service is running — check it with: systemctl status gh-runnerd")
	return nil
}

func selfInvocation() string {
	exe, err := os.Executable()
	if err != nil {
		return "gh-runnerd"
	}
	wd, err := os.Getwd()
	if err == nil && filepath.Dir(exe) == wd {
		return "./" + filepath.Base(exe)
	}
	return exe
}

func exampleOwner(login string) string {
	if login != "" {
		return login
	}
	return "acme"
}

func shortErr(err error) string {
	s := err.Error()
	// Drop raw JSON bodies from API errors; the status line is enough here.
	if idx := strings.Index(s, ": {"); idx > 0 {
		s = s[:idx]
	}
	if len(s) > 160 {
		s = s[:160] + "..."
	}
	return s
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
