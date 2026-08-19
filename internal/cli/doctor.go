package cli

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/RefireLab/gh-runnerd/internal/ansi"
	"github.com/RefireLab/gh-runnerd/internal/doctor"
)

func doctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check that this Ubuntu host can run gh-runnerd",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfigOptional(cmd)
			if err != nil {
				return err
			}
			rep := doctor.Run(cfg)
			printReport(cmd.OutOrStdout(), rep)
			if rep.HasErrors() {
				os.Exit(1)
			}
			return nil
		},
	}
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
