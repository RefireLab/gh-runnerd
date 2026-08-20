package ghapi

import (
	"fmt"
	"strconv"
	"strings"
)

// ResolveRunnerGroup maps a wizard answer (name or numeric id) to a group.
// With no list from the API, "Default" (or empty) is id 1 and a bare number
// is accepted as-is.
func ResolveRunnerGroup(groups []RunnerGroup, input string) (RunnerGroup, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		for _, g := range groups {
			if g.Default {
				return g, nil
			}
		}
		if len(groups) > 0 {
			return groups[0], nil
		}
		return RunnerGroup{ID: 1, Name: "Default", Default: true}, nil
	}
	if id, err := strconv.ParseInt(input, 10, 64); err == nil && id > 0 {
		for _, g := range groups {
			if g.ID == id {
				return g, nil
			}
		}
		if len(groups) == 0 {
			return RunnerGroup{ID: id, Name: input}, nil
		}
		return RunnerGroup{}, fmt.Errorf("no runner group with id %d", id)
	}
	for _, g := range groups {
		if strings.EqualFold(g.Name, input) {
			return g, nil
		}
	}
	if len(groups) == 0 && strings.EqualFold(input, "default") {
		return RunnerGroup{ID: 1, Name: "Default", Default: true}, nil
	}
	return RunnerGroup{}, fmt.Errorf("unknown runner group %q", input)
}
