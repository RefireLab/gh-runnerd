package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/RefireLab/gh-runnerd/internal/bake"
	"github.com/RefireLab/gh-runnerd/internal/config"
	"github.com/RefireLab/gh-runnerd/internal/images"
	"github.com/RefireLab/gh-runnerd/internal/registry"
	"github.com/RefireLab/gh-runnerd/internal/runnerimages"
	"github.com/RefireLab/gh-runnerd/internal/wizard"
)

func runnerImageCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "runner-image",
		Short: "Manage bootable Ubuntu runner VM images",
	}
	cmd.AddCommand(runnerListCmd())
	cmd.AddCommand(runnerBakeCmd())
	cmd.AddCommand(runnerImportCmd())
	cmd.AddCommand(runnerValidateCmd())
	cmd.AddCommand(runnerActivateCmd())
	cmd.AddCommand(runnerBuildCmd())
	cmd.AddCommand(runnerUpdateCmd())
	cmd.AddCommand(runnerAvailableCmd())
	return cmd
}

func catalog(cmd *cobra.Command) (images.Catalog, error) {
	cfg, err := loadConfigOptional(cmd)
	if err != nil {
		return images.Catalog{}, err
	}
	if err := cfg.Layout().Ensure(); err != nil {
		return images.Catalog{}, err
	}
	return images.Catalog{Dir: cfg.Layout().Runner}, nil
}

// bakeOverrides are the optional knobs shared by bake/update/build.
type bakeOverrides struct {
	Name          string
	RunnerVersion string
	Guest         string
	Flavor        string // minimal | essential | full ("" = config)
	Image         string // upstream family, e.g. ubuntu-24.04 ("" = config)
	UpstreamRef   string // release tag or branch ("" = newest release)
	SkipScripts   []string
	OnlyScripts   []string
	ExtraRuns     []string
	Fresh         bool
	Verbose       bool
	Timeout       time.Duration
}

func addHostedFlags(cmd *cobra.Command, o *bakeOverrides) {
	cmd.Flags().StringVar(&o.Flavor, "flavor", "", "image contents: minimal, essential, or full (default from config, else minimal)")
	cmd.Flags().StringVar(&o.Image, "image", "", "upstream image to mirror, e.g. ubuntu-24.04 (see: gh-runnerd runner-image available)")
	cmd.Flags().StringVar(&o.UpstreamRef, "upstream-ref", "", "pin an actions/runner-images release tag or branch (default: newest release)")
	cmd.Flags().StringSliceVar(&o.SkipScripts, "skip-scripts", nil, "upstream build scripts to skip, by file name")
	cmd.Flags().StringSliceVar(&o.OnlyScripts, "only-scripts", nil, "run only these upstream build scripts (debugging)")
}

// bakeAndInstall builds the golden image with the config's VM settings,
// then imports and activates it in one go.
func bakeAndInstall(ctx context.Context, cfg config.Config, out io.Writer, o bakeOverrides) error {
	dirs := cfg.Layout()
	if err := dirs.Ensure(); err != nil {
		return err
	}
	if err := ensureCA(cfg); err != nil {
		return err
	}
	caPEM, err := os.ReadFile(filepath.Join(dirs.CA, "ca.crt"))
	if err != nil {
		return fmt.Errorf("read CA (run gh-runnerd init first): %w", err)
	}

	flavor, err := runnerimages.ParseFlavor(firstNonEmptyStr(o.Flavor, cfg.Image.Flavor))
	if err != nil {
		return err
	}
	family := firstNonEmptyStr(o.Image, cfg.Image.Upstream, "ubuntu-24.04")
	if !runnerimages.ValidFamily(family) {
		return fmt.Errorf("unknown image %q — list them with: gh-runnerd runner-image available", family)
	}
	if o.Image != "" && o.Flavor == "" && flavor == runnerimages.FlavorMinimal {
		// --image without a flavor means the user wants the upstream
		// software, not just a different base version.
		flavor = runnerimages.FlavorEssential
	}

	opts := bake.Options{
		Name:          o.Name,
		UbuntuVersion: runnerimages.UbuntuVersion(family),
		RunnerVersion: o.RunnerVersion,
		GuestBinary:   o.Guest,
		CACertPEM:     caPEM,
		HostIP:        cfg.Network.HostIP,
		CacheDir:      filepath.Join(dirs.Cache, "bake"),
		DiskGB:        cfg.DiskGB(),
		MemoryMB:      cfg.MemoryMB(),
		CPUs:          cfg.VM.CPUs,
		MinFreeGB:     runnerimages.EstimatedDataGB(flavor),
		ExtraRuns:     o.ExtraRuns,
		Fresh:         o.Fresh,
		Verbose:       o.Verbose,
		Timeout:       o.Timeout,
		Out:           out,
	}

	if flavor != runnerimages.FlavorMinimal {
		say := func(format string, args ...any) { fmt.Fprintf(out, format+"\n", args...) }
		ref := firstNonEmptyStr(o.UpstreamRef, cfg.Image.UpstreamRef)
		if ref == "" {
			ref, err = runnerimages.LatestReleaseTag(ctx, cfg.GitHub.Token, family, hostArch())
			if err != nil {
				say("!! could not resolve the newest %s release (%v) — building from the main branch", runnerimages.Repo, err)
				ref = "main"
			}
		}
		root, err := runnerimages.Fetch(ctx, filepath.Join(dirs.Cache, "runner-images"), ref, o.Fresh, say)
		if err != nil {
			return err
		}
		plan, err := runnerimages.BuildPlan(root, family, hostArch(), flavor, ref, o.SkipScripts, o.OnlyScripts)
		if err != nil {
			return err
		}
		say(">> %s %s: %d upstream build scripts from %s@%s", family, flavor, plan.ScriptCount(), runnerimages.Repo, ref)
		if len(plan.Dropped) > 0 {
			say("   (not in this release, skipped: %s)", strings.Join(plan.Dropped, ", "))
		}
		opts.HostedTree = root
		opts.HostedSetup = plan.SetupScript()
		if rec := runnerimages.RecommendedDiskGB(flavor); opts.DiskGB < rec {
			say(">> raising the image disk to %d GB for the %s flavor (vm.disk %s is too small for it)", rec, flavor, cfg.VM.Disk)
			opts.DiskGB = rec
		}
		if rec := runnerimages.RecommendedMemoryMB(flavor); opts.MemoryMB < rec {
			opts.MemoryMB = rec
		}
		if opts.Timeout <= 0 {
			opts.Timeout = runnerimages.RecommendedTimeout(flavor)
		}
	}

	name := opts.Name
	if name == "" {
		name = "ubuntu-" + opts.UbuntuVersion + "-" + hostArch()
	}
	opts.Name = name
	tmpOut := filepath.Join(dirs.Runner, "."+name+".qcow2.tmp")
	defer os.Remove(tmpOut)
	opts.OutPath = tmpOut
	if err := bake.Run(ctx, opts); err != nil {
		return err
	}
	cat := images.Catalog{Dir: dirs.Runner}
	img, err := cat.Import(tmpOut, name)
	if err != nil {
		return err
	}
	if err := cat.Activate(name); err != nil {
		return err
	}
	fmt.Fprintf(out, "Runner image ready\n  name:   %s (active)\n  flavor: %s\n  path:   %s\n  sha256: %s\n", img.Name, flavor, img.Path, img.SHA256)
	if family != "ubuntu-24.04" && cfg.VM.Template == "ubuntu-24.04" {
		fmt.Fprintf(out, "note: set vm.template = %q in the config so restarts keep using this image\n", family)
	}
	return nil
}

func hostArch() string {
	return runtime.GOARCH
}

func firstNonEmptyStr(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// hasActiveRunnerImage reports whether an activated template already exists.
func hasActiveRunnerImage(cfg config.Config) bool {
	cat := images.Catalog{Dir: cfg.Layout().Runner}
	img, err := cat.Active()
	if err != nil {
		return false
	}
	_, err = os.Stat(img.Path)
	return err == nil
}

func runnerBakeCmd() *cobra.Command {
	var o bakeOverrides
	cmd := &cobra.Command{
		Use:   "bake",
		Short: "Build the runner VM image right here (download, install, activate)",
		Long: "Downloads the official Ubuntu cloud image, boots it once under QEMU/KVM\n" +
			"to install Docker, the GitHub Actions runner, and the gh-runnerd guest\n" +
			"agent, then imports and activates the result. With --flavor essential or\n" +
			"--flavor full it also runs GitHub's own actions/runner-images build\n" +
			"scripts, so the VM carries the same software as GitHub-hosted runners\n" +
			"(git, gh, node, python, ...). Needs internet, /dev/kvm, and the\n" +
			"qemu/cloud-image-utils packages.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfigOptional(cmd)
			if err != nil {
				return err
			}
			return bakeAndInstall(cmd.Context(), cfg, cmd.OutOrStdout(), o)
		},
	}
	cmd.Flags().StringVar(&o.Name, "name", "", "template name (default <image>-<arch>)")
	cmd.Flags().StringVar(&o.RunnerVersion, "runner-version", "", "GitHub Actions runner version (default "+bake.DefaultRunnerVersion+")")
	cmd.Flags().StringVar(&o.Guest, "guest", "", "path to the gh-runnerd-guest binary")
	cmd.Flags().BoolVar(&o.Fresh, "fresh", false, "re-download the Ubuntu cloud image and build scripts instead of using the cache")
	cmd.Flags().BoolVar(&o.Verbose, "verbose", false, "stream the VM console while baking")
	cmd.Flags().DurationVar(&o.Timeout, "timeout", 0, "abort the build after this long (default 45m, essential 3h, full 14h)")
	addHostedFlags(cmd, &o)
	return cmd
}

func runnerAvailableCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "available",
		Short: "List upstream images (actions/runner-images) you can bake",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, _ := loadConfigOptional(cmd)
			out := cmd.OutOrStdout()
			tags, err := runnerimages.LatestReleases(cmd.Context(), cfg.GitHub.Token, hostArch())
			if err != nil {
				fmt.Fprintf(out, "(release lookup failed: %v)\n", err)
				tags = map[string]string{}
			}
			fmt.Fprintf(out, "%-14s %-28s %s\n", "IMAGE", "LATEST RELEASE", "BAKE WITH")
			for _, family := range runnerimages.KnownFamilies() {
				tag := tags[family]
				if tag == "" {
					tag = "(none for " + hostArch() + ")"
				}
				fmt.Fprintf(out, "%-14s %-28s sudo gh-runnerd runner-image bake --image %s --flavor essential\n", family, tag, family)
			}
			fmt.Fprintln(out, "\nFlavors:")
			fmt.Fprintln(out, "  minimal    Docker + runner only (~2 GB image, 3-5 min)")
			fmt.Fprintln(out, "  essential  + the everyday tools from GitHub's images: git, gh, node,")
			fmt.Fprintln(out, "             python, cmake, docker plugins, ... (~10 GB image, ~10 min)")
			fmt.Fprintln(out, "  full       everything GitHub's ubuntu-latest ships — browsers, JDKs,")
			fmt.Fprintln(out, "             Android SDK, CodeQL, toolcache... (~60-80 GB image, needs")
			fmt.Fprintln(out, "             ~130 GB free disk, ~30 min)")
			return nil
		},
	}
}

func runnerListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List imported runner VM images",
		RunE: func(cmd *cobra.Command, args []string) error {
			cat, err := catalog(cmd)
			if err != nil {
				return err
			}
			list, err := cat.List()
			if err != nil {
				return err
			}
			if len(list) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "(no runner images — build one with: gh-runnerd runner-image bake)")
				return nil
			}
			for _, img := range list {
				mark := " "
				if img.Active {
					mark = "*"
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s %s\t%s\t%s\n", mark, img.Name, img.SHA256, img.Path)
			}
			return nil
		},
	}
}

func runnerImportCmd() *cobra.Command {
	var name string
	var activate bool
	cmd := &cobra.Command{
		Use:   "import [qcow2]",
		Short: "Import a bootable Ubuntu qcow2 runner image (wizard when run bare)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cat, err := catalog(cmd)
			if err != nil {
				return err
			}
			p := wizard.New(cmd.InOrStdin(), cmd.OutOrStdout())
			interactive := len(args) == 0
			var src string
			if len(args) == 1 {
				src = args[0]
			} else {
				if !p.Interactive() {
					return fmt.Errorf("usage: gh-runnerd runner-image import <file.qcow2> (or run in a terminal for the wizard)")
				}
				src, err = pickQcow2(cmd, p)
				if err != nil || src == "" {
					return err
				}
			}
			if name == "" {
				def := strings.TrimSuffix(filepath.Base(src), ".qcow2")
				if interactive && p.Interactive() {
					name, err = p.Ask("Name for this image", def)
					if err != nil {
						return err
					}
				} else {
					name = def
				}
			}
			img, err := cat.Import(src, name)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Imported runner image %s\n  path:   %s\n  sha256: %s\n", img.Name, img.Path, img.SHA256)
			makeActive := activate
			if interactive && p.Interactive() {
				makeActive, err = p.AskYesNo("Use it for new runner VMs (activate)?", true)
				if err != nil {
					return err
				}
			}
			if makeActive {
				if err := cat.Activate(name); err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "activated %s\n", name)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "template name (default: file name)")
	cmd.Flags().BoolVar(&activate, "activate", false, "activate the image after import")
	return cmd
}

// pickQcow2 offers qcow2 files from the current directory, or a fresh bake
// when there are none.
func pickQcow2(cmd *cobra.Command, p *wizard.Prompter) (string, error) {
	matches, _ := filepath.Glob("*.qcow2")
	sort.Strings(matches)
	if len(matches) == 0 {
		p.Say("No .qcow2 files in this folder.")
		yes, err := p.AskYesNo("Build the standard Ubuntu 24.04 image instead? (~600 MB download, 3-5 min)", true)
		if err != nil || !yes {
			p.Say("nothing imported — put a .qcow2 file here or run: gh-runnerd runner-image bake")
			return "", err
		}
		cfg, err := loadConfigOptional(cmd)
		if err != nil {
			return "", err
		}
		return "", bakeAndInstall(cmd.Context(), cfg, cmd.OutOrStdout(), bakeOverrides{})
	}
	idx, err := p.Select("Which image do you want to import?", matches, 0)
	if err != nil {
		return "", err
	}
	return matches[idx], nil
}

func runnerValidateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "validate <name>",
		Short: "Validate a runner image checksum and qcow2 metadata",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cat, err := catalog(cmd)
			if err != nil {
				return err
			}
			if err := cat.Validate(args[0]); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "ok: %s\n", args[0])
			return nil
		},
	}
}

func runnerActivateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "activate <name>",
		Short: "Use this runner image for new VMs",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cat, err := catalog(cmd)
			if err != nil {
				return err
			}
			if err := cat.Activate(args[0]); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "activated %s\n", args[0])
			return nil
		},
	}
}

func runnerBuildCmd() *cobra.Command {
	var o bakeOverrides
	cmd := &cobra.Command{
		Use:   "build <Runnerfile>",
		Short: "Build a custom Ubuntu runner image from a Runnerfile",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if o.Name == "" {
				return fmt.Errorf("--name is required")
			}
			rf, err := images.ParseRunnerfileFile(args[0])
			if err != nil {
				return err
			}
			cfg, err := loadConfigOptional(cmd)
			if err != nil {
				return err
			}
			if err := cfg.Layout().Ensure(); err != nil {
				return err
			}
			s := registry.OpenPinned(cfg.Layout(), cfg.PinnedQuotaBytes())
			for _, img := range rf.Preloads {
				fmt.Fprintf(cmd.OutOrStdout(), "PRELOAD %s\n", img)
				parsed, err := registry.ParseLocalRef(img)
				if err != nil {
					return err
				}
				if _, err := s.Pull(cmd.Context(), img, parsed.Name, parsed.Tag, "", registry.UpstreamAuth{
					DockerHubUser:  cfg.Registry.DockerHubUsername,
					DockerHubToken: cfg.Registry.DockerHubToken,
				}); err != nil {
					return fmt.Errorf("preload %s: %w", img, err)
				}
			}
			fmt.Fprintf(cmd.OutOrStdout(), "FROM %s\n", rf.From)
			for _, run := range rf.Runs {
				fmt.Fprintf(cmd.OutOrStdout(), "RUN %s\n", run)
			}
			o.ExtraRuns = rf.Runs
			if err := bakeAndInstall(cmd.Context(), cfg, cmd.OutOrStdout(), o); err != nil {
				return fmt.Errorf("bake from Runnerfile: %w (PRELOAD images were still imported into the local registry)", err)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&o.Name, "name", "", "output template name")
	cmd.Flags().StringVar(&o.RunnerVersion, "runner-version", "", "GitHub Actions runner version")
	cmd.Flags().StringVar(&o.Guest, "guest", "", "path to the gh-runnerd-guest binary")
	cmd.Flags().BoolVar(&o.Fresh, "fresh", false, "re-download the Ubuntu cloud image")
	cmd.Flags().BoolVar(&o.Verbose, "verbose", false, "stream the VM console while baking")
	return cmd
}

func runnerUpdateCmd() *cobra.Command {
	var o bakeOverrides
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Rebuild the runner template from the newest cloud image and build scripts",
		Long: "Re-bakes the runner image with the flavor and upstream image recorded in\n" +
			"the config ([image] section), refreshing the Ubuntu cloud image, the\n" +
			"GitHub Actions runner, and (for essential/full) the newest\n" +
			"actions/runner-images release.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfigOptional(cmd)
			if err != nil {
				return err
			}
			o.Fresh = true
			return bakeAndInstall(cmd.Context(), cfg, cmd.OutOrStdout(), o)
		},
	}
	cmd.Flags().StringVar(&o.RunnerVersion, "runner-version", "", "GitHub Actions runner version")
	cmd.Flags().StringVar(&o.Guest, "guest", "", "path to the gh-runnerd-guest binary")
	cmd.Flags().BoolVar(&o.Verbose, "verbose", false, "stream the VM console while baking")
	cmd.Flags().DurationVar(&o.Timeout, "timeout", 0, "abort the build after this long (default 45m, essential 3h, full 14h)")
	addHostedFlags(cmd, &o)
	return cmd
}
