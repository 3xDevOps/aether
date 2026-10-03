//go:build integration

package gitengine

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
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

// TestDiffWatchPollsWhenTheKernelRefusesAWatcher covers a host whose inotify
// budget is spent: the watch must start anyway, because a failed
// StartDiffWatch fails the whole launch, and snapshots and file activity
// must keep arriving. The kernel can refuse the instance itself or, once the
// instance exists, the checkout's watches.
func TestDiffWatchPollsWhenTheKernelRefusesAWatcher(t *testing.T) {
	refusals := map[string]func() (*fsnotify.Watcher, error){
		"instance": func() (*fsnotify.Watcher, error) {
			return nil, fmt.Errorf("couldn't initialize inotify: %w", syscall.EMFILE)
		},
		"watches": func() (*fsnotify.Watcher, error) {
			watcher, err := fsnotify.NewWatcher()
			if err != nil {
				return nil, err
			}
			return watcher, watcher.Close()
		},
	}
	for name, refuse := range refusals {
		t.Run(name, func(t *testing.T) {
			newWatcher = refuse
			t.Cleanup(func() { newWatcher = fsnotify.NewWatcher })

			bus, err := events.NewInProc(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = bus.Close() })
			e := newTestEngine(t, bus)
			url := serveTransport(t, e)
			seedWorkspace(t, e, url, "ws1")
			const run = "run-watch-polling"
			checkout, _, err := e.CreateRunCheckout(t.Context(), "ws1", run, "main", "watch polling", "")
			if err != nil {
				t.Fatal(err)
			}
			diffs := subscribeTypes(t, bus, events.TypeRunDiff)
			if err = e.StartDiffWatch(t.Context(), "ws1", run); err != nil {
				t.Fatalf("StartDiffWatch without a kernel watcher: %v", err)
			}
			if err = os.WriteFile(filepath.Join(checkout, "polled.txt"), []byte("first poll\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			awaitWatchPatch(t, e, diffs, run, "+first poll")
			if _, ok := e.LastFileChange(run); !ok {
				t.Error("a polled change was not recorded as file activity")
			}
			if err = os.WriteFile(filepath.Join(checkout, "polled.txt"), []byte("first poll\nsecond poll\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			awaitWatchPatch(t, e, diffs, run, "+second poll")
			e.StopDiffWatch(run)
		})
	}
}

// TestDiffWatchPollsForASubtreeItCannotWatch covers the kernel refusing one
// directory watch while the rest of the checkout is watched: edits confined
// to that subtree must still reach a snapshot.
func TestDiffWatchPollsForASubtreeItCannotWatch(t *testing.T) {
	bus, err := events.NewInProc(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bus.Close() })
	e := newTestEngine(t, bus)
	url := serveTransport(t, e)
	seedWorkspace(t, e, url, "ws1")
	const run = "run-watch-subtree"
	checkout, _, err := e.CreateRunCheckout(t.Context(), "ws1", run, "main", "watch subtree", "")
	if err != nil {
		t.Fatal(err)
	}
	// inotify cannot watch a directory the server cannot read. Root can read
	// everything, so there the subtree is simply watched.
	blind := filepath.Join(checkout, "blind")
	if err = os.Mkdir(blind, 0o000); err != nil {
		t.Fatal(err)
	}
	diffs := subscribeTypes(t, bus, events.TypeRunDiff)
	if err = e.StartDiffWatch(t.Context(), "ws1", run); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(blind, 0o700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(blind, "note.txt"), []byte("first\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	awaitWatchPatch(t, e, diffs, run, "+first")
	if err = os.WriteFile(filepath.Join(blind, "note.txt"), []byte("first\nunwatched edit\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	awaitWatchPatch(t, e, diffs, run, "+unwatched edit")
}
