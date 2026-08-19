package images

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
)

// Instruction is one Runnerfile line.
type Instruction struct {
	Op   string
	Args string
	Line int
}

// Runnerfile is a parsed custom runner image definition.
type Runnerfile struct {
	From     string
	Runs     []string
	Preloads []string
	Raw      []Instruction
}

// ParseRunnerfile reads a Runnerfile from r.
func ParseRunnerfile(r io.Reader) (Runnerfile, error) {
	var rf Runnerfile
	sc := bufio.NewScanner(r)
	line := 0
	for sc.Scan() {
		line++
		raw := strings.TrimSpace(sc.Text())
		if raw == "" || strings.HasPrefix(raw, "#") {
			continue
		}
		op, args, _ := strings.Cut(raw, " ")
		op = strings.ToUpper(op)
		args = strings.TrimSpace(args)
		inst := Instruction{Op: op, Args: args, Line: line}
		rf.Raw = append(rf.Raw, inst)
		switch op {
		case "FROM":
			if rf.From != "" {
				return Runnerfile{}, fmt.Errorf("line %d: multiple FROM instructions", line)
			}
			if args == "" {
				return Runnerfile{}, fmt.Errorf("line %d: FROM requires an image", line)
			}
			if err := CheckRunnerBase(args); err != nil {
				return Runnerfile{}, fmt.Errorf("line %d: %w", line, err)
			}
			rf.From = args
		case "RUN":
			if args == "" {
				return Runnerfile{}, fmt.Errorf("line %d: RUN requires a command", line)
			}
			rf.Runs = append(rf.Runs, args)
		case "PRELOAD":
			if args == "" {
				return Runnerfile{}, fmt.Errorf("line %d: PRELOAD requires an image reference", line)
			}
			rf.Preloads = append(rf.Preloads, args)
		default:
			return Runnerfile{}, fmt.Errorf("line %d: unknown instruction %s (allowed: FROM, RUN, PRELOAD)", line, op)
		}
	}
	if err := sc.Err(); err != nil {
		return Runnerfile{}, err
	}
	if rf.From == "" {
		return Runnerfile{}, fmt.Errorf("Runnerfile must start with FROM gh-runnerd/ubuntu-24.04")
	}
	return rf, nil
}

// ParseRunnerfileFile reads a path.
func ParseRunnerfileFile(path string) (Runnerfile, error) {
	f, err := os.Open(path)
	if err != nil {
		return Runnerfile{}, err
	}
	defer f.Close()
	return ParseRunnerfile(f)
}

// CheckRunnerBase enforces Ubuntu-only runner templates.
func CheckRunnerBase(from string) error {
	from = strings.TrimSpace(from)
	allowed := map[string]struct{}{
		"gh-runnerd/ubuntu-24.04":       {},
		"gh-runnerd/ubuntu-24.04-amd64": {},
		"gh-runnerd/ubuntu-24.04-arm64": {},
		"ubuntu-24.04":                  {},
		"ubuntu-24.04-amd64":            {},
		"ubuntu-24.04-arm64":            {},
	}
	if _, ok := allowed[from]; ok {
		return nil
	}
	return fmt.Errorf("runner base %q is not allowed; gh-runnerd ships only Ubuntu 24.04 templates", from)
}
