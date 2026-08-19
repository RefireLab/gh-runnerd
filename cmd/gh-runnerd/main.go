package main

import (
	"fmt"
	"os"

	"github.com/RefireLab/gh-runnerd/internal/ansi"
	"github.com/RefireLab/gh-runnerd/internal/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		msg := fmt.Sprintf("gh-runnerd: %v", err)
		if ansi.Enabled(os.Stderr) {
			msg = ansi.Red(msg)
		}
		fmt.Fprintln(os.Stderr, msg)
		os.Exit(1)
	}
}
