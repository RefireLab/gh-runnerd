package ghapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/RefireLab/gh-runnerd/internal/config"
)

func TestResolveRunnerGroup(t *testing.T) {
	t.Parallel()
	groups := []RunnerGroup{
		{ID: 1, Name: "Default", Default: true},
		{ID: 7, Name: "prod-gpu"},
	}
	cases := []struct {
		in      string
		wantID  int64
		wantErr bool
	}{
		{in: "", wantID: 1},
		{in: "Default", wantID: 1},
		{in: "prod-gpu", wantID: 7},
		{in: "7", wantID: 7},
		{in: "missing", wantErr: true},
		{in: "99", wantErr: true},
	}
	for _, c := range cases {
		got, err := ResolveRunnerGroup(groups, c.in)
		if c.wantErr {
			if err == nil {
				t.Fatalf("%q: expected error", c.in)
			}
			continue
		}
		if err != nil || got.ID != c.wantID {
			t.Fatalf("%q: got %+v err %v", c.in, got, err)
		}
	}
	def, err := ResolveRunnerGroup(nil, "Default")
	if err != nil || def.ID != 1 {
		t.Fatalf("nil list Default: %+v %v", def, err)
	}
	byID, err := ResolveRunnerGroup(nil, "12")
	if err != nil || byID.ID != 12 {
		t.Fatalf("nil list id: %+v %v", byID, err)
	}
}

func TestListRunnerGroups(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/orgs/acme/actions/runner-groups" {
			t.Errorf("path %s", r.URL.Path)
			w.WriteHeader(404)
			return
		}
		_, _ = w.Write([]byte(`{"runner_groups":[{"id":1,"name":"Default","default":true},{"id":3,"name":"gpu"}]}`))
	}))
	t.Cleanup(ts.Close)
	c := New(config.GitHubConfig{
		BaseURL: ts.URL,
		Token:   "t",
		Scope:   "org",
		Org:     "acme",
	}).WithHTTP(ts.Client())
	got, err := c.ListRunnerGroups(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[1].Name != "gpu" {
		t.Fatalf("%+v", got)
	}
}
