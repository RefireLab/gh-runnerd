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
)

func initCmd() *cobra.Command {
	var (
		withExamples bool
		token        string
		owner        string
		repo         string
		org          string
		scope        string
	)
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Create data directories, CA, and a starter config",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := config.Defaults()
			if d := flagStr(cmd, "data-dir"); d != "" {
				cfg.DataDir = d
			}
			if token != "" {
				cfg.GitHub.Token = token
			}
			if owner != "" {
				cfg.GitHub.Owner = owner
			}
			if repo != "" {
				cfg.GitHub.Repo = repo
			}
			if org != "" {
				cfg.GitHub.Org = org
				cfg.GitHub.Scope = "org"
			}
			if scope != "" {
				cfg.GitHub.Scope = scope
			}
			if cfg.Webhook.Secret == "" {
				buf := make([]byte, 16)
				if _, err := rand.Read(buf); err == nil {
					cfg.Webhook.Secret = hex.EncodeToString(buf)
				}
			}
			dirs := cfg.Layout()
			if err := dirs.Ensure(); err != nil {
				return err
			}
			if !tlsutil.Exists(dirs.CA) {
				ip := net.ParseIP(cfg.Network.HostIP)
				bundle, err := tlsutil.Generate(tlsutil.DefaultSANs(), []net.IP{ip})
				if err != nil {
					return err
				}
				if err := bundle.Write(dirs.CA); err != nil {
					return err
				}
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
			fmt.Fprintf(cmd.OutOrStdout(), "  1. Bake or import an Ubuntu 24.04 runner image:\n")
			fmt.Fprintf(cmd.OutOrStdout(), "       make runner-image\n")
			fmt.Fprintf(cmd.OutOrStdout(), "       # or: gh-runnerd runner-image import ./ubuntu-24.04-amd64.qcow2 --name ubuntu-24.04-amd64\n")
			if withExamples {
				fmt.Fprintf(cmd.OutOrStdout(), "  2. Pre-seed example containers (requires network):\n")
				for _, img := range []string{"alpine:3.22", "ubuntu:24.04", "node:22-bookworm", "python:3.13-slim"} {
					fmt.Fprintf(cmd.OutOrStdout(), "       gh-runnerd image pull %s\n", img)
				}
			}
			fmt.Fprintf(cmd.OutOrStdout(), "  3. gh-runnerd doctor\n")
			fmt.Fprintf(cmd.OutOrStdout(), "  4. gh-runnerd serve --config %s\n", cfgPath)
			return nil
		},
	}
	cmd.Flags().BoolVar(&withExamples, "with-examples", false, "print commands to pre-seed alpine/ubuntu/node/python images")
	cmd.Flags().StringVar(&token, "token", os.Getenv("GH_RUNNERD_GITHUB_TOKEN"), "GitHub PAT or fine-grained token")
	cmd.Flags().StringVar(&owner, "owner", "", "GitHub owner for repo scope")
	cmd.Flags().StringVar(&repo, "repo", "", "GitHub repository for repo scope")
	cmd.Flags().StringVar(&org, "org", "", "GitHub organization (sets scope=org)")
	cmd.Flags().StringVar(&scope, "scope", "", "repo or org")
	return cmd
}
