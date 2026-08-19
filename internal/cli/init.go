package cli

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"os"

	"github.com/spf13/cobra"

	"github.com/RefireLab/gh-runnerd/internal/config"
	"github.com/RefireLab/gh-runnerd/internal/tlsutil"
	"github.com/RefireLab/gh-runnerd/internal/wizard"
)

type initPreset struct {
	Token        string
	Owner        string
	Repo         string
	Org          string
	Scope        string
	WithExamples bool
}

func initCmd() *cobra.Command {
	var (
		preset         initPreset
		nonInteractive bool
	)
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Set up gh-runnerd (interactive wizard)",
		Long: "Interactive setup wizard: writes the config, installs host packages,\n" +
			"builds the runner VM image, and optionally installs a systemd service.\n" +
			"With --non-interactive (or when stdin is not a terminal) it only writes\n" +
			"the config and data directories from flags.",
		RunE: func(cmd *cobra.Command, args []string) error {
			p := wizard.New(cmd.InOrStdin(), cmd.OutOrStdout())
			if nonInteractive || !p.Interactive() {
				return runInitNonInteractive(cmd, preset)
			}
			return runInitWizard(cmd, p, preset)
		},
	}
	cmd.Flags().BoolVar(&preset.WithExamples, "with-examples", false, "print commands to pre-seed alpine/ubuntu/node/python images")
	cmd.Flags().StringVar(&preset.Token, "token", os.Getenv("GH_RUNNERD_GITHUB_TOKEN"), "GitHub PAT or fine-grained token")
	cmd.Flags().StringVar(&preset.Owner, "owner", "", "GitHub owner for repo scope")
	cmd.Flags().StringVar(&preset.Repo, "repo", "", "GitHub repository for repo scope")
	cmd.Flags().StringVar(&preset.Org, "org", "", "GitHub organization (sets scope=org)")
	cmd.Flags().StringVar(&preset.Scope, "scope", "", "repo or org")
	cmd.Flags().BoolVar(&nonInteractive, "non-interactive", false, "no questions: write config and directories from flags only")
	return cmd
}

// ensureCA generates the internal CA once; the registry TLS and every baked
// runner image depend on it.
func ensureCA(cfg config.Config) error {
	dirs := cfg.Layout()
	if tlsutil.Exists(dirs.CA) {
		return nil
	}
	ip := net.ParseIP(cfg.Network.HostIP)
	bundle, err := tlsutil.Generate(tlsutil.DefaultSANs(), []net.IP{ip})
	if err != nil {
		return err
	}
	return bundle.Write(dirs.CA)
}

func randomSecret() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return ""
	}
	return hex.EncodeToString(buf)
}

func runInitNonInteractive(cmd *cobra.Command, preset initPreset) error {
	cfg := config.Defaults()
	if d := flagStr(cmd, "data-dir"); d != "" {
		cfg.DataDir = d
	}
	if preset.Token != "" {
		cfg.GitHub.Token = preset.Token
	}
	if preset.Owner != "" {
		cfg.GitHub.Owner = preset.Owner
	}
	if preset.Repo != "" {
		cfg.GitHub.Repo = preset.Repo
	}
	if preset.Org != "" {
		cfg.GitHub.Org = preset.Org
		cfg.GitHub.Scope = "org"
	}
	if preset.Scope != "" {
		cfg.GitHub.Scope = preset.Scope
	}
	if cfg.Webhook.Secret == "" {
		cfg.Webhook.Secret = randomSecret()
	}
	dirs := cfg.Layout()
	if err := dirs.Ensure(); err != nil {
		return err
	}
	if err := ensureCA(cfg); err != nil {
		return err
	}
	cfgPath := flagStr(cmd, "config")
	if cfgPath == "" {
		if os.Geteuid() == 0 {
			cfgPath = "/etc/gh-runnerd/config.toml"
		} else {
			cfgPath = "gh-runnerd.toml"
		}
	}
	if err := config.WriteFile(cfgPath, cfg); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Initialized gh-runnerd\n")
	fmt.Fprintf(cmd.OutOrStdout(), "  config:   %s\n", cfgPath)
	fmt.Fprintf(cmd.OutOrStdout(), "  data dir: %s\n", dirs.Root)
	fmt.Fprintf(cmd.OutOrStdout(), "  CA:       %s\n", dirs.CA)
	fmt.Fprintf(cmd.OutOrStdout(), "\nNext:\n")
	fmt.Fprintf(cmd.OutOrStdout(), "  1. Build the runner VM image: sudo gh-runnerd runner-image bake\n")
	if preset.WithExamples {
		fmt.Fprintf(cmd.OutOrStdout(), "  2. Pre-seed example containers (requires network):\n")
		for _, img := range []string{"alpine:3.22", "ubuntu:24.04", "node:22-bookworm", "python:3.13-slim"} {
			fmt.Fprintf(cmd.OutOrStdout(), "       gh-runnerd image pull %s\n", img)
		}
	}
	fmt.Fprintf(cmd.OutOrStdout(), "  3. gh-runnerd doctor\n")
	fmt.Fprintf(cmd.OutOrStdout(), "  4. sudo gh-runnerd serve --config %s\n", cfgPath)
	return nil
}
