package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/RefireLab/gh-runnerd/internal/bake"
	"github.com/RefireLab/gh-runnerd/internal/config"
	"github.com/RefireLab/gh-runnerd/internal/images"
	"github.com/RefireLab/gh-runnerd/internal/registry"
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
	ExtraRuns     []string
	Fresh         bool
	Verbose       bool
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
	name := o.Name
	if name == "" {
		name = images.DefaultName()
	}
	tmpOut := filepath.Join(dirs.Runner, "."+name+".qcow2.tmp")
	defer os.Remove(tmpOut)
	err = bake.Run(ctx, bake.Options{
		Name:          name,
		RunnerVersion: o.RunnerVersion,
		GuestBinary:   o.Guest,
		CACertPEM:     caPEM,
		OutPath:       tmpOut,
		CacheDir:      filepath.Join(dirs.Cache, "bake"),
		DiskGB:        cfg.DiskGB(),
		MemoryMB:      cfg.MemoryMB(),
		CPUs:          cfg.VM.CPUs,
		ExtraRuns:     o.ExtraRuns,
		Fresh:         o.Fresh,
		Verbose:       o.Verbose,
		Out:           out,
	})
	if err != nil {
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
	fmt.Fprintf(out, "Runner image ready\n  name:   %s (active)\n  path:   %s\n  sha256: %s\n", img.Name, img.Path, img.SHA256)
	return nil
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
		Short: "Build the Ubuntu 24.04 runner VM image right here (download, install, activate)",
		Long: "Downloads the official Ubuntu 24.04 cloud image, boots it once under\n" +
			"QEMU/KVM to install Docker, the GitHub Actions runner, and the gh-runnerd\n" +
			"guest agent, then imports and activates the result. Needs internet,\n" +
			"/dev/kvm, and the qemu/cloud-image-utils packages.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfigOptional(cmd)
			if err != nil {
				return err
			}
			return bakeAndInstall(cmd.Context(), cfg, cmd.OutOrStdout(), o)
		},
	}
	cmd.Flags().StringVar(&o.Name, "name", "", "template name (default ubuntu-24.04-<arch>)")
	cmd.Flags().StringVar(&o.RunnerVersion, "runner-version", "", "GitHub Actions runner version (default "+bake.DefaultRunnerVersion+")")
	cmd.Flags().StringVar(&o.Guest, "guest", "", "path to the gh-runnerd-guest binary")
	cmd.Flags().BoolVar(&o.Fresh, "fresh", false, "re-download the Ubuntu cloud image instead of using the cache")
	cmd.Flags().BoolVar(&o.Verbose, "verbose", false, "stream the VM console while baking")
	return cmd
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
		yes, err := p.AskYesNo("Build the standard Ubuntu 24.04 image instead? (~600 MB download, 10-20 min)", true)
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
		Short: "Rebuild the shipped Ubuntu 24.04 runner template from the newest cloud image",
		Args:  cobra.NoArgs,
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
	return cmd
}
