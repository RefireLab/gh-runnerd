package ghapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/RefireLab/gh-runnerd/internal/config"
)

func TestViewerAndOwnerType(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/user", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(401)
			return
		}
		_, _ = w.Write([]byte(`{"login":"octo"}`))
	})
	mux.HandleFunc("/users/acme", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"login":"acme","type":"Organization"}`))
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	c := New(config.GitHubConfig{BaseURL: ts.URL, Token: "tok"}).WithHTTP(ts.Client())
	login, err := c.Viewer(context.Background())
	if err != nil || login != "octo" {
		t.Fatalf("login %q err %v", login, err)
	}
	typ, err := c.OwnerType(context.Background(), "acme")
	if err != nil || typ != "Organization" {
		t.Fatalf("type %q err %v", typ, err)
	}

	bad := New(config.GitHubConfig{BaseURL: ts.URL, Token: "wrong"}).WithHTTP(ts.Client())
	if _, err := bad.Viewer(context.Background()); err == nil {
		t.Fatal("bad token must fail")
	}
}

func TestCheckRunnerAccess(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/acme/app/actions/runners", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"total_count":0,"runners":[]}`))
	})
	mux.HandleFunc("/orgs/acme/actions/runners", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		_, _ = w.Write([]byte(`{"message":"Resource not accessible"}`))
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	repoC := New(config.GitHubConfig{BaseURL: ts.URL, Token: "t", Scope: "repo", Owner: "acme", Repo: "app"}).WithHTTP(ts.Client())
	if err := repoC.CheckRunnerAccess(context.Background()); err != nil {
		t.Fatalf("repo access: %v", err)
	}
	orgC := New(config.GitHubConfig{BaseURL: ts.URL, Token: "t", Scope: "org", Org: "acme"}).WithHTTP(ts.Client())
	if err := orgC.CheckRunnerAccess(context.Background()); err == nil {
		t.Fatal("403 must fail")
	}
	empty := New(config.GitHubConfig{BaseURL: ts.URL, Token: "t", Scope: "repo"}).WithHTTP(ts.Client())
	if err := empty.CheckRunnerAccess(context.Background()); err == nil {
		t.Fatal("missing owner/repo must fail")
	}
}
