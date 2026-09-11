//go:build integration

package gitengine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
)

func awaitWatchPatch(t *testing.T, e *Engine, diffs events.Subscription, run domain.RunID, addedLine string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		ev, ok := nextEvent(t, diffs, time.Until(deadline))
		if !ok {
			t.Fatalf("no snapshot patch containing %q", addedLine)
		}
		payload := ev.Payload.(events.RunDiffPayload)
		patch, err := e.RunPatch(t.Context(), run, PatchRequest{From: payload.ParentTree, To: payload.Tree})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(patch.Text, addedLine) {
			return
		}
	}
}

func TestDiffWatchContinuesAfterBackendError(t *testing.T) {
	bus, err := events.NewInProc(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bus.Close() })
	e := newTestEngine(t, bus)
	url := serveTransport(t, e)
	seedWorkspace(t, e, url, "ws1")
	const run = "run-watch-error"
	checkout, _, err := e.CreateRunCheckout(t.Context(), "ws1", run, "main", "watch error", "")
	if err != nil {
		t.Fatal(err)
	}
	diffs := subscribeTypes(t, bus, events.TypeRunDiff)
	if err = e.StartDiffWatch(t.Context(), "ws1", run); err != nil {
		t.Fatal(err)
	}
	e.mu.Lock()
	watch := e.watches[run]
	e.mu.Unlock()
	select {
	case watch.watcher.Errors <- fsnotify.ErrEventOverflow:
	case <-time.After(time.Second):
		t.Fatal("backend error delivery blocked the watcher")
	}
	if err = os.WriteFile(filepath.Join(checkout, "after-error.txt"), []byte("still observed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	awaitWatchPatch(t, e, diffs, run, "+still observed")
	if err = os.WriteFile(filepath.Join(checkout, "after-error.txt"), []byte("still observed\nsubsequent edit\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	awaitWatchPatch(t, e, diffs, run, "+subsequent edit")
}

func TestDiffWatchFollowsRecreatedDirectoryInodes(t *testing.T) {
	bus, err := events.NewInProc(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bus.Close() })
	e := newTestEngine(t, bus)
	e.cfg.QuietPeriod = 200 * time.Millisecond
	url := serveTransport(t, e)
	seedWorkspace(t, e, url, "ws1")
	const run = "run-watch-recreate"
	checkout, _, err := e.CreateRunCheckout(t.Context(), "ws1", run, "main", "watch recreate", "")
	if err != nil {
		t.Fatal(err)
	}
	live := filepath.Join(checkout, "live")
	if err = os.MkdirAll(filepath.Join(live, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(live, "nested", "note.txt"), []byte("original\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	prepared := filepath.Join(t.TempDir(), "prepared")
	if err = os.MkdirAll(filepath.Join(prepared, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(prepared, "nested", "note.txt"), []byte("replacement\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	diffs := subscribeTypes(t, bus, events.TypeRunDiff)
	if err = e.StartDiffWatch(t.Context(), "ws1", run); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(live, filepath.Join(t.TempDir(), "moved-away")); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(prepared, live); err != nil {
		t.Fatal(err)
	}
	awaitWatchPatch(t, e, diffs, run, "+replacement")
	if err = os.WriteFile(filepath.Join(live, "nested", "note.txt"), []byte("replacement\nnew inode observed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	awaitWatchPatch(t, e, diffs, run, "+new inode observed")
}
