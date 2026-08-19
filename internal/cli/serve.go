package cli

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/RefireLab/gh-runnerd/internal/daemon"
)

func serveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Run the daemon: VMs, registry, webhook, poller",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, _, err := loadConfigRequired(cmd)
			if err != nil {
				return err
			}
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
			return daemon.New(cfg, log).Run(ctx)
		},
	}
}
