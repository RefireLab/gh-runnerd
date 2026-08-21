package ghapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// Runner is a self-hosted runner registration as GitHub reports it.
type Runner struct {
	ID     int64         `json:"id"`
	Name   string        `json:"name"`
	Status string        `json:"status"` // "online" or "offline"
	Busy   bool          `json:"busy"`
	Labels []RunnerLabel `json:"labels"`
}

// RunnerLabel is one label on a registered runner.
type RunnerLabel struct {
	Name string `json:"name"`
}

func (c *Client) runnersPath() string {
	if strings.ToLower(c.cfg.Scope) == "org" {
		return fmt.Sprintf("/orgs/%s/actions/runners", c.cfg.Org)
	}
	return fmt.Sprintf("/repos/%s/%s/actions/runners", c.cfg.Owner, c.cfg.Repo)
}

// ListRunners returns every self-hosted runner registered in the configured
// scope, following pagination.
func (c *Client) ListRunners(ctx context.Context) ([]Runner, error) {
	var all []Runner
	for page := 1; ; page++ {
		var out struct {
			TotalCount int      `json:"total_count"`
			Runners    []Runner `json:"runners"`
		}
		path := fmt.Sprintf("%s?per_page=100&page=%d", c.runnersPath(), page)
		if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
			return nil, err
		}
		all = append(all, out.Runners...)
		if len(out.Runners) < 100 || len(all) >= out.TotalCount {
			return all, nil
		}
	}
}

// RemoveRunner deletes one runner registration. A 404 is success: GitHub
// already removed it on its own (ephemeral runners disappear after a
// completed job). Deleting a busy runner fails with 422.
func (c *Client) RemoveRunner(ctx context.Context, id int64) error {
	err := c.do(ctx, http.MethodDelete, fmt.Sprintf("%s/%d", c.runnersPath(), id), nil, nil)
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
		return nil
	}
	return err
}

// StaleFilter selects runner registrations that are safe to remove.
type StaleFilter struct {
	// Prefix keeps only runners whose name starts with it; empty keeps
	// every name.
	Prefix string
	// IncludeIdle also selects online runners that are not running a job.
	// A runner whose host died keeps showing "Idle" in GitHub for several
	// minutes until GitHub notices the broken connection.
	IncludeIdle bool
	// Skip lists runner names that must never be selected, e.g. the live
	// VMs of a running daemon.
	Skip map[string]bool
}

// Match reports whether r is removable under f. Busy runners never match.
func (f StaleFilter) Match(r Runner) bool {
	if r.Busy || f.Skip[r.Name] {
		return false
	}
	if f.Prefix != "" && !strings.HasPrefix(r.Name, f.Prefix) {
		return false
	}
	return r.Status == "offline" || f.IncludeIdle
}

// SelectStale returns the runners matching f, preserving order.
func SelectStale(runners []Runner, f StaleFilter) []Runner {
	var out []Runner
	for _, r := range runners {
		if f.Match(r) {
			out = append(out, r)
		}
	}
	return out
}

// RemoveStale lists the scope's runners and removes every one matching f.
// One failed delete does not stop the batch: removed collects successes,
// failed the rest, and err joins the individual delete errors.
func (c *Client) RemoveStale(ctx context.Context, f StaleFilter) (removed, failed []Runner, err error) {
	runners, err := c.ListRunners(ctx)
	if err != nil {
		return nil, nil, err
	}
	var errs []error
	for _, r := range SelectStale(runners, f) {
		if derr := c.RemoveRunner(ctx, r.ID); derr != nil {
			failed = append(failed, r)
			errs = append(errs, fmt.Errorf("remove %s (id %d): %w", r.Name, r.ID, derr))
			continue
		}
		removed = append(removed, r)
	}
	return removed, failed, errors.Join(errs...)
}
