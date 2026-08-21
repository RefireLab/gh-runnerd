package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/RefireLab/gh-runnerd/internal/config"
)

// fakeGitHub serves the runners list endpoint and records deletions.
type fakeGitHub struct {
	mu      sync.Mutex
	runners []map[string]any
	deleted []string
}

func (f *fakeGitHub) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/app/actions/runners":
			_ = json.NewEncoder(w).Encode(map[string]any{"total_count": len(f.runners), "runners": f.runners})
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/repos/acme/app/actions/runners/"):
			f.deleted = append(f.deleted, strings.TrimPrefix(r.URL.Path, "/repos/acme/app/actions/runners/"))
			w.WriteHeader(204)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	})
}

func (f *fakeGitHub) deletions() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.deleted...)
}

func writeRunnersConfig(t *testing.T, baseURL string) string {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	cfg := config.Defaults()
	cfg.DataDir = filepath.Join(dir, "data")
	cfg.GitHub.BaseURL = baseURL
	cfg.GitHub.Token = "t"
	cfg.GitHub.Scope = "repo"
	cfg.GitHub.Owner = "acme"
	cfg.GitHub.Repo = "app"
	if err := config.WriteFile(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	return cfgPath
}

func runRunners(t *testing.T, cfgPath string, stdin string, args ...string) (string, error) {
	t.Helper()
	cmd := Root()
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetIn(strings.NewReader(stdin))
	cmd.SetArgs(append(args, "--config", cfgPath))
	err := cmd.Execute()
	return buf.String(), err
}

func TestRunnersCleanupDryRunDeletesNothing(t *testing.T) {
	gh := &fakeGitHub{runners: []map[string]any{
		{"id": 1, "name": "gh-runnerd-98781-40", "status": "offline", "busy": false},
		{"id": 2, "name": "refirelab-master", "status": "offline", "busy": false},
	}}
	ts := httptest.NewServer(gh.handler(t))
	t.Cleanup(ts.Close)
	cfgPath := writeRunnersConfig(t, ts.URL)

	out, err := runRunners(t, cfgPath, "", "runners", "cleanup", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	if len(gh.deletions()) != 0 {
		t.Fatalf("dry run must not delete: %v", gh.deletions())
	}
	if !strings.Contains(out, "gh-runnerd-98781-40") || strings.Contains(out, "refirelab-master") {
		t.Fatalf("candidates must respect the prefix:\n%s", out)
	}
}

func TestRunnersCleanupNeedsConfirmation(t *testing.T) {
	gh := &fakeGitHub{runners: []map[string]any{
		{"id": 1, "name": "gh-runnerd-98781-40", "status": "offline", "busy": false},
	}}
	ts := httptest.NewServer(gh.handler(t))
	t.Cleanup(ts.Close)
	cfgPath := writeRunnersConfig(t, ts.URL)

	if _, err := runRunners(t, cfgPath, "", "runners", "cleanup"); err == nil {
		t.Fatal("no confirmation and no --yes must abort with an error")
	}
	if len(gh.deletions()) != 0 {
		t.Fatalf("aborted run must not delete: %v", gh.deletions())
	}

	out, err := runRunners(t, cfgPath, "y\n", "runners", "cleanup")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if fmt.Sprint(gh.deletions()) != "[1]" {
		t.Fatalf("confirmed run must delete: %v", gh.deletions())
	}
}

func TestRunnersCleanupYesRemovesOffline(t *testing.T) {
	gh := &fakeGitHub{runners: []map[string]any{
		{"id": 1, "name": "gh-runnerd-98781-40", "status": "offline", "busy": false},
		{"id": 2, "name": "gh-runnerd-98781-41", "status": "online", "busy": false},
		{"id": 3, "name": "gh-runnerd-98781-42", "status": "offline", "busy": true},
	}}
	ts := httptest.NewServer(gh.handler(t))
	t.Cleanup(ts.Close)
	cfgPath := writeRunnersConfig(t, ts.URL)

	out, err := runRunners(t, cfgPath, "", "runners", "cleanup", "--yes")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if fmt.Sprint(gh.deletions()) != "[1]" {
		t.Fatalf("only the offline non-busy runner may go: %v", gh.deletions())
	}
	if !strings.Contains(out, "removed gh-runnerd-98781-40") {
		t.Fatalf("output:\n%s", out)
	}
}

func TestRunnersCleanupIdleFlag(t *testing.T) {
	gh := &fakeGitHub{runners: []map[string]any{
		{"id": 1, "name": "gh-runnerd-98781-40", "status": "online", "busy": false},
	}}
	ts := httptest.NewServer(gh.handler(t))
	t.Cleanup(ts.Close)
	cfgPath := writeRunnersConfig(t, ts.URL)

	out, err := runRunners(t, cfgPath, "", "runners", "cleanup", "--yes")
	if err != nil {
		t.Fatal(err)
	}
	if len(gh.deletions()) != 0 {
		t.Fatalf("idle runner must survive without --idle: %v", gh.deletions())
	}
	if !strings.Contains(out, "--idle") {
		t.Fatalf("must hint about --idle when idle candidates exist:\n%s", out)
	}

	if _, err := runRunners(t, cfgPath, "", "runners", "cleanup", "--yes", "--idle"); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(gh.deletions()) != "[1]" {
		t.Fatalf("--idle must remove the zombie idle runner: %v", gh.deletions())
	}
}

func TestRunnersCleanupAllIgnoresPrefixButSkipsLiveVMs(t *testing.T) {
	gh := &fakeGitHub{runners: []map[string]any{
		{"id": 1, "name": "refirelab-master", "status": "offline", "busy": false},
		{"id": 2, "name": "gh-runnerd-98781-40", "status": "offline", "busy": false},
	}}
	ts := httptest.NewServer(gh.handler(t))
	t.Cleanup(ts.Close)
	cfgPath := writeRunnersConfig(t, ts.URL)

	// A fresh daemon heartbeat says VM gh-runnerd-98781-40 is alive.
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Layout().Ensure(); err != nil {
		t.Fatal(err)
	}
	status := fmt.Sprintf(`{"time":%q,"pool":{"vms":[{"name":"gh-runnerd-98781-40"}]}}`, time.Now().UTC().Format(time.RFC3339))
	if err := os.WriteFile(cfg.Layout().StatusFile(), []byte(status), 0o600); err != nil {
		t.Fatal(err)
	}

	out, err := runRunners(t, cfgPath, "", "runners", "cleanup", "--all", "--yes")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if fmt.Sprint(gh.deletions()) != "[1]" {
		t.Fatalf("--all must take the foreign offline runner but spare the live VM: %v", gh.deletions())
	}
}

func TestRunnersCleanupAllAndPrefixConflict(t *testing.T) {
	ts := httptest.NewServer((&fakeGitHub{}).handler(t))
	t.Cleanup(ts.Close)
	cfgPath := writeRunnersConfig(t, ts.URL)
	if _, err := runRunners(t, cfgPath, "", "runners", "cleanup", "--all", "--prefix", "x", "--yes"); err == nil {
		t.Fatal("--all with --prefix must be rejected")
	}
}

func TestRunnersListMarksOurs(t *testing.T) {
	gh := &fakeGitHub{runners: []map[string]any{
		{"id": 1, "name": "gh-runnerd-98781-40", "status": "offline", "busy": false, "labels": []map[string]any{{"name": "gh-runnerd"}}},
		{"id": 2, "name": "refirelab-master", "status": "online", "busy": false, "labels": []map[string]any{{"name": "refirelab-master"}}},
	}}
	ts := httptest.NewServer(gh.handler(t))
	t.Cleanup(ts.Close)
	cfgPath := writeRunnersConfig(t, ts.URL)

	out, err := runRunners(t, cfgPath, "", "runners", "list")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "gh-runnerd-98781-40 *") {
		t.Fatalf("our runner must carry the * marker:\n%s", out)
	}
	if strings.Contains(out, "refirelab-master *") {
		t.Fatalf("foreign runner must not carry the marker:\n%s", out)
	}
	if !strings.Contains(out, "2 runners, 1 created by gh-runnerd") {
		t.Fatalf("summary line:\n%s", out)
	}
}
