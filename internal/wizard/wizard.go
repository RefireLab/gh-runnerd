// Package wizard implements the interactive terminal prompts used by
// gh-runnerd init and other guided commands.
package wizard

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"golang.org/x/term"

	"github.com/RefireLab/gh-runnerd/internal/ansi"
)

// Prompter asks questions on a terminal and falls back to plain line
// reads when stdin is not a TTY (tests, pipes).
type Prompter struct {
	in    *bufio.Reader
	inFD  int
	out   io.Writer
	isTTY bool
	color bool
}

// New wraps in/out. Interactive() is true only when in is a terminal.
func New(in io.Reader, out io.Writer) *Prompter {
	p := &Prompter{in: bufio.NewReader(in), inFD: -1, out: out}
	if f, ok := in.(*os.File); ok {
		p.inFD = int(f.Fd())
		p.isTTY = term.IsTerminal(p.inFD)
	}
	if f, ok := out.(*os.File); ok {
		p.color = ansi.Enabled(f)
	}
	return p
}

// Interactive reports whether prompts can be shown to a human.
func (p *Prompter) Interactive() bool { return p.isTTY }

// Say prints one line of wizard output. Alert lines ([!!]) are painted
// red and success markers ([ok]) green when the output is a terminal.
func (p *Prompter) Say(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if p.color {
		switch {
		case strings.HasPrefix(msg, "[!!]"):
			msg = ansi.Red(msg)
		case strings.HasPrefix(msg, "[ok]"):
			msg = ansi.Green("[ok]") + strings.TrimPrefix(msg, "[ok]")
		}
	}
	fmt.Fprintln(p.out, msg)
}

func (p *Prompter) readLine() (string, error) {
	line, err := p.in.ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

// Ask prompts for a free-form answer; empty input returns def.
func (p *Prompter) Ask(label, def string) (string, error) {
	if def != "" {
		fmt.Fprintf(p.out, "%s [%s]: ", label, def)
	} else {
		fmt.Fprintf(p.out, "%s: ", label)
	}
	line, err := p.readLine()
	if err != nil {
		return "", err
	}
	if line == "" {
		return def, nil
	}
	return line, nil
}

// AskYesNo prompts for y/n; empty input returns def.
func (p *Prompter) AskYesNo(label string, def bool) (bool, error) {
	hint := "y/N"
	if def {
		hint = "Y/n"
	}
	for {
		fmt.Fprintf(p.out, "%s [%s]: ", label, hint)
		line, err := p.readLine()
		if err != nil {
			return def, err
		}
		switch strings.ToLower(line) {
		case "":
			return def, nil
		case "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		default:
			p.Say("please answer y or n")
		}
	}
}

// AskInt prompts for a positive integer; empty input returns def.
func (p *Prompter) AskInt(label string, def int) (int, error) {
	for {
		line, err := p.Ask(label, strconv.Itoa(def))
		if err != nil {
			return def, err
		}
		n, convErr := strconv.Atoi(line)
		if convErr != nil || n < 1 {
			p.Say("please enter a number >= 1")
			continue
		}
		return n, nil
	}
}

// AskSecret prompts without echoing when stdin is a TTY.
func (p *Prompter) AskSecret(label string) (string, error) {
	fmt.Fprintf(p.out, "%s: ", label)
	if p.isTTY {
		raw, err := term.ReadPassword(p.inFD)
		fmt.Fprintln(p.out)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(raw)), nil
	}
	return p.readLine()
}

// Select prints a numbered list and returns the chosen index.
func (p *Prompter) Select(label string, options []string, def int) (int, error) {
	if len(options) == 0 {
		return -1, fmt.Errorf("no options")
	}
	if def < 0 || def >= len(options) {
		def = 0
	}
	p.Say("%s", label)
	for i, opt := range options {
		p.Say("  %d) %s", i+1, opt)
	}
	for {
		line, err := p.Ask("choice", strconv.Itoa(def+1))
		if err != nil {
			return def, err
		}
		n, convErr := strconv.Atoi(line)
		if convErr != nil || n < 1 || n > len(options) {
			p.Say("please enter 1-%d", len(options))
			continue
		}
		return n - 1, nil
	}
}
