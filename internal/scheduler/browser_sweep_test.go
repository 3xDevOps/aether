package scheduler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/3xDevOps/Aether/internal/browser"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/runtime"
	"github.com/3xDevOps/Aether/internal/store"
)

type sweptBrowserRuntime struct {
	*fakeRuntime
	mu       sync.Mutex
	browsers map[runtime.ID]runtime.BrowserInfo
}

func (r *sweptBrowserRuntime) CreateBrowser(context.Context, runtime.BrowserSpec) (runtime.ID, error) {
	return "", errors.New("unexpected companion creation")
}

func (r *sweptBrowserRuntime) InspectBrowser(_ context.Context, id runtime.ID) (runtime.BrowserInfo, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	info, ok := r.browsers[id]
	if !ok {
		return runtime.BrowserInfo{}, runtime.ErrNotFound
	}
	return info, nil
}

func (r *sweptBrowserRuntime) Destroy(ctx context.Context, id runtime.ID) error {
	r.mu.Lock()
	delete(r.browsers, id)
	r.mu.Unlock()
	return r.fakeRuntime.Destroy(ctx, id)
}

func (r *sweptBrowserRuntime) has(id runtime.ID) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.browsers[id]
	return ok
}

// lookupFailingStore fails every session lookup while failing is set.
type lookupFailingStore struct {
	store.Store
	failing atomic.Bool
}

func (s *lookupFailingStore) GetRun(ctx context.Context, id domain.RunID) (*domain.Run, error) {
	if s.failing.Load() {
		return nil, context.DeadlineExceeded
	}
	return s.Store.GetRun(ctx, id)
}

func newBrowserSweepEnv(t *testing.T) (*testEnv, *sweptBrowserRuntime, *lookupFailingStore) {
	t.Helper()
	var rt *sweptBrowserRuntime
	var db *lookupFailingStore
	e := newTestEnv(t, func(c *Config) {
		rt = &sweptBrowserRuntime{fakeRuntime: c.Runtime.(*fakeRuntime), browsers: make(map[runtime.ID]runtime.BrowserInfo)}
		c.Runtime = rt
		db = &lookupFailingStore{Store: c.Store}
		c.Store = db
		c.BrowserImage = "aether/browser:test"
	})
	return e, rt, db
}

// addCompanion records a running companion, and its journal when journaled.
func (e *testEnv) addCompanion(t *testing.T, rt *sweptBrowserRuntime, run domain.RunID, runContainer, companion runtime.ID, journaled bool) {
	t.Helper()
	key := "browser-" + string(companion)
	rt.mu.Lock()
	rt.browsers[companion] = runtime.BrowserInfo{ContainerID: companion, RunContainer: runContainer, CreationKey: key, State: "running"}
	rt.mu.Unlock()
	if !journaled {
		return
	}
	journal, err := json.Marshal(browser.Status{RunID: string(run), RunContainer: runContainer, ContainerID: companion, CreationKey: key, State: "running"})
	if err != nil {
		t.Fatal(err)
	}
	dir := browserJournalDir(e, run)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "state.json"), journal, 0o600); err != nil {
		t.Fatal(err)
	}
}

func browserJournalDir(e *testEnv, run domain.RunID) string {
	digest := sha256.Sum256([]byte(run))
	return filepath.Join(e.cfg.StateDir, "browser", hex.EncodeToString(digest[:16]))
}

func TestSweepBrowsersRemovesOnlyCompanionsNoOpenSessionCanReach(t *testing.T) {
	e, rt, _ := newBrowserSweepEnv(t)
	ctx := t.Context()
	open, openContainer := e.launchFake(t, "open")
	moved, _ := e.launchFake(t, "moved to a new container")
	ended, endedContainer := e.launchFake(t, "ended")
	endedContainer.exitNow(0)
	e.waitStoreStatus(t, ended.ID, domain.RunCompleted)
	waitFor(t, "ended session finalized", func() bool {
		e.sched.mu.Lock()
		defer e.sched.mu.Unlock()
		return e.sched.runs[ended.ID] == nil
	})

	e.addCompanion(t, rt, open.ID, openContainer.id, "open-companion", true)
	e.addCompanion(t, rt, moved.ID, "fc-replaced", "moved-companion", true)
	e.addCompanion(t, rt, ended.ID, endedContainer.id, "ended-companion", true)
	e.addCompanion(t, rt, "run-deleted", "fc-deleted", "deleted-companion", true)
	e.addCompanion(t, rt, "", "fc-gone", "unjournaled-companion", false)

	e.sched.sweepBrowsers(ctx)

	for companion, kept := range map[runtime.ID]bool{
		"open-companion":        true,
		"unjournaled-companion": true,
		"moved-companion":       false,
		"ended-companion":       false,
		"deleted-companion":     false,
	} {
		if rt.has(companion) != kept {
			t.Errorf("%s kept = %v, want %v", companion, !kept, kept)
		}
	}
	for run, kept := range map[domain.RunID]bool{open.ID: true, moved.ID: false, ended.ID: false, "run-deleted": false} {
		_, err := os.Stat(browserJournalDir(e, run))
		if (err == nil) != kept {
			t.Errorf("journal of %s kept = %v, want %v", run, err == nil, kept)
		}
	}
	if openContainer.currentState() != "running" {
		t.Fatalf("open session container = %s, want running", openContainer.currentState())
	}
}

func TestSweepBrowsersKeepsCompanionsWhenTheSessionLookupFails(t *testing.T) {
	e, rt, db := newBrowserSweepEnv(t)
	e.addCompanion(t, rt, "run-unanswered", "fc-unanswered", "unanswered-companion", true)
	db.failing.Store(true)
	e.sched.sweepBrowsers(t.Context())
	if !rt.has("unanswered-companion") {
		t.Fatal("a session lookup that timed out removed its companion")
	}
	if _, err := os.Stat(browserJournalDir(e, "run-unanswered")); err != nil {
		t.Fatalf("journal after a failed lookup: %v", err)
	}
	db.failing.Store(false)
	e.sched.sweepBrowsers(t.Context())
	if rt.has("unanswered-companion") {
		t.Fatal("companion of an unknown session survived the next sweep")
	}
}

func TestStopDevelopmentRunRemovesCompanionWithoutRecordedContainer(t *testing.T) {
	e, rt, _ := newBrowserSweepEnv(t)
	e.addCompanion(t, rt, "run-lost", "fc-lost", "lost-companion", true)
	if err := e.sched.StopDevelopmentRun(t.Context(), "run-lost"); err != nil {
		t.Fatal(err)
	}
	if rt.has("lost-companion") {
		t.Fatal("companion of a session with no recorded container survived its stop")
	}
	if _, err := os.Stat(browserJournalDir(e, "run-lost")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("journal after stop: %v", err)
	}
}
