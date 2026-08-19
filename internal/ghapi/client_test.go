package ghapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/RefireLab/gh-runnerd/internal/config"
)

func TestGenerateJITConfig(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/acme/app/actions/runners/generate-jitconfig" {
			t.Errorf("path %s", r.URL.Path)
			w.WriteHeader(404)
			return
		}
		if r.Header.Get("Authorization") != "Bearer ghs_test" {
			t.Errorf("auth %s", r.Header.Get("Authorization"))
		}
		body, _ := io.ReadAll(r.Body)
		var req jitRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatal(err)
		}
		if req.Name == "" || len(req.Labels) == 0 {
			t.Fatalf("bad body %s", body)
		}
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"encoded_jit_config":"abc123","runner":{"id":9,"name":"r1"}}`))
	}))
	t.Cleanup(ts.Close)
	c := New(config.GitHubConfig{
		BaseURL: ts.URL,
		Token:   "ghs_test",
		Scope:   "repo",
		Owner:   "acme",
		Repo:    "app",
	}).WithHTTP(ts.Client())
	got, err := c.GenerateJITConfig(context.Background(), "runner-1", []string{"gh-runnerd"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Encoded != "abc123" || got.Runner.ID != 9 {
		t.Fatalf("%+v", got)
	}
}

func TestParseWorkflowJob(t *testing.T) {
	t.Parallel()
	job, action, err := ParseWorkflowJob([]byte(`{
		"action":"queued",
		"workflow_job":{"id":1,"run_id":2,"name":"test","labels":["gh-runnerd"],"html_url":"https://example"}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if action != "queued" || job.ID != 1 || job.Labels[0] != "gh-runnerd" {
		t.Fatalf("%s %+v", action, job)
	}
}

func TestListQueuedJobs(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/acme/app/actions/runs", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"workflow_runs":[{"id":10}]}`))
	})
	mux.HandleFunc("/repos/acme/app/actions/runs/10/jobs", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"jobs":[{"id":1,"run_id":10,"name":"t","status":"queued","labels":["gh-runnerd"],"html_url":"u"}]}`))
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	c := New(config.GitHubConfig{
		BaseURL: ts.URL,
		Token:   "t",
		Scope:   "repo",
		Owner:   "acme",
		Repo:    "app",
	}).WithHTTP(ts.Client())
	jobs, err := c.ListQueuedJobs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].ID != 1 {
		t.Fatalf("%+v", jobs)
	}
}
