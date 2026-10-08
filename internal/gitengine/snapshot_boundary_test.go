//go:build integration

package gitengine

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/events"
)

func TestSnapshotRevertRetainsEveryChangedTree(t *testing.T) {
	e := newTestEngine(t, nil)
	seedWorkspace(t, e, serveTransport(t, e), "ws1")
	ctx := t.Context()
	checkout, _, err := e.CreateRunCheckout(ctx, "ws1", "run1", "main", "revert history", "")
	if err != nil {
		t.Fatal(err)
	}
	capture := func(content string) string {
		t.Helper()
		if err := os.WriteFile(filepath.Join(checkout, "notes.txt"), []byte(content+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		tree, err := e.writeSnapshotTree(ctx, "run1", checkout)
		if err != nil {
			t.Fatal(err)
		}
		return tree
	}
	a, b := capture("A"), capture("B")
	if got := capture("A"); got != a {
		t.Fatalf("revert tree = %s, want %s", got, a)
	}
	capture("A")
	c, d := capture("C"), capture("D")
	for _, interval := range [][2]string{{a, b}, {b, a}, {a, c}, {c, d}} {
		if _, err := e.RunPatch(ctx, "run1", PatchRequest{From: interval[0], To: interval[1]}); err != nil {
			t.Fatalf("recorded interval unavailable: %v", err)
		}
	}
	store, err := e.snapshotStorePath("run1")
	if err != nil {
		t.Fatal(err)
	}
	refs, _, err := e.gitBareBounded(ctx, store, -1, "for-each-ref", "--format=%(objectname)", snapshotHistoryRoot)
	if err != nil || len(strings.Fields(refs)) != 4 {
		t.Fatalf("unchanged/reverted trees created duplicate refs: %q, %v", refs, err)
	}
}

func TestSnapshotPublishedPairIsAtomicOnRefLockFailure(t *testing.T) {
	log, err := events.OpenSQLiteLog(filepath.Join(t.TempDir(), "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = log.Close() })
	bus, err := events.NewInProc(t.Context(), log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bus.Close() })
	e := newTestEngine(t, bus)
	seedWorkspace(t, e, serveTransport(t, e), "ws1")
	ctx := t.Context()
	checkout, _, err := e.CreateRunCheckout(ctx, "ws1", "run1", "main", "atomic interval", "")
	if err != nil {
		t.Fatal(err)
	}
	var published string
	publish := func(tree string) error {
		_, err := bus.Publish(ctx, events.Event{
			WorkspaceID: "ws1", RunID: "run1",
			Payload: events.RunDiffPayload{ParentTree: published, Tree: tree, HistoryGap: true},
		})
		return err
	}
	capture := func(content string) string {
		t.Helper()
		if err := os.WriteFile(filepath.Join(checkout, "notes.txt"), []byte(content+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		tree, err := e.writeWatchSnapshot(ctx, "run1", checkout, publish)
		if err != nil {
			t.Fatal(err)
		}
		published = tree
		return tree
	}
	a, b := capture("A"), capture("B")
	store, err := e.snapshotStorePath("run1")
	if err != nil {
		t.Fatal(err)
	}
	blocker := filepath.Join(store, snapshotPublishedRef+".lock")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(checkout, "notes.txt"), []byte("C\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := e.writeWatchSnapshot(ctx, "run1", checkout, publish); err == nil {
		t.Fatal("published ref lock did not fail the transaction")
	}
	for ref, want := range map[string]string{"refs/aether/previous": a, snapshotPublishedRef: b} {
		got, _, err := e.gitBareBounded(ctx, store, 256, "rev-parse", "--verify", ref)
		if err != nil || strings.TrimSpace(got) != want {
			t.Fatalf("failed transaction changed %s: %q, %v", ref, got, err)
		}
	}
	patch, err := e.RunPatch(ctx, "run1", PatchRequest{From: a, To: b})
	if err != nil || !strings.Contains(patch.Text, "+B") {
		t.Fatalf("last published interval unavailable: %+v, %v", patch, err)
	}
	if _, err := os.Stat(filepath.Join(store, "history-gap")); err != nil {
		t.Fatalf("failed transaction lost gap: %v", err)
	}
	rows, err := log.Read(ctx, events.Filter{Run: "run1", Types: []events.Type{events.TypeRunDiff}}, 0, 0, 10)
	if err != nil || len(rows) != 3 {
		t.Fatalf("published interval count = %d, %v", len(rows), err)
	}
	delivered := rows[2].Payload.(events.RunDiffPayload)
	if delivered.ParentTree != b || delivered.Tree == b {
		t.Fatalf("new interval was not delivered before ref failure: %+v", delivered)
	}
	// Evidence uses the same staging store and advances generic latest.
	// Neither previously advertised interval may depend on that scratch pin.
	if err := os.WriteFile(filepath.Join(checkout, "notes.txt"), []byte("D\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	evidence, err := e.CaptureEvidence(ctx, "run1", "packet1")
	if err != nil {
		t.Fatal(err)
	}
	latest, _, err := e.gitBareBounded(ctx, store, 256, "rev-parse", "--verify", "refs/aether/latest")
	if err != nil || strings.TrimSpace(latest) == delivered.Tree {
		t.Fatalf("evidence did not advance latest: %q, %v", latest, err)
	}
	for _, interval := range [][2]string{{a, b}, {delivered.ParentTree, delivered.Tree}} {
		if _, err := e.RunPatch(ctx, "run1", PatchRequest{From: interval[0], To: interval[1]}); err != nil {
			t.Fatalf("advertised interval lost after independent staging: %v", err)
		}
	}
	patch, err = e.RunPatch(ctx, "run1", PatchRequest{})
	if err != nil || !strings.Contains(patch.Text, "+D") {
		t.Fatalf("current diff no longer sees latest files: %+v, %v", patch, err)
	}
	patch, err = e.RenderEvidence(ctx, "ws1", evidence.Commit, 0)
	if err != nil || !strings.Contains(patch.Text, "+D") {
		t.Fatalf("evidence lost during snapshot publication: %+v, %v", patch, err)
	}
	inflight, _, err := e.gitBareBounded(ctx, store, -1, "for-each-ref", "--format=%(objectname)", "refs/aether/inflight/")
	if err != nil || len(strings.Fields(inflight)) != 2 {
		t.Fatalf("inflight interval is not bounded to two pins: %q, %v", inflight, err)
	}
	if err := os.Remove(blocker); err != nil {
		t.Fatal(err)
	}
	c, err := e.writeWatchSnapshot(ctx, "run1", checkout, publish)
	if err != nil || c != delivered.Tree {
		t.Fatalf("retry did not acknowledge the protected inflight tree: %s, %v", c, err)
	}
	published = c
	if _, err := e.RunPatch(ctx, "run1", PatchRequest{From: b, To: c}); err != nil {
		t.Fatalf("successful retry interval: %v", err)
	}
	inflight, _, err = e.gitBareBounded(ctx, store, -1, "for-each-ref", "--format=%(objectname)", "refs/aether/inflight/")
	if err != nil || inflight != "" {
		t.Fatalf("acknowledged inflight pins remain: %q, %v", inflight, err)
	}
	d := capture("D")
	if d != strings.TrimSpace(latest) {
		t.Fatalf("next capture skipped current checkout: %s != %s", d, latest)
	}
	if _, err := e.RunPatch(ctx, "run1", PatchRequest{From: c, To: d}); err != nil {
		t.Fatalf("post-recovery current interval: %v", err)
	}
	// The next publication releases B's interval pin; its history remains.
	if _, err := e.RunPatch(ctx, "run1", PatchRequest{From: b, To: c}); err != nil {
		t.Fatalf("new publication expired the just-recovered interval: %v", err)
	}
}

func TestSnapshotGapSurvivesEventLogAppendFailure(t *testing.T) {
	for _, mode := range []string{"unchanged-retry", "quiet-restart", "restart-with-newer-checkout"} {
		t.Run(mode, func(t *testing.T) {
			restart := mode != "unchanged-retry"
			newer := mode == "restart-with-newer-checkout"
			ctx := t.Context()
			logPath := filepath.Join(t.TempDir(), "events.db")
			log, err := events.OpenSQLiteLog(logPath)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = log.Close() })
			faultDB, err := sql.Open("sqlite", logPath)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = faultDB.Close() })
			bus, err := events.NewInProc(ctx, log)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = bus.Close() })
			e := newTestEngine(t, bus)
			seedWorkspace(t, e, serveTransport(t, e), "ws1")
			checkout, _, err := e.CreateRunCheckout(ctx, "ws1", "run1", "main", "durable gap", "")
			if err != nil {
				t.Fatal(err)
			}
			meta, err := e.readRunMeta("run1")
			if err != nil {
				t.Fatal(err)
			}
			head, err := e.checkoutHead(ctx, "run1", checkout)
			if err != nil {
				t.Fatal(err)
			}
			base, err := e.gitCheckout(ctx, "run1", checkout, "rev-parse", meta.Base+"^{tree}")
			if err != nil {
				t.Fatal(err)
			}
			// Match StartDiffWatch's publication scope without starting its goroutine.
			e.mu.Lock()
			e.registry["run1"] = runInfo{workspace: "ws1", branch: meta.Branch}
			e.mu.Unlock()
			w := &diffWatch{e: e, run: "run1", checkout: checkout, base: meta.Base, lastHead: head, lastTree: base}
			write := func(body string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(checkout, "notes.txt"), []byte(body+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			write("A")
			w.snapshot()
			a := w.lastTree
			write("B")
			w.snapshot()
			b := w.lastTree
			if a == base || b == a {
				t.Fatal("initial snapshots did not publish")
			}
			store, err := e.snapshotStorePath("run1")
			if err != nil {
				t.Fatal(err)
			}
			// Force a real staging failure to establish an acknowledged gap.
			blocker := filepath.Join(store, "index.lock")
			if err := os.Mkdir(blocker, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(blocker, "busy"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
			write("C")
			w.snapshot()
			if !w.historyGap {
				t.Fatal("capture failure did not mark gap")
			}
			if err := os.Remove(filepath.Join(blocker, "busy")); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(blocker); err != nil {
				t.Fatal(err)
			}
			before, err := log.LastSeq(ctx)
			if err != nil {
				t.Fatal(err)
			}
			// Fail the actual SQLite Append, not a stand-in Bus.Publish.
			if _, err := faultDB.ExecContext(ctx, `CREATE TRIGGER fail_snapshot_append BEFORE INSERT ON events WHEN NEW.type = 'run.diff' BEGIN SELECT RAISE(FAIL, 'injected snapshot append failure'); END`); err != nil {
				t.Fatal(err)
			}
			w.snapshot()
			if !w.historyGap || !w.dirty || w.lastTree != b || e.lastSnapshotTree("run1") != b {
				t.Fatalf("failed append advanced/acknowledged interval: tree=%s gap=%t retry=%t", w.lastTree, w.historyGap, w.dirty)
			}
			if after, err := log.LastSeq(ctx); err != nil || after != before {
				t.Fatalf("failed append persisted an event: %d -> %d, %v", before, after, err)
			}
			if _, err := os.Stat(filepath.Join(store, "history-gap")); err != nil {
				t.Fatalf("failed append removed durable gap: %v", err)
			}
			latest, _, err := e.gitBareBounded(ctx, store, 256, "rev-parse", "refs/aether/latest")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := e.RunPatch(ctx, "run1", PatchRequest{From: a, To: b}); err != nil {
				t.Fatalf("failed publish unpinned last durable interval: %v", err)
			}
			if _, err := faultDB.ExecContext(ctx, "DROP TRIGGER fail_snapshot_append"); err != nil {
				t.Fatal(err)
			}
			wantEvents := 1
			if newer {
				// This edit predates watch registration, so only the recovery
				// follow-up timer can discover it after replaying inflight C.
				write("D")
				wantEvents = 2
			}
			if restart {
				// Start a real fresh watch; no rewrite or new fs event is needed.
				diffs := subscribeTypes(t, bus, events.TypeRunDiff)
				if err := e.StartDiffWatch(ctx, "ws1", "run1"); err != nil {
					t.Fatal(err)
				}
				for range wantEvents {
					if _, ok := nextEvent(t, diffs, 5*time.Second); !ok {
						e.StopDiffWatch("run1")
						t.Fatal("quiet restart did not recover gap and current checkout")
					}
				}
				e.StopDiffWatch("run1")
			} else {
				w.snapshot()
			}
			rows, err := log.Read(ctx, events.Filter{Run: "run1", Types: []events.Type{events.TypeRunDiff}}, before, 0, 10)
			if err != nil || len(rows) != wantEvents {
				t.Fatalf("recovery event count = %d, %v", len(rows), err)
			}
			payload := rows[0].Payload.(events.RunDiffPayload)
			if !payload.HistoryGap || payload.SnapshotError != "" || payload.ParentTree != b || payload.Tree != strings.TrimSpace(latest) {
				t.Fatalf("recovery hides missing interval: %+v", payload)
			}
			if _, err := os.Stat(filepath.Join(store, "history-gap")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("successful publication did not acknowledge gap: %v", err)
			}
			if _, err := e.RunPatch(ctx, "run1", PatchRequest{From: payload.ParentTree, To: payload.Tree}); err != nil {
				t.Fatalf("recovered interval unreadable: %v", err)
			}
			if newer {
				current := rows[1].Payload.(events.RunDiffPayload)
				if current.HistoryGap || current.SnapshotError != "" || current.ParentTree != payload.Tree || current.Tree == payload.Tree {
					t.Fatalf("post-recovery interval = %+v", current)
				}
				patch, err := e.RunPatch(ctx, "run1", PatchRequest{From: current.ParentTree, To: current.Tree})
				if err != nil || !strings.Contains(patch.Text, "+D") {
					t.Fatalf("post-recovery interval missed quiet current files: %+v, %v", patch, err)
				}
			}
		})
	}
}
