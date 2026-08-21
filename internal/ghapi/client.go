package ghapi

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/RefireLab/gh-runnerd/internal/config"
)

// Client talks to the GitHub REST API for JIT runners and job polling.
type Client struct {
	cfg        config.GitHubConfig
	http       *http.Client
	mu         sync.Mutex
	instToken  string
	instExpiry time.Time
}

func New(cfg config.GitHubConfig) *Client {
	return &Client{cfg: cfg, http: &http.Client{Timeout: 30 * time.Second}}
}

// WithHTTP replaces the HTTP client (tests).
func (c *Client) WithHTTP(h *http.Client) *Client {
	c.http = h
	return c
}

type jitRequest struct {
	Name          string   `json:"name"`
	RunnerGroupID int64    `json:"runner_group_id"`
	Labels        []string `json:"labels"`
}

// JITResult is the generate-jitconfig response.
type JITResult struct {
	Encoded string `json:"encoded_jit_config"`
	Runner  struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	} `json:"runner"`
}

// GenerateJITConfig creates a single-job runner registration.
func (c *Client) GenerateJITConfig(ctx context.Context, name string, labels []string) (JITResult, error) {
	group := c.cfg.RunnerGroupID
	if group == 0 {
		group = 1
	}
	body, err := json.Marshal(jitRequest{Name: name, RunnerGroupID: group, Labels: labels})
	if err != nil {
		return JITResult{}, err
	}
	path := c.jitPath()
	var out JITResult
	if err := c.do(ctx, http.MethodPost, path, body, &out); err != nil {
		return JITResult{}, err
	}
	if out.Encoded == "" {
		return JITResult{}, fmt.Errorf("github jitconfig response missing encoded_jit_config")
	}
	return out, nil
}

func (c *Client) jitPath() string {
	if strings.ToLower(c.cfg.Scope) == "org" {
		return fmt.Sprintf("/orgs/%s/actions/runners/generate-jitconfig", c.cfg.Org)
	}
	return fmt.Sprintf("/repos/%s/%s/actions/runners/generate-jitconfig", c.cfg.Owner, c.cfg.Repo)
}

// RunnerGroup is a GitHub Actions self-hosted runner group.
type RunnerGroup struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Default bool   `json:"default"`
}

// ListRunnerGroups returns org or repo runner groups the token can see.
func (c *Client) ListRunnerGroups(ctx context.Context) ([]RunnerGroup, error) {
	var out struct {
		RunnerGroups []RunnerGroup `json:"runner_groups"`
	}
	if err := c.do(ctx, http.MethodGet, c.runnerGroupsPath(), nil, &out); err != nil {
		return nil, err
	}
	return out.RunnerGroups, nil
}

func (c *Client) runnerGroupsPath() string {
	if strings.ToLower(c.cfg.Scope) == "org" {
		return fmt.Sprintf("/orgs/%s/actions/runner-groups?per_page=100", c.cfg.Org)
	}
	return fmt.Sprintf("/repos/%s/%s/actions/runner-groups?per_page=100", c.cfg.Owner, c.cfg.Repo)
}

// QueuedJob is a workflow job waiting for a runner.
type QueuedJob struct {
	ID     int64    `json:"id"`
	RunID  int64    `json:"run_id"`
	Name   string   `json:"name"`
	Labels []string `json:"labels"`
	URL    string   `json:"html_url"`
}

type workflowJobEvent struct {
	Action      string    `json:"action"`
	WorkflowJob QueuedJob `json:"workflow_job"`
}

// ParseWorkflowJob extracts a queued job from a webhook body.
func ParseWorkflowJob(body []byte) (QueuedJob, string, error) {
	var ev workflowJobEvent
	if err := json.Unmarshal(body, &ev); err != nil {
		return QueuedJob{}, "", err
	}
	return ev.WorkflowJob, ev.Action, nil
}

type runsPage struct {
	WorkflowRuns []struct {
		ID int64 `json:"id"`
	} `json:"workflow_runs"`
}

type jobsPage struct {
	Jobs []struct {
		ID      int64    `json:"id"`
		RunID   int64    `json:"run_id"`
		Name    string   `json:"name"`
		Status  string   `json:"status"`
		Labels  []string `json:"labels"`
		HTMLURL string   `json:"html_url"`
	} `json:"jobs"`
}

func (c *Client) pollRepos() [][2]string {
	if strings.ToLower(c.cfg.Scope) == "repo" {
		return [][2]string{{c.cfg.Owner, c.cfg.Repo}}
	}
	var out [][2]string
	for _, r := range c.cfg.PollRepos {
		owner, repo, ok := strings.Cut(r, "/")
		if !ok {
			continue
		}
		out = append(out, [2]string{owner, repo})
	}
	return out
}

// ListQueuedJobs polls configured repositories for queued workflow jobs.
func (c *Client) ListQueuedJobs(ctx context.Context) ([]QueuedJob, error) {
	var all []QueuedJob
	for _, pair := range c.pollRepos() {
		if pair[0] == "" || pair[1] == "" {
			continue
		}
		var runs runsPage
		path := fmt.Sprintf("/repos/%s/%s/actions/runs?status=queued&per_page=20", pair[0], pair[1])
		if err := c.do(ctx, http.MethodGet, path, nil, &runs); err != nil {
			return nil, err
		}
		for _, run := range runs.WorkflowRuns {
			var jobs jobsPage
			jpath := fmt.Sprintf("/repos/%s/%s/actions/runs/%d/jobs", pair[0], pair[1], run.ID)
			if err := c.do(ctx, http.MethodGet, jpath, nil, &jobs); err != nil {
				return nil, err
			}
			for _, j := range jobs.Jobs {
				if j.Status != "queued" {
					continue
				}
				all = append(all, QueuedJob{
					ID:     j.ID,
					RunID:  j.RunID,
					Name:   j.Name,
					Labels: j.Labels,
					URL:    j.HTMLURL,
				})
			}
		}
	}
	return all, nil
}

func (c *Client) do(ctx context.Context, method, path string, body []byte, dest any) error {
	token, err := c.token(ctx)
	if err != nil {
		return err
	}
	base := strings.TrimRight(c.cfg.BaseURL, "/")
	if base == "" {
		base = "https://api.github.com"
	}
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		return &APIError{Method: method, Path: path, StatusCode: resp.StatusCode, Status: resp.Status, Body: truncate(raw, 400)}
	}
	if dest == nil {
		return nil
	}
	return json.Unmarshal(raw, dest)
}

// APIError is a non-2xx GitHub API response.
type APIError struct {
	Method     string
	Path       string
	StatusCode int
	Status     string
	Body       string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("github %s %s: %s: %s", e.Method, e.Path, e.Status, e.Body)
}

func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "..."
}

func (c *Client) token(ctx context.Context) (string, error) {
	if t := strings.TrimSpace(c.cfg.Token); t != "" {
		return t, nil
	}
	return c.installationToken(ctx)
}

type tokenResp struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

func (c *Client) installationToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.instToken != "" && time.Now().Before(c.instExpiry.Add(-5*time.Minute)) {
		return c.instToken, nil
	}
	jwt, err := c.appJWT()
	if err != nil {
		return "", err
	}
	path := fmt.Sprintf("/app/installations/%d/access_tokens", c.cfg.InstallationID)
	base := strings.TrimRight(c.cfg.BaseURL, "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+path, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+jwt)
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("installation token: %s: %s", resp.Status, truncate(raw, 400))
	}
	var tr tokenResp
	if err := json.Unmarshal(raw, &tr); err != nil {
		return "", err
	}
	c.instToken = tr.Token
	c.instExpiry = tr.ExpiresAt
	if c.instExpiry.IsZero() {
		c.instExpiry = time.Now().Add(50 * time.Minute)
	}
	return c.instToken, nil
}

func (c *Client) appJWT() (string, error) {
	if c.cfg.AppID == 0 || c.cfg.AppPrivateKeyPath == "" {
		return "", fmt.Errorf("github app_id and app_private_key_path are required when no token is set")
	}
	pemBytes, err := os.ReadFile(c.cfg.AppPrivateKeyPath)
	if err != nil {
		return "", err
	}
	key, err := parseRSA(pemBytes)
	if err != nil {
		return "", err
	}
	now := time.Now().Unix()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"iat":%d,"exp":%d,"iss":%d}`, now-60, now+9*60, c.cfg.AppID)))
	signing := header + "." + payload
	hash := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, hash[:])
	if err != nil {
		return "", err
	}
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

func parseRSA(pemBytes []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, fmt.Errorf("no PEM block in GitHub App private key")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	rk, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("GitHub App private key is not RSA")
	}
	return rk, nil
}
