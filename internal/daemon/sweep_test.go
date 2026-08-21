package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/RefireLab/gh-runnerd/internal/ghapi"
)

type fakeRunnerAPI struct {
	runners []ghapi.Runner
	listErr error
	removed []int64
}

func (f *fakeRunnerAPI) ListRunners(ctx context.Context) ([]ghapi.Runner, error) {
	return f.runners, f.listErr
}

func (f *fakeRunnerAPI) RemoveRunner(ctx context.Context, id int64) error {
	f.removed = append(f.removed, id)
	for i, r := range f.runners {
		if r.ID == id {
			f.runners = append(f.runners[:i], f.runners[i+1:]...)
			break
		}
	}
	return nil
}

func testSweeper(api runnerAPI, live map[string]bool) *sweeper {
	s := newSweeper(api, "gh-runnerd", func() map[string]bool { return live }, slog.Default())
	return s
}

func TestSweepDeletesOnlyAfterGrace(t *testing.T) {
	t.Parallel()
	api := &fakeRunnerAPI{runners: []ghapi.Runner{
		{ID: 1, Name: "gh-runnerd-98781-40", Status: "offline"},
	}}
	s := testSweeper(api, nil)
	now := time.Now()
	s.now = func() time.Time { return now }

	s.sweep(context.Background())
	if len(api.removed) != 0 {
		t.Fatalf("first sweep must only mark, removed %v", api.removed)
	}
	now = now.Add(time.Minute)
	s.sweep(context.Background())
	if len(api.removed) != 0 {
		t.Fatalf("still inside grace, removed %v", api.removed)
	}
	now = now.Add(2 * time.Minute)
	s.sweep(context.Background())
	if fmt.Sprint(api.removed) != "[1]" {
		t.Fatalf("expected runner 1 removed after grace, got %v", api.removed)
	}
	if len(s.seen) != 0 {
		t.Fatalf("seen must be cleaned after removal: %v", s.seen)
	}
}

func TestSweepNeverTouchesForeignBusyOnlineOrLive(t *testing.T) {
	t.Parallel()
	api := &fakeRunnerAPI{runners: []ghapi.Runner{
		{ID: 1, Name: "refirelab-master", Status: "offline"},     // foreign name
		{ID: 2, Name: "gh-runnerd-prod-11-2", Status: "offline"}, // other daemon's prefix
		{ID: 3, Name: "gh-runnerd-11-3", Status: "online"},       // healthy idle
		{ID: 4, Name: "gh-runnerd-11-4", Status: "offline", Busy: true},
		{ID: 5, Name: "gh-runnerd-11-5", Status: "offline"}, // live local VM booting
		{ID: 6, Name: "gh-runnerd-11-6", Status: "offline"}, // the only real garbage
	}}
	s := testSweeper(api, map[string]bool{"gh-runnerd-11-5": true})
	now := time.Now()
	s.now = func() time.Time { return now }

	s.sweep(context.Background())
	now = now.Add(3 * time.Minute)
	s.sweep(context.Background())
	if fmt.Sprint(api.removed) != "[6]" {
		t.Fatalf("only runner 6 is removable, got %v", api.removed)
	}
}

func TestSweepForgetsRecoveredRunners(t *testing.T) {
	t.Parallel()
	api := &fakeRunnerAPI{runners: []ghapi.Runner{
		{ID: 1, Name: "gh-runnerd-11-1", Status: "offline"},
	}}
	s := testSweeper(api, nil)
	now := time.Now()
	s.now = func() time.Time { return now }

	s.sweep(context.Background())
	api.runners[0].Status = "online" // reconnected before grace ran out
	now = now.Add(3 * time.Minute)
	s.sweep(context.Background())
	if len(api.removed) != 0 {
		t.Fatalf("recovered runner must not be removed: %v", api.removed)
	}
	if len(s.seen) != 0 {
		t.Fatalf("recovered runner must leave seen: %v", s.seen)
	}

	// If it goes offline again the grace window starts over.
	api.runners[0].Status = "offline"
	now = now.Add(3 * time.Minute)
	s.sweep(context.Background())
	if len(api.removed) != 0 {
		t.Fatalf("fresh stale window must not remove immediately: %v", api.removed)
	}
}

func TestSweepSurvivesListErrors(t *testing.T) {
	t.Parallel()
	api := &fakeRunnerAPI{listErr: fmt.Errorf("boom")}
	s := testSweeper(api, nil)
	s.sweep(context.Background())
	if len(api.removed) != 0 {
		t.Fatalf("nothing must be removed on list error")
	}
}
