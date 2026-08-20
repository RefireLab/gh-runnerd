package runnerimages

import (
	"fmt"
	"path"
	"regexp"
	"strings"
)

// Step is one ordered provisioning action from the upstream Packer
// template: a batch of shell or PowerShell scripts with their environment.
type Step struct {
	Pwsh    bool     // run with pwsh -f instead of the bash shebang
	Env     []string // resolved KEY=VALUE pairs
	Scripts []string // script basenames, executed in order
}

// parseVariableDefaults extracts `variable "name" { default = "..." }`
// string defaults from variable.ubuntu.pkr.hcl. ${env("X")} defaults
// resolve to the empty string — we never build inside Azure.
func parseVariableDefaults(src string) map[string]string {
	vars := map[string]string{}
	for _, block := range hclBlocks(src, `variable`) {
		name := block.label
		def, ok := hclString(block.body, "default")
		if !ok {
			continue
		}
		vars[name] = envCallRe.ReplaceAllString(def, "")
	}
	return vars
}

var envCallRe = regexp.MustCompile(`\$\{env\("[^"]*"\)\}`)
var varRefRe = regexp.MustCompile(`\$\{var\.([A-Za-z0-9_]+)\}`)

// interpolate resolves ${var.x} references. Unresolvable references make
// ok false so the caller can drop the value instead of shipping garbage.
func interpolate(s string, vars map[string]string) (string, bool) {
	ok := true
	out := varRefRe.ReplaceAllStringFunc(s, func(m string) string {
		name := varRefRe.FindStringSubmatch(m)[1]
		v, found := vars[name]
		if !found {
			ok = false
			return ""
		}
		return v
	})
	return out, ok
}

// ParseTemplate extracts the ordered shell/pwsh provisioning steps from a
// build.*.pkr.hcl template. Inline provisioners (mkdir/mv/reboot/waagent
// deprovision) and file provisioners are intentionally skipped: the
// generated setup script recreates that fixed layout itself, and a QEMU
// bake VM neither reboots mid-provision nor talks to the Azure agent.
func ParseTemplate(src string, vars map[string]string) ([]Step, error) {
	var steps []Step
	for _, block := range hclBlocks(src, "provisioner") {
		if block.label != "shell" {
			continue
		}
		scripts := hclStringList(block.body, "scripts")
		if len(scripts) == 0 {
			if s, ok := hclString(block.body, "script"); ok {
				scripts = []string{s}
			}
		}
		if len(scripts) == 0 {
			continue
		}
		step := Step{}
		if exec, ok := hclString(block.body, "execute_command"); ok {
			step.Pwsh = strings.Contains(exec, "pwsh")
		}
		for _, raw := range hclStringList(block.body, "environment_vars") {
			resolved, ok := interpolate(raw, vars)
			if !ok || !strings.Contains(resolved, "=") {
				continue
			}
			step.Env = append(step.Env, resolved)
		}
		for _, raw := range scripts {
			resolved, _ := interpolate(raw, vars)
			step.Scripts = append(step.Scripts, path.Base(resolved))
		}
		steps = append(steps, step)
	}
	if len(steps) == 0 {
		return nil, fmt.Errorf("no shell provisioners found — upstream template layout changed")
	}
	return steps, nil
}

// hclBlock is one `kind "label" { body }` occurrence.
type hclBlock struct {
	label string
	body  string
}

// hclBlocks scans src for top-level-ish blocks of the given kind using a
// small string-aware brace matcher (HCL values contain ${...} braces, so
// naive matching breaks).
func hclBlocks(src, kind string) []hclBlock {
	var blocks []hclBlock
	re := regexp.MustCompile(kind + `\s+"([^"]+)"\s*\{`)
	for _, loc := range re.FindAllStringSubmatchIndex(src, -1) {
		label := src[loc[2]:loc[3]]
		bodyStart := loc[1] // right after the opening brace
		end, ok := matchBrace(src, bodyStart-1)
		if !ok {
			continue
		}
		blocks = append(blocks, hclBlock{label: label, body: src[bodyStart:end]})
	}
	return blocks
}

// matchBrace returns the index of the brace closing src[open] ('{'),
// skipping over double-quoted strings and ${...} interpolations.
func matchBrace(src string, open int) (int, bool) {
	depth := 0
	inStr := false
	for i := open; i < len(src); i++ {
		c := src[i]
		if inStr {
			switch c {
			case '\\':
				i++
			case '"':
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i, true
			}
		}
	}
	return 0, false
}

// hclString extracts `key = "value"` from a block body.
func hclString(body, key string) (string, bool) {
	re := regexp.MustCompile(`(?m)^\s*` + key + `\s*=\s*"`)
	loc := re.FindStringIndex(body)
	if loc == nil {
		return "", false
	}
	rest := body[loc[1]:]
	end := findStringEnd(rest)
	if end < 0 {
		return "", false
	}
	return rest[:end], true
}

// hclStringList extracts `key = [ "a", "b", ... ]` from a block body.
func hclStringList(body, key string) []string {
	re := regexp.MustCompile(`(?m)^\s*` + key + `\s*=\s*\[`)
	loc := re.FindStringIndex(body)
	if loc == nil {
		return nil
	}
	rest := body[loc[1]:]
	closing := strings.Index(rest, "]")
	if closing < 0 {
		return nil
	}
	var out []string
	quoted := regexp.MustCompile(`"((?:[^"\\]|\\.)*)"`)
	for _, m := range quoted.FindAllStringSubmatch(rest[:closing], -1) {
		out = append(out, m[1])
	}
	return out
}

// findStringEnd returns the index of the terminating quote of a string
// whose opening quote was already consumed.
func findStringEnd(s string) int {
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '"':
			return i
		}
	}
	return -1
}
