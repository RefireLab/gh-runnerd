package cli

import (
	"errors"
	"fmt"
	"io"
	"io/fs"

	"github.com/spf13/cobra"

	"github.com/RefireLab/gh-runnerd/internal/config"
	"github.com/RefireLab/gh-runnerd/internal/version"
)

func Root() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "gh-runnerd",
		Short: "Ephemeral Ubuntu VM GitHub Actions runners",
		Long: fmt.Sprintf(`gh-runnerd %s — ephemeral Ubuntu VM GitHub Actions runners

by RefireLab
  https://refirelab.com/
  https://github.com/RefireLab/gh-runnerd`, version.Version),
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       version.Version,
	}
	cmd.PersistentFlags().String("config", "", "path to config.toml")
	cmd.PersistentFlags().String("data-dir", "", "override data directory")
	cmd.AddCommand(initCmd())
	cmd.AddCommand(doctorCmd())
	cmd.AddCommand(serveCmd())
	cmd.AddCommand(statusCmd())
	cmd.AddCommand(runnersCmd())
	cmd.AddCommand(imageCmd())
	cmd.AddCommand(runnerImageCmd())
	return cmd
}

func Execute() error {
	return Root().Execute()
}

func flagStr(cmd *cobra.Command, name string) string {
	v, _ := cmd.Flags().GetString(name)
	return v
}

func loadConfigOptional(cmd *cobra.Command) (config.Config, error) {
	cfg, _, err := loadConfigOptionalPath(cmd)
	return cfg, err
}

// loadConfigOptionalPath is loadConfigOptional plus the path of the config
// file that was loaded — empty when none was found and the built-in
// defaults are in use.
func loadConfigOptionalPath(cmd *cobra.Command) (config.Config, string, error) {
	cfg := config.Defaults()
	path := flagStr(cmd, "config")
	if path == "" {
		if found, err := config.Find(""); err == nil {
			path = found
		}
	}
	if path != "" {
		loaded, err := config.Load(path)
		if err != nil {
			if errors.Is(err, fs.ErrPermission) {
				return config.Config{}, "", fmt.Errorf("config %s is not readable by this user — run with sudo: %w", path, err)
			}
			return config.Config{}, "", err
		}
		cfg = loaded
	}
	if d := flagStr(cmd, "data-dir"); d != "" {
		cfg.DataDir = d
	}
	return cfg, path, nil
}

func loadConfigRequired(cmd *cobra.Command) (config.Config, string, error) {
	path := flagStr(cmd, "config")
	var err error
	if path == "" {
		path, err = config.Find("")
		if err != nil {
			return config.Config{}, "", err
		}
	}
	cfg, err := config.Load(path)
	if err != nil {
		return config.Config{}, "", err
	}
	if d := flagStr(cmd, "data-dir"); d != "" {
		cfg.DataDir = d
	}
	return cfg, path, nil
}

func printImport(out io.Writer, name, tag, digest, ref string) {
	fmt.Fprintf(out, "Imported image\n\n")
	fmt.Fprintf(out, "Name:       %s\n", name)
	fmt.Fprintf(out, "Tag:        %s\n", tag)
	fmt.Fprintf(out, "Digest:     %s\n", digest)
	fmt.Fprintf(out, "Reference:  %s\n", ref)
}
