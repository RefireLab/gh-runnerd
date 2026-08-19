// Package ansi provides minimal terminal color helpers. Colors are only
// applied when the target is an interactive terminal and the user has not
// opted out via NO_COLOR.
package ansi

import (
	"os"

	"golang.org/x/term"
)

const (
	reset  = "\x1b[0m"
	red    = "\x1b[31;1m"
	green  = "\x1b[32m"
	yellow = "\x1b[33m"
)

// Enabled reports whether f is a terminal that should receive colors.
func Enabled(f *os.File) bool {
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return false
	}
	return f != nil && term.IsTerminal(int(f.Fd()))
}

// Red wraps s in bright red (errors and alerts).
func Red(s string) string { return red + s + reset }

// Green wraps s in green (success markers).
func Green(s string) string { return green + s + reset }

// Yellow wraps s in yellow (warnings).
func Yellow(s string) string { return yellow + s + reset }
