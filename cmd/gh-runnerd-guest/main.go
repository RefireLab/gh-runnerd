package main

import (
	"fmt"
	"os"

	"github.com/RefireLab/gh-runnerd/internal/guest"
)

func main() {
	cfg := guest.AgentConfig{
		HostIP:    envOr("GH_RUNNERD_HOST", "10.87.0.1"),
		RunnerDir: envOr("GH_RUNNERD_RUNNER_DIR", "/opt/actions-runner"),
	}
	if err := guest.RunAgent(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "gh-runnerd-guest: %v\n", err)
		os.Exit(1)
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
