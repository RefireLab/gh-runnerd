package cli

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func statusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show daemon pool status",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfigOptional(cmd)
			if err != nil {
				return err
			}
			raw, err := os.ReadFile(cfg.Layout().StatusFile())
			if err != nil {
				return fmt.Errorf("daemon does not appear to be running (%s)", cfg.Layout().StatusFile())
			}
			var pretty any
			if err := json.Unmarshal(raw, &pretty); err != nil {
				fmt.Fprint(cmd.OutOrStdout(), string(raw))
				return nil
			}
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(pretty)
		},
	}
}
