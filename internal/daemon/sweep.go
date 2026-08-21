package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"time"

	"github.com/RefireLab/gh-runnerd/internal/ghapi"
)

// runnerAPI is the slice of the GitHub client the sweeper needs.
type runnerAPI interface {
	ListRunners(ctx context.Context) ([]ghapi.Runner, error)
	RemoveRunner(ctx context.Context, id int64) error
}

// sweeper removes stale runner registrations that a crashed VM or a killed
// daemon left behind in GitHub, where they would otherwise sit Offline for
// about a day until GitHub garbage-collects them.
//
// A registration is deleted only when it matches this daemon's runner name
// format (<prefix>-N-N), is offline and not busy, does not belong to a live
// VM, and has stayed that way for two sweeps at least grace apart. The
// grace pass means a runner that is briefly offline while its VM boots —
// here or on another host sharing the prefix — is never deleted.
type sweeper struct {
	api   runnerAPI
	name  *regexp.Regexp
	live  func() map[string]bool
	log   *slog.Logger
	grace time.Duration
	now   func() time.Time
	seen  map[int64]time.Time
}

func newSweeper(api runnerAPI, prefix string, live func() map[string]bool, log *slog.Logger) *sweeper {
	return &sweeper{
		api:   api,
		name:  regexp.MustCompile(fmt.Sprintf(`^%s-[0-9]+-[0-9]+$`, regexp.QuoteMeta(prefix))),
		live:  live,
		log:   log,
		grace: 2 * time.Minute,
		now:   time.Now,
		seen:  map[int64]time.Time{},
	}
}

// sweep runs one pass. The first pass only marks candidates; deletions
// start once a candidate has stayed stale for grace.
func (s *sweeper) sweep(ctx context.Context) {
	runners, err := s.api.ListRunners(ctx)
	if err != nil {
		s.log.Warn("list runners for stale cleanup", "err", err)
		return
	}
	stale := ghapi.SelectStale(runners, ghapi.StaleFilter{Skip: s.live()})
	now := s.now()
	current := make(map[int64]bool, len(stale))
	for _, r := range stale {
		if !s.name.MatchString(r.Name) {
			continue
		}
		current[r.ID] = true
		first, seenBefore := s.seen[r.ID]
		if !seenBefore {
			s.seen[r.ID] = now
			continue
		}
		if now.Sub(first) < s.grace {
			continue
		}
		if err := s.api.RemoveRunner(ctx, r.ID); err != nil {
			s.log.Warn("remove stale runner", "name", r.Name, "id", r.ID, "err", err)
			continue
		}
		delete(s.seen, r.ID)
		s.log.Info("removed stale offline runner from GitHub", "name", r.Name, "id", r.ID)
	}
	// Forget candidates that recovered or disappeared, so a name reused
	// after a healthy reconnect starts a fresh grace window.
	for id := range s.seen {
		if !current[id] {
			delete(s.seen, id)
		}
	}
}
