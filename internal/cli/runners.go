package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/RefireLab/gh-runnerd/internal/config"
	"github.com/RefireLab/gh-runnerd/internal/ghapi"
)

func runnersCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "runners",
		Short: "Inspect and clean up runner registrations in GitHub",
	}
	cmd.AddCommand(runnersListCmd())
	cmd.AddCommand(runnersCleanupCmd())
	return cmd
}

func runnersListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List the self-hosted runners registered in GitHub",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, _, err := loadConfigRequired(cmd)
			if err != nil {
				return err
			}
			runners, err := ghapi.New(cfg.GitHub).ListRunners(cmd.Context())
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if len(runners) == 0 {
				fmt.Fprintln(out, "No runners registered.")
				return nil
			}
			mine := 0
			w := tabwriter.NewWriter(out, 2, 4, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tNAME\tSTATUS\tBUSY\tLABELS")
			for _, r := range runners {
				name := r.Name
				if strings.HasPrefix(r.Name, cfg.Runner.NamePrefix+"-") {
					name += " *"
					mine++
				}
				fmt.Fprintf(w, "%d\t%s\t%s\t%v\t%s\n", r.ID, name, r.Status, r.Busy, labelNames(r))
			}
			_ = w.Flush()
			fmt.Fprintf(out, "\n%d runners, %d created by gh-runnerd (* = name prefix %q)\n", len(runners), mine, cfg.Runner.NamePrefix)
			return nil
		},
	}
}

func runnersCleanupCmd() *cobra.Command {
	var (
		prefix      string
		all         bool
		includeIdle bool
		dryRun      bool
		yes         bool
	)
	cmd := &cobra.Command{
		Use:   "cleanup",
		Short: "Remove stale runner registrations from GitHub",
		Long: `Remove stale runner registrations from GitHub.

By default only Offline runners whose name starts with runner.name_prefix
from the config are removed — the garbage a crashed VM or a killed daemon
leaves behind. Busy runners are never touched.

--idle also removes runners GitHub still shows as Idle: a runner whose
host died keeps showing Idle for several minutes until GitHub notices the
broken connection. Do not use it while a daemon with the same prefix is
running on another host — its warm idle runners would be deregistered too.
Runners of a daemon running on this host are detected and skipped.

--all ignores the prefix and matches every runner in the configured scope,
including manually registered hosts; removed ones would need to be
re-registered by hand, so double-check the list it prints (or --dry-run
first) before confirming.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, _, err := loadConfigRequired(cmd)
			if err != nil {
				return err
			}
			if all && cmd.Flags().Changed("prefix") {
				return fmt.Errorf("--all and --prefix are mutually exclusive")
			}
			match := cfg.Runner.NamePrefix + "-"
			if cmd.Flags().Changed("prefix") {
				match = prefix
			}
			if all {
				match = ""
			}
			if match == "" && !all {
				return fmt.Errorf("an empty --prefix would match every runner; pass --all if that is what you want")
			}
			filter := ghapi.StaleFilter{Prefix: match, IncludeIdle: includeIdle, Skip: liveVMNames(cfg)}
			client := ghapi.New(cfg.GitHub)
			runners, err := client.ListRunners(cmd.Context())
			if err != nil {
				return err
			}
			stale := ghapi.SelectStale(runners, filter)
			out := cmd.OutOrStdout()
			if len(stale) == 0 {
				fmt.Fprintln(out, "Nothing to clean up.")
				if !includeIdle && len(ghapi.SelectStale(runners, ghapi.StaleFilter{Prefix: match, IncludeIdle: true, Skip: filter.Skip})) > 0 {
					fmt.Fprintln(out, "There are Idle runners that would match; add --idle to remove dead runners GitHub has not marked Offline yet.")
				}
				return nil
			}

			w := tabwriter.NewWriter(out, 2, 4, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tNAME\tSTATUS")
			for _, r := range stale {
				fmt.Fprintf(w, "%d\t%s\t%s\n", r.ID, r.Name, r.Status)
			}
			_ = w.Flush()
			if dryRun {
				fmt.Fprintf(out, "\nDry run: %d runner registration(s) would be removed.\n", len(stale))
				return nil
			}
			if !yes {
				if all {
					fmt.Fprintln(out, "\nWARNING: --all matches every runner in the scope, including manually registered hosts; removed ones must be re-registered by hand.")
				}
				fmt.Fprintf(out, "\nRemove %d runner registration(s) from GitHub? [y/N]: ", len(stale))
				line, _ := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
				switch strings.ToLower(strings.TrimSpace(line)) {
				case "y", "yes":
				default:
					return fmt.Errorf("aborted (pass --yes to skip the confirmation)")
				}
			}

			// Re-list inside RemoveStale so a runner that connected or took
			// a job since the preview is re-checked and left alone.
			removed, failed, err := client.RemoveStale(cmd.Context(), filter)
			for _, r := range removed {
				fmt.Fprintf(out, "removed %s (id %d)\n", r.Name, r.ID)
			}
			for _, r := range failed {
				fmt.Fprintf(out, "FAILED  %s (id %d)\n", r.Name, r.ID)
			}
			fmt.Fprintf(out, "Removed %d runner registration(s).\n", len(removed))
			return err
		},
	}
	cmd.Flags().StringVar(&prefix, "prefix", "", "only runners whose name starts with this (default: runner.name_prefix from the config)")
	cmd.Flags().BoolVar(&all, "all", false, "ignore the prefix and match every runner in the scope")
	cmd.Flags().BoolVar(&includeIdle, "idle", false, "also remove Idle (online, not busy) runners — dead ones linger Idle until GitHub notices")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "only show what would be removed")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	return cmd
}

func labelNames(r ghapi.Runner) string {
	names := make([]string, 0, len(r.Labels))
	for _, l := range r.Labels {
		names = append(names, l.Name)
	}
	return strings.Join(names, ",")
}

// liveVMNames returns the VM names of a daemon running on this host, read
// from its status heartbeat (written every 3s). A missing or stale
// heartbeat yields nil: no daemon, nothing to protect. Protecting live
// names keeps cleanup from deregistering a runner that is starting right
// now — a VM shows Offline for a moment between JIT generation and the
// runner process connecting.
func liveVMNames(cfg config.Config) map[string]bool {
	raw, err := os.ReadFile(cfg.Layout().StatusFile())
	if err != nil {
		return nil
	}
	var st struct {
		Time time.Time `json:"time"`
		Pool struct {
			VMs []struct {
				Name string `json:"name"`
			} `json:"vms"`
		} `json:"pool"`
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		return nil
	}
	if time.Since(st.Time) > time.Minute {
		return nil
	}
	names := map[string]bool{}
	for _, vm := range st.Pool.VMs {
		names[vm.Name] = true
	}
	return names
}
