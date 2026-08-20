package githubutil

import (
	"strings"
)

// JobMatches reports whether a runner registered with runnerLabels can take a
// job whose runs-on labels are jobLabels. GitHub requires the runner to have
// every requested label.
func JobMatches(runnerLabels, jobLabels []string) bool {
	if len(jobLabels) == 0 {
		return false
	}
	have := map[string]struct{}{}
	for _, l := range runnerLabels {
		have[normalizeLabel(l)] = struct{}{}
	}
	for _, l := range jobLabels {
		if _, ok := have[normalizeLabel(l)]; !ok {
			return false
		}
	}
	return true
}

// OwnsJob reports whether this gh-runnerd installation should handle the job.
// True when the job requests at least one of our configured labels.
func OwnsJob(configured, jobLabels []string) bool {
	want := map[string]struct{}{}
	for _, l := range configured {
		want[normalizeLabel(l)] = struct{}{}
	}
	for _, l := range jobLabels {
		if _, ok := want[normalizeLabel(l)]; ok {
			return true
		}
	}
	return false
}

func normalizeLabel(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// ParseLabelList splits a comma-separated label string and drops empties.
func ParseLabelList(s string) []string {
	return MergeLabels(nil, strings.Split(s, ","))
}

// MergeLabels returns unique labels, configured first, then extra from the job.
func MergeLabels(configured, extra []string) []string {
	seen := map[string]struct{}{}
	var out []string
	add := func(list []string) {
		for _, l := range list {
			l = strings.TrimSpace(l)
			if l == "" {
				continue
			}
			k := normalizeLabel(l)
			if _, ok := seen[k]; ok {
				continue
			}
			seen[k] = struct{}{}
			out = append(out, l)
		}
	}
	add(configured)
	add(extra)
	return out
}
