package serverupdate

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/selfupdate"
	"github.com/3xDevOps/Aether/internal/store/storetest"
)

// fakeFeed answers Latest with whatever the test last set.
type fakeFeed struct {
	mu     sync.Mutex
	latest selfupdate.Manifest
	err    error
	calls  int
}

func (f *fakeFeed) set(m selfupdate.Manifest, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.latest, f.err = m, err
}

func (f *fakeFeed) Latest(context.Context) (selfupdate.Manifest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.latest, f.err
}

func (f *fakeFeed) Fetch(context.Context, string) (selfupdate.Manifest, error) {
	return selfupdate.Manifest{}, errors.New("not used")
}

func newCheckService(t *testing.T, dir string, feed Releases, now func() time.Time) *Service {
	t.Helper()
	db, err := storetest.Open(filepath.Join(t.TempDir(), "aether.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	svc, err := New(Config{Store: db, Bus: newTestBus(t), StateDir: dir, Releases: feed, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

// A failed check is reported verbatim and backs off; the schedule and the
// error survive a restart, and a later success clears the error.
func TestReleaseCheckBacksOffAndPersists(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	feed := &fakeFeed{}
	feed.set(selfupdate.Manifest{}, errors.New("Get \"https://github.com/3xDevOps/Aether/releases/latest\": dial tcp: lookup github.com: no such host"))
	svc := newCheckService(t, dir, feed, clock)

	first := svc.runCheck(t.Context())
	if first.Error == "" || first.Failures != 1 {
		t.Fatalf("after one failure: %+v", first)
	}
	if wait := first.NextAt.Sub(now); wait < 4*time.Minute || wait > 6*time.Minute {
		t.Fatalf("first retry in %s, want 5m +/-10%%", wait)
	}
	second := svc.runCheck(t.Context())
	if wait := second.NextAt.Sub(now); wait < 9*time.Minute || wait > 11*time.Minute {
		t.Fatalf("second retry in %s, want 10m +/-10%%", wait)
	}

	restarted := newCheckService(t, dir, feed, clock)
	got, err := restarted.Status(t.Context(), protocol.ServerUpdateStatusParams{})
	if err != nil {
		t.Fatal(err)
	}
	if got.CheckError != second.Error || got.CheckedAt != now.Format(time.RFC3339) {
		t.Fatalf("status after restart = %+v, want the persisted failed check", got)
	}
	if restarted.check.NextAt != second.NextAt || restarted.check.Failures != 2 {
		t.Fatalf("restart reset the schedule: %+v", restarted.check)
	}

	feed.set(selfupdate.Manifest{Version: "v9.9.9", NotesURL: "https://github.com/3xDevOps/Aether/releases/tag/v9.9.9"}, nil)
	// Any member may refresh, so a check younger than refreshEvery stands.
	if got, err = restarted.Status(t.Context(), protocol.ServerUpdateStatusParams{Refresh: true}); err != nil || got.CheckError != second.Error {
		t.Fatalf("refresh within %s = %+v, %v; want the cached check", refreshEvery, got, err)
	}
	now = now.Add(refreshEvery)
	got, err = restarted.Status(t.Context(), protocol.ServerUpdateStatusParams{Refresh: true})
	if err != nil {
		t.Fatal(err)
	}
	if got.CheckError != "" || got.LatestVersion != "v9.9.9" || got.Latest != "v9.9.9" || got.NotesURL == "" {
		t.Fatalf("status after a refreshed success = %+v", got)
	}
	if wait := restarted.check.NextAt.Sub(now); wait < 5*time.Hour+24*time.Minute || wait > 6*time.Hour+36*time.Minute {
		t.Fatalf("next check in %s, want 6h +/-10%%", wait)
	}
}

// A release whose signed manifest is not published yet is not a failure.
func TestReleaseCheckWithoutAManifestIsNotAnError(t *testing.T) {
	feed := &fakeFeed{}
	feed.set(selfupdate.Manifest{}, selfupdate.ErrNoRelease)
	svc := newCheckService(t, t.TempDir(), feed, time.Now)
	if st := svc.runCheck(t.Context()); st.Error != "" || st.Failures != 0 {
		t.Fatalf("check = %+v, want no error", st)
	}
}

func TestBackoffDoublesUpToTheInterval(t *testing.T) {
	for n, want := range map[int]time.Duration{1: 5 * time.Minute, 2: 10 * time.Minute, 4: 40 * time.Minute, 7: 320 * time.Minute, 8: checkInterval, 30: checkInterval} {
		if got := backoff(n); got != want {
			t.Errorf("backoff(%d) = %s, want %s", n, got, want)
		}
	}
}

// The background loop checks on start when nothing is scheduled.
func TestStartChecksInTheBackground(t *testing.T) {
	feed := &fakeFeed{}
	feed.set(selfupdate.Manifest{Version: "v9.9.9"}, nil)
	svc := newCheckService(t, t.TempDir(), feed, time.Now)
	if err := svc.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = svc.Close() }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		svc.checkMu.Lock()
		latest := svc.check.LatestVersion
		svc.checkMu.Unlock()
		if latest == "v9.9.9" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the background check never ran")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
