package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

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
			fmt.Fprint(cmd.OutOrStdout(), rep.String())
			if rep.HasErrors() {
				os.Exit(1)
			}
			return nil
		},
	}
}
