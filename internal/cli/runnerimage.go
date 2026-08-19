package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/RefireLab/gh-runnerd/internal/images"
	"github.com/RefireLab/gh-runnerd/internal/registry"
)

func runnerImageCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "runner-image",
		Short: "Manage bootable Ubuntu runner VM images",
	}
	cmd.AddCommand(runnerListCmd())
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
				fmt.Fprintln(cmd.OutOrStdout(), "(no runner images — bake ubuntu-24.04 with make runner-image)")
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
	cmd := &cobra.Command{
		Use:   "import <qcow2>",
		Short: "Import a bootable Ubuntu qcow2 runner image",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if name == "" {
				return fmt.Errorf("--name is required")
			}
			cat, err := catalog(cmd)
			if err != nil {
				return err
			}
			img, err := cat.Import(args[0], name)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Imported runner image %s\n  path:   %s\n  sha256: %s\n", img.Name, img.Path, img.SHA256)
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "template name, e.g. ubuntu-24.04-amd64")
	return cmd
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
	var name string
	var bakeDocker bool
	cmd := &cobra.Command{
		Use:   "build <Runnerfile>",
		Short: "Build a custom Ubuntu runner image from a Runnerfile",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if name == "" {
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
			script := filepath.Join("images", "runner", "bake.sh")
			if _, err := os.Stat(script); err != nil {
				return fmt.Errorf("Runnerfile RUN steps require %s (base Ubuntu image bake)", script)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "FROM %s\n", rf.From)
			for _, run := range rf.Runs {
				fmt.Fprintf(cmd.OutOrStdout(), "RUN %s\n", run)
			}
			env := os.Environ()
			env = append(env,
				"GH_RUNNERD_RUNNERFILE_NAME="+name,
				"GH_RUNNERD_RUNNERFILE_FROM="+rf.From,
				"GH_RUNNERD_RUNNERFILE_RUNS="+joinLines(rf.Runs),
			)
			if bakeDocker {
				env = append(env, "GH_RUNNERD_BAKE_DOCKER=1")
			}
			c := exec.Command(script, "--from-runnerfile")
			c.Env = env
			c.Stdout = cmd.OutOrStdout()
			c.Stderr = cmd.ErrOrStderr()
			c.Dir = findRepoRoot()
			if err := c.Run(); err != nil {
				return fmt.Errorf("bake from Runnerfile: %w (PRELOAD images were still imported into the local registry)", err)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "output template name")
	cmd.Flags().BoolVar(&bakeDocker, "bake-docker", false, "also pre-seed Docker graph inside the qcow2")
	return cmd
}

func runnerUpdateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "update",
		Short: "Rebuild/download the shipped Ubuntu 24.04 runner template",
		RunE: func(cmd *cobra.Command, args []string) error {
			script := filepath.Join(findRepoRoot(), "images", "runner", "bake.sh")
			c := exec.Command(script)
			c.Stdout = cmd.OutOrStdout()
			c.Stderr = cmd.ErrOrStderr()
			return c.Run()
		},
	}
}

func joinLines(in []string) string {
	out := ""
	for i, s := range in {
		if i > 0 {
			out += "\n"
		}
		out += s
	}
	return out
}

func findRepoRoot() string {
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	dir := wd
	for i := 0; i < 6; i++ {
		if _, err := os.Stat(filepath.Join(dir, "images", "runner", "bake.sh")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return wd
}
