package cli

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/RefireLab/gh-runnerd/internal/ansi"
	"github.com/RefireLab/gh-runnerd/internal/config"
	"github.com/RefireLab/gh-runnerd/internal/doctor"
)

func doctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check that this Ubuntu host can run gh-runnerd",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, cfgPath, err := loadConfigOptionalPath(cmd)
			if err != nil {
				return err
			}
			rep := doctor.Run(cfg)
			rep.Checks = append([]doctor.Check{configCheck(cfgPath, cfg)}, rep.Checks...)
			rep.Checks = append(rep.Checks, doctor.EgressChecks(cfg)...)
			printReport(cmd.OutOrStdout(), rep)
			if rep.HasErrors() {
				os.Exit(1)
			}
			return nil
		},
	}
}

// configCheck says which config file the report is based on. Without it,
// doctor run outside the install folder silently checked the built-in
// defaults and read like a broken install ("no runner image", "no auth")
// while the real one was healthy.
func configCheck(path string, cfg config.Config) doctor.Check {
	if path != "" {
		return doctor.Check{Name: "config", Severity: doctor.OK, Message: path}
	}
	if _, err := os.Stat(config.SystemPath); errors.Is(err, fs.ErrPermission) {
		return doctor.Check{Name: "config", Severity: doctor.Warn,
			Message: fmt.Sprintf("%s exists but is not readable by this user — the checks below use built-in defaults; run: sudo gh-runnerd doctor", config.SystemPath)}
	}
	return doctor.Check{Name: "config", Severity: doctor.Warn,
		Message: fmt.Sprintf("no config file found — checking built-in defaults (data_dir %s); run doctor from the install folder or pass --config", cfg.DataDir)}
}

// printReport renders the doctor report, painting errors red and warnings
// yellow when stdout is a terminal.
func printReport(out io.Writer, rep doctor.Report) {
	color := false
	if f, ok := out.(*os.File); ok {
		color = ansi.Enabled(f)
	}
	for _, c := range rep.Checks {
		line := fmt.Sprintf("%-8s %-18s %s", strings.ToUpper(string(c.Severity)), c.Name, c.Message)
		if color {
			switch c.Severity {
			case doctor.Error:
				line = ansi.Red(line)
			case doctor.Warn:
				line = ansi.Yellow(line)
			case doctor.OK:
				line = ansi.Green("OK") + line[2:]
			}
		}
		fmt.Fprintln(out, line)
	}
}
