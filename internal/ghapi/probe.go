package ghapi

import (
	"context"
	"fmt"
	"net/http"
	"strings"
)

// Viewer returns the login of the user the token belongs to (GET /user).
func (c *Client) Viewer(ctx context.Context) (string, error) {
	var out struct {
		Login string `json:"login"`
	}
	if err := c.do(ctx, http.MethodGet, "/user", nil, &out); err != nil {
		return "", err
	}
	if out.Login == "" {
		return "", fmt.Errorf("github /user returned no login")
	}
	return out.Login, nil
}

// OwnerType reports whether owner is a "User" or an "Organization".
func (c *Client) OwnerType(ctx context.Context, owner string) (string, error) {
	var out struct {
		Type string `json:"type"`
	}
	if err := c.do(ctx, http.MethodGet, "/users/"+owner, nil, &out); err != nil {
		return "", err
	}
	return out.Type, nil
}

// CheckRunnerAccess verifies the credentials can administer self-hosted
// runners for the configured scope by listing them.
func (c *Client) CheckRunnerAccess(ctx context.Context) error {
	var path string
	if strings.ToLower(c.cfg.Scope) == "org" {
		if c.cfg.Org == "" {
			return fmt.Errorf("org scope requires github.org")
		}
		path = fmt.Sprintf("/orgs/%s/actions/runners?per_page=1", c.cfg.Org)
	} else {
		if c.cfg.Owner == "" || c.cfg.Repo == "" {
			return fmt.Errorf("repo scope requires github.owner and github.repo")
		}
		path = fmt.Sprintf("/repos/%s/%s/actions/runners?per_page=1", c.cfg.Owner, c.cfg.Repo)
	}
	var out struct {
		TotalCount int `json:"total_count"`
	}
	return c.do(ctx, http.MethodGet, path, nil, &out)
}
