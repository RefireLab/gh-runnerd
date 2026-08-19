package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	"github.com/RefireLab/gh-runnerd/internal/bake"
	"github.com/RefireLab/gh-runnerd/internal/config"
	"github.com/RefireLab/gh-runnerd/internal/ghapi"
	"github.com/RefireLab/gh-runnerd/internal/host"
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
	bakeOK := offerBake(cmd, p, opsCfg, kvmErr)

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
	p.Say("  # .github/workflows/ci.yml")
	p.Say("  jobs:")
	p.Say("    build:")
	p.Say("      runs-on: gh-runnerd")
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

	p.Say("")
	maxConc, err := p.AskInt("How many jobs may run at the same time? (each uses ~4 GB RAM while running)", 2)
	if err != nil {
		return config.Config{}, err
	}
	cfg.Pool.MaxConcurrent = maxConc
	warm, err := p.AskYesNo("Keep one VM warmed up between jobs? (jobs start in seconds, costs ~4 GB RAM all the time)", false)
	if err != nil {
		return config.Config{}, err
	}
	if warm {
		cfg.Pool.MinIdle = 1
	}
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
	p.Say("gh-runnerd needs one token to register runners. Create it here:")
	p.Say("  https://github.com/settings/personal-access-tokens/new")
	p.Say("  1. Repository access: pick the repo(s) that will use these runners")
	p.Say("  2. Repository permissions -> Administration: Read and write")
	p.Say("  (or a classic token with the \"repo\" scope)")
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

func verifyRunnerTarget(ctx context.Context, p *wizard.Prompter, cfg *config.Config) error {
	if cfg.GitHub.Token == "" {
		return nil
	}
	if err := ghapi.New(cfg.GitHub).CheckRunnerAccess(ctx); err != nil {
		p.Say("[!!] the token cannot manage runners there (%v)", shortErr(err))
		p.Say("     the token needs: Administration: Read and write on that repo/org")
		return err
	}
	target := cfg.GitHub.Owner + "/" + cfg.GitHub.Repo
	if strings.EqualFold(cfg.GitHub.Scope, "org") {
		target = "organization " + cfg.GitHub.Org
	}
	p.Say("[ok] runner access confirmed for %s", target)
	return nil
}

// offerBake asks to build the VM image now and reports whether an image is
// ready afterwards.
func offerBake(cmd *cobra.Command, p *wizard.Prompter, opsCfg config.Config, kvmErr error) bool {
	if hasActiveRunnerImage(opsCfg) {
		p.Say("[ok] runner VM image already present")
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
	yes, err := p.AskYesNo("Build the runner VM image now? (one time; ~600 MB download, 10-20 minutes)", true)
	if err != nil || !yes {
		p.Say("later, run: sudo gh-runnerd runner-image bake")
		return false
	}
	if err := bakeAndInstall(cmd.Context(), opsCfg, cmd.OutOrStdout(), bakeOverrides{}); err != nil {
		p.Say("[!!] image build failed: %v", err)
		p.Say("     fix the issue and re-run: sudo gh-runnerd runner-image bake")
		return false
	}
	return true
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
