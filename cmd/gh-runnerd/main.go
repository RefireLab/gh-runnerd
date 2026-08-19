package main

import (
	"fmt"
	"os"

	"github.com/RefireLab/gh-runnerd/internal/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "gh-runnerd: %v\n", err)
		os.Exit(1)
	}
}
