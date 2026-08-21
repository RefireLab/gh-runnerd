package ghapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/RefireLab/gh-runnerd/internal/config"
)

func orgClient(ts *httptest.Server) *Client {
	return New(config.GitHubConfig{
		BaseURL: ts.URL,
		Token:   "t",
		Scope:   "org",
		Org:     "acme",
	}).WithHTTP(ts.Client())
}

func TestListRunnersPaginates(t *testing.T) {
	total := 150
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/orgs/acme/actions/runners" {
			t.Errorf("path %s", r.URL.Path)
			w.WriteHeader(404)
			return
		}
		page := r.URL.Query().Get("page")
		start, count := 0, 100
		if page == "2" {
			start, count = 100, 50
		} else if page != "1" {
			t.Errorf("unexpected page %q", page)
		}
		out := struct {
			TotalCount int      `json:"total_count"`
			Runners    []Runner `json:"runners"`
		}{TotalCount: total}
		for i := start; i < start+count; i++ {
			out.Runners = append(out.Runners, Runner{ID: int64(i + 1), Name: fmt.Sprintf("gh-runnerd-1-%d", i), Status: "offline"})
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(ts.Close)
	runners, err := orgClient(ts).ListRunners(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(runners) != total {
		t.Fatalf("got %d runners, want %d", len(runners), total)
	}
	if runners[100].ID != 101 {
		t.Fatalf("second page not appended: %+v", runners[100])
	}
}

func TestRemoveRunnerTreats404AsGone(t *testing.T) {
	var deletes []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("method %s", r.Method)
		}
		deletes = append(deletes, r.URL.Path)
		w.WriteHeader(404)
	}))
	t.Cleanup(ts.Close)
	if err := orgClient(ts).RemoveRunner(context.Background(), 9); err != nil {
		t.Fatalf("404 must be success: %v", err)
	}
	if len(deletes) != 1 || deletes[0] != "/orgs/acme/actions/runners/9" {
		t.Fatalf("deletes %v", deletes)
	}
}

func TestRemoveRunnerBusyIsError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(422)
		_, _ = w.Write([]byte(`{"message":"Runner is busy"}`))
	}))
	t.Cleanup(ts.Close)
	if err := orgClient(ts).RemoveRunner(context.Background(), 9); err == nil {
		t.Fatal("422 must be an error")
	}
}

func TestSelectStale(t *testing.T) {
	t.Parallel()
	runners := []Runner{
		{ID: 1, Name: "gh-runnerd-1-1", Status: "offline"},
		{ID: 2, Name: "gh-runnerd-1-2", Status: "offline", Busy: true},
		{ID: 3, Name: "gh-runnerd-1-3", Status: "online"},
		{ID: 4, Name: "other-1", Status: "offline"},
		{ID: 5, Name: "gh-runnerd-1-5", Status: "offline"},
	}
	ids := func(rs []Runner) []int64 {
		var out []int64
		for _, r := range rs {
			out = append(out, r.ID)
		}
		return out
	}
	got := ids(SelectStale(runners, StaleFilter{Prefix: "gh-runnerd-"}))
	if fmt.Sprint(got) != "[1 5]" {
		t.Fatalf("prefix filter: %v", got)
	}
	got = ids(SelectStale(runners, StaleFilter{Prefix: "gh-runnerd-", IncludeIdle: true}))
	if fmt.Sprint(got) != "[1 3 5]" {
		t.Fatalf("idle included: %v", got)
	}
	got = ids(SelectStale(runners, StaleFilter{}))
	if fmt.Sprint(got) != "[1 4 5]" {
		t.Fatalf("empty prefix matches all: %v", got)
	}
	got = ids(SelectStale(runners, StaleFilter{Prefix: "gh-runnerd-", Skip: map[string]bool{"gh-runnerd-1-5": true}}))
	if fmt.Sprint(got) != "[1]" {
		t.Fatalf("skip: %v", got)
	}
}

func TestRemoveStaleContinuesPastFailures(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"total_count":3,"runners":[
				{"id":1,"name":"gh-runnerd-1-1","status":"offline","busy":false},
				{"id":2,"name":"gh-runnerd-1-2","status":"offline","busy":false},
				{"id":3,"name":"gh-runnerd-1-3","status":"online","busy":true}
			]}`))
		case r.URL.Path == "/orgs/acme/actions/runners/1":
			w.WriteHeader(500)
		default:
			w.WriteHeader(204)
		}
	}))
	t.Cleanup(ts.Close)
	removed, failed, err := orgClient(ts).RemoveStale(context.Background(), StaleFilter{Prefix: "gh-runnerd-"})
	if err == nil {
		t.Fatal("expected combined error for the failed delete")
	}
	if len(removed) != 1 || removed[0].ID != 2 {
		t.Fatalf("removed %+v", removed)
	}
	if len(failed) != 1 || failed[0].ID != 1 {
		t.Fatalf("failed %+v", failed)
	}
}
