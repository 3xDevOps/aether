package serverupdate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"math/rand/v2"
	"os"
	"path/filepath"
	"time"

	"github.com/3xDevOps/Aether/internal/envname"
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
	LatestChannel string    `json:"latest_channel,omitempty"`
	LatestBuild   int       `json:"latest_build,omitempty"`
	LatestHotfix  int       `json:"latest_hotfix,omitempty"`
	NotesURL      string    `json:"notes_url,omitempty"`
	Error         string    `json:"error,omitempty"`
	Failures      int       `json:"failures,omitempty"`
	NextAt        time.Time `json:"next_at,omitzero"`
	// Pointers are the channel pointers' high-water marks.
	Pointers selfupdate.Marks `json:"pointers,omitempty"`
	// Channel is the release channel the latest fields were found on.
	Channel string `json:"channel,omitempty"`
	// followed is the pointer this check accepted, zero when none.
	followed selfupdate.Pointer
}

// forget drops the latest fields, which a check on another channel than
// theirs must not keep.
func (st *checkState) forget() {
	st.LatestVersion, st.LatestChannel, st.LatestBuild, st.LatestHotfix, st.NotesURL = "", "", 0, 0, ""
}

// latest is the newest release the check found, in release order.
func (st checkState) latest() selfupdate.Release {
	return selfupdate.Release{Version: st.LatestVersion, Channel: st.LatestChannel, Build: st.LatestBuild, Hotfix: st.LatestHotfix}
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

// checkTimeout bounds a check, and separately what it schedules, so a
// stalled release host fails them instead of holding the check.
const checkTimeout = time.Minute

// refreshEvery is the least time between the checks that
// server.update_status refresh can force, since any member may call it.
const refreshEvery = time.Minute

// checks reports whether this server runs the background check: a build
// with no release version has nothing to compare, so on the default feed
// it never dials, and neither does a process with the opt-out set.
func (s *Service) checks() bool {
	if envname.Getenv(selfupdate.OptOutEnv) != "" {
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
		// A restart keeps the check state but not the display pointers;
		// with the next check hours away, they are fetched once now.
		if cur := s.current(); cur.Channel != "" && cur.NextAt.After(s.cfg.Now()) {
			showCtx, cancel := context.WithTimeout(ctx, checkTimeout)
			s.showPointers(showCtx, cur, true)
			cancel()
		}
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
			s.checkAndSchedule(ctx)
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
// serialized by run, the only writer of check, so readers holding
// checkMu never wait on the network.
func (s *Service) runCheck(ctx context.Context) checkState {
	asked := s.checkRuns.Load()
	s.run <- struct{}{}
	defer func() { <-s.run }()
	if s.checkRuns.Load() != asked {
		return s.current()
	}
	return s.checkLocked(ctx)
}

// current is a copy of the check state. Only a holder of run replaces it,
// but checkSoon and a failed self-schedule move its NextAt under checkMu.
func (s *Service) current() checkState {
	s.checkMu.Lock()
	defer s.checkMu.Unlock()
	return s.check
}

// checkLocked is runCheck for a caller holding run. A check whose ctx
// was cancelled is not recorded: the request went away, the release host
// did not fail. One past its deadline is a failed check.
func (s *Service) checkLocked(ctx context.Context) checkState {
	s.checkRuns.Add(1)
	s.reloadState()
	before := s.current()
	st := before
	m, marks, channel, followed, err := s.latest(ctx, st.Pointers)
	if err != nil && errors.Is(ctx.Err(), context.Canceled) {
		return before
	}
	st.followed = followed
	now := s.cfg.Now().UTC()
	st.CheckedAt, st.Pointers = now, marks
	if st.Channel != channel {
		st.forget()
		st.Channel = channel
	}
	switch {
	case err == nil:
		st.LatestVersion, st.NotesURL, st.Error, st.Failures = m.Version, m.NotesURL, "", 0
		st.LatestChannel, st.LatestBuild, st.LatestHotfix = m.Channel, m.Build, m.Hotfix
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
	if werr := s.persistCheck(st); werr != nil {
		slog.Warn("serverupdate: persist release check", "error", werr)
		if s.cfg.Releases == nil && st.Error == "" && !maps.Equal(st.Pointers, before.Pointers) {
			// A channel mark only in memory is gone after a restart, so the
			// check that found it fails, offers nothing new and keeps the
			// marks on disk.
			failed := before
			failed.CheckedAt = now
			failed.Error = werr.Error()
			failed.Failures++
			failed.NextAt = now.Add(jitter(backoff(failed.Failures)))
			st = failed
		}
	}
	s.checkMu.Lock()
	s.check = st
	s.checkMu.Unlock()
	return st
}

// unreadableState is the error every channel check fails with while
// check.json cannot be read.
func unreadableState(err error) error {
	return fmt.Errorf("%w; fix it, or remove it, which forgets the channel marks, to resume release checks", err)
}

// reloadState reads check.json again while it could not be read, so a
// file an operator fixed or removed is taken up without a restart. The
// caller holds run.
func (s *Service) reloadState() {
	if s.stateErr == nil {
		return
	}
	check, err := loadCheck(s.cfg.StateDir)
	if err != nil {
		s.stateErr = unreadableState(err)
		return
	}
	s.stateErr = nil
	s.checkMu.Lock()
	s.check = check
	s.checkMu.Unlock()
}

// persistCheck saves st to check.json, unless the file could not be read.
func (s *Service) persistCheck(st checkState) error {
	if s.stateErr != nil {
		return nil
	}
	data, err := json.Marshal(st)
	if err == nil {
		err = selfupdate.WriteFileAtomic(checkPath(s.cfg.StateDir), data)
	}
	if err != nil {
		return fmt.Errorf("serverupdate: record the release check: %w", err)
	}
	return nil
}

// latest asks for the newest release: through the signed pointer of the
// server's release channel, or from Config.Releases when it is set. It
// returns marks raised to the pointer it accepted, the channel it
// followed and that pointer, zero when it accepted none.
func (s *Service) latest(ctx context.Context, marks selfupdate.Marks) (selfupdate.Manifest, selfupdate.Marks, string, selfupdate.Pointer, error) {
	if s.cfg.Releases != nil {
		m, err := s.cfg.Releases.Latest(ctx)
		return m, marks, "", selfupdate.Pointer{}, err
	}
	channel, err := s.chooseChannel(ctx)
	if err != nil {
		return selfupdate.Manifest{}, marks, "", selfupdate.Pointer{}, err
	}
	if s.stateErr != nil {
		return selfupdate.Manifest{}, marks, channel, selfupdate.Pointer{}, s.stateErr
	}
	m, p, raised, err := selfupdate.FollowChannelPointer(ctx, s.cfg.Checker.BaseURL(), channel, marks)
	return m, raised, channel, p, err
}

// published is the verified pointer of every channel, for display: the
// one followed, accepted, for channel, and each other channel's as
// FetchPointer verifies it; at start, with no check run yet, the
// followed channel's too. A pointer that does not verify is left out.
// Nothing here touches the marks, the channel or what the server takes.
func (s *Service) published(ctx context.Context, channel string, followed selfupdate.Pointer, atStart bool) map[string]publishedPointer {
	out := map[string]publishedPointer{}
	if channel == "" {
		return out
	}
	for _, ch := range []string{selfupdate.ChannelNightly, selfupdate.ChannelBeta, selfupdate.ChannelStable} {
		p := followed
		if ch != channel || atStart {
			var err error
			if p, err = selfupdate.FetchPointer(ctx, s.cfg.Checker.BaseURL(), ch); err != nil {
				continue
			}
		}
		if p.Tag != "" {
			out[ch] = publishedPointer{Version: p.Tag, Build: p.Build, Hotfix: p.Hotfix, PublishedAt: p.PublishedAt}
		}
	}
	return out
}

// publishedPointer is what a channel's pointer named at the last check.
type publishedPointer struct {
	Version     string    `json:"version"`
	Build       int       `json:"build,omitempty"`
	Hotfix      int       `json:"hotfix,omitempty"`
	PublishedAt time.Time `json:"published_at"`
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
	s.checkAndSchedule(ctx)
	select {
	case s.kick <- struct{}{}:
	default:
	}
}
