package serverupdate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"os"
	"path/filepath"
	"time"

	"github.com/3xDevOps/Aether/internal/selfupdate"
	"github.com/3xDevOps/Aether/internal/version"
)

// checkInterval is how often the server looks for a release on its own.
const checkInterval = 6 * time.Hour

// checkRetry is the first wait after a failed check; it doubles per
// consecutive failure up to checkInterval.
const checkRetry = 5 * time.Minute

// checkState is the background release check, persisted so a restart does
// not reset the schedule or the backoff.
type checkState struct {
	CheckedAt     time.Time `json:"checked_at,omitzero"`
	LatestVersion string    `json:"latest_version,omitempty"`
	NotesURL      string    `json:"notes_url,omitempty"`
	Error         string    `json:"error,omitempty"`
	Failures      int       `json:"failures,omitempty"`
	NextAt        time.Time `json:"next_at,omitzero"`
}

func checkPath(dir string) string { return filepath.Join(dir, "check.json") }

func loadCheck(dir string) (checkState, error) {
	var st checkState
	data, err := os.ReadFile(checkPath(dir))
	if errors.Is(err, os.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return st, fmt.Errorf("serverupdate: read %s: %w", checkPath(dir), err)
	}
	if err := json.Unmarshal(data, &st); err != nil {
		return checkState{}, fmt.Errorf("serverupdate: parse %s: %w", checkPath(dir), err)
	}
	return st, nil
}

// jitter spreads d by +/-10% so servers installed together do not ask
// GitHub in step.
func jitter(d time.Duration) time.Duration {
	return d + time.Duration((rand.Float64()*0.2-0.1)*float64(d))
}

// backoff is the wait after the n-th consecutive failure.
func backoff(n int) time.Duration {
	d := checkRetry
	for i := 1; i < n && d < checkInterval; i++ {
		d *= 2
	}
	return min(d, checkInterval)
}

// refreshEvery is the least time between the checks that
// server.update_status refresh can force, since any member may call it.
const refreshEvery = time.Minute

// checks reports whether this server runs the background check: a build
// with no release version has nothing to compare, so on the default feed
// it never dials, and neither does a process with the opt-out set.
func (s *Service) checks() bool {
	if os.Getenv(selfupdate.OptOutEnv) != "" {
		return false
	}
	return s.cfg.Releases != nil || version.Version != "dev"
}

// Start runs the background release check until ctx ends.
func (s *Service) Start(ctx context.Context) error {
	if !s.checks() {
		return nil
	}
	ctx, s.stop = context.WithCancel(ctx)
	s.loop.Add(1)
	go func() {
		defer s.loop.Done()
		for {
			s.checkMu.Lock()
			wait := s.check.NextAt.Sub(s.cfg.Now())
			s.checkMu.Unlock()
			timer := time.NewTimer(max(wait, 0))
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-s.kick:
				timer.Stop()
				continue
			case <-timer.C:
			}
			s.runCheck(ctx)
		}
	}()
	return nil
}

// Close stops the background check.
func (s *Service) Close() error {
	if s.stop != nil {
		s.stop()
	}
	s.loop.Wait()
	return nil
}

// runCheck asks the release feed for the newest signed release and
// persists the answer. Callers that queued behind a check that started
// after they did take its answer instead of asking again. Checks are
// serialized by runMu, the only writer of check, so readers holding
// checkMu never wait on the network.
func (s *Service) runCheck(ctx context.Context) checkState {
	asked := s.checkRuns.Load()
	s.runMu.Lock()
	defer s.runMu.Unlock()
	if s.checkRuns.Load() != asked {
		return s.check
	}
	defer s.checkRuns.Add(1)
	m, err := s.releases().Latest(ctx)
	now := s.cfg.Now().UTC()
	st := s.check
	st.CheckedAt = now
	switch {
	case err == nil:
		st.LatestVersion, st.NotesURL, st.Error, st.Failures = m.Version, m.NotesURL, "", 0
		st.NextAt = now.Add(jitter(checkInterval))
	case errors.Is(err, selfupdate.ErrNoRelease):
		// The newest release has not published its signed manifest yet:
		// not a failure, and nothing newer to offer.
		st.Error, st.Failures = "", 0
		st.NextAt = now.Add(jitter(checkInterval))
	default:
		st.Error = err.Error()
		st.Failures++
		st.NextAt = now.Add(jitter(backoff(st.Failures)))
		slog.Warn("serverupdate: release check failed", "error", err, "retry_at", st.NextAt)
	}
	s.checkMu.Lock()
	s.check = st
	s.checkMu.Unlock()
	if data, merr := json.Marshal(st); merr == nil {
		if werr := writeFileAtomic(checkPath(s.cfg.StateDir), data); werr != nil {
			slog.Warn("serverupdate: persist release check", "error", werr)
		}
	}
	return st
}

// refresh runs a check now and moves the background schedule after it,
// unless a check ran within refreshEvery, whose answer then stands.
func (s *Service) refresh(ctx context.Context) {
	if !s.checks() {
		return
	}
	s.checkMu.Lock()
	recent := s.cfg.Now().Sub(s.check.CheckedAt) < refreshEvery
	s.checkMu.Unlock()
	if recent {
		return
	}
	s.runCheck(ctx)
	select {
	case s.kick <- struct{}{}:
	default:
	}
}
