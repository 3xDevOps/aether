//go:build integration

package gitengine

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/events"
)

func TestSnapshotRetentionExpiresRangesButKeepsCurrentAndEvidence(t *testing.T) {
	e := newTestEngine(t, nil)
	seedWorkspace(t, e, serveTransport(t, e), "ws1")
	ctx := t.Context()
	checkout, _, err := e.CreateRunCheckout(ctx, "ws1", "run1", "main", "bounded snapshots", "")
	if err != nil {
		t.Fatal(err)
	}
	store, err := e.snapshotStorePath("run1")
	if err != nil {
		t.Fatal(err)
	}
	var trees []string
	for i := range 5 {
		if err := os.WriteFile(filepath.Join(checkout, "notes.txt"), []byte(fmt.Sprintf("version %d\n", i)), 0o600); err != nil {
			t.Fatal(err)
		}
		tree, err := e.writeSnapshotTree(ctx, "run1", checkout)
		if err != nil {
			t.Fatal(err)
		}
		trees = append(trees, tree)
	}
	evidence, err := e.CaptureEvidence(ctx, "run1", "packet1")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.gitBareBounded(ctx, store, 0, "update-ref", "refs/valuable/keep", trees[0]); err != nil {
		t.Fatal(err)
	}
	lock := e.snapshotLock("run1")
	lock.Lock()
	err = e.retainSnapshot(ctx, store, trees[4], snapshotPolicy{1, 2, time.Hour, time.Now})
	lock.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.RunPatch(ctx, "run1", PatchRequest{From: trees[1], To: trees[4]}); !errors.Is(err, ErrSnapshotTreeMissing) {
		t.Fatalf("expired interval = %v", err)
	}
	if _, _, err := e.gitBareBounded(ctx, store, 64, "cat-file", "-t", trees[1]); err == nil {
		t.Fatal("unreferenced historical tree survived scoped GC")
	}
	if _, _, err := e.gitBareBounded(ctx, store, 64, "cat-file", "-t", trees[0]); err != nil {
		t.Fatalf("protected ref lost: %v", err)
	}
	patch, err := e.RunPatch(ctx, "run1", PatchRequest{})
	if err != nil || !strings.Contains(patch.Text, "+version 4") {
		t.Fatalf("current diff = %+v, %v", patch, err)
	}
	patch, err = e.RenderEvidence(ctx, "ws1", evidence.Commit, 0)
	if err != nil || !strings.Contains(patch.Text, "+version 4") {
		t.Fatalf("retained evidence = %+v, %v", patch, err)
	}
	meta, err := e.readRunMeta("run1")
	if err != nil {
		t.Fatal(err)
	}
	base, err := e.gitCheckout(ctx, "run1", checkout, "rev-parse", meta.Base+"^{tree}")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.RunPatch(ctx, "run1", PatchRequest{From: base, To: trees[4]}); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotRetentionMigratesLegacyAndDeduplicatesUnchangedTrees(t *testing.T) {
	e := newTestEngine(t, nil)
	seedWorkspace(t, e, serveTransport(t, e), "ws1")
	ctx := t.Context()
	checkout, _, err := e.CreateRunCheckout(ctx, "ws1", "run1", "main", "legacy snapshots", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(checkout, "notes.txt"), []byte("legacy\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := e.snapshotStorePath("run1")
	if err != nil {
		t.Fatal(err)
	}
	// Build the actual old layout: an initialized bare Git directory with
	// HEAD and objects, but last is its only published-history catalog.
	if err := e.ensureScratchGitDir(ctx, store, checkout); err != nil {
		t.Fatal(err)
	}
	index, err := scratchIndex(store, checkout, "run1")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.gitStaged(ctx, checkout, index, 0, "add", "-A"); err != nil {
		t.Fatal(err)
	}
	tree, _, err := e.gitStaged(ctx, checkout, index, 256, "write-tree")
	if err != nil {
		t.Fatal(err)
	}
	tree = strings.TrimSpace(tree)
	if err := os.WriteFile(filepath.Join(store, lastTreeFile), []byte(tree+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(store, "HEAD")); err != nil {
		t.Fatal(err)
	}
	refs, _, err := e.gitBareBounded(ctx, store, -1, "for-each-ref", "--format=%(refname)", "refs/aether/")
	if err != nil || refs != "" {
		t.Fatalf("not a legacy store: %q, %v", refs, err)
	}
	meta, err := e.readRunMeta("run1")
	if err != nil {
		t.Fatal(err)
	}
	base, err := e.gitCheckout(ctx, "run1", checkout, "rev-parse", meta.Base+"^{tree}")
	if err != nil {
		t.Fatal(err)
	}
	patch, err := e.RunPatch(ctx, "run1", PatchRequest{From: base, To: tree})
	if err != nil || !strings.Contains(patch.Text, "+legacy") {
		t.Fatalf("quiet legacy interval: %+v, %v", patch, err)
	}
	if _, err := os.Stat(filepath.Join(store, lastTreeFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read did not migrate last: %v", err)
	}
	for range 3 {
		if _, err := e.writeSnapshotTree(ctx, "run1", checkout); err != nil {
			t.Fatal(err)
		}
	}
	if got := e.lastSnapshotTree("run1"); got != tree {
		t.Fatalf("migrated tip = %s", got)
	}
	if _, err := os.Stat(filepath.Join(store, lastTreeFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy marker still present: %v", err)
	}
	refs, _, err = e.gitBareBounded(ctx, store, -1, "for-each-ref", "--format=%(objectname)", snapshotHistoryRoot)
	if err != nil || len(strings.Fields(refs)) != 1 {
		t.Fatalf("duplicate refs: %q, %v", refs, err)
	}
}

func TestSnapshotOversizedInputLeavesCurrentFilesAndRecovery(t *testing.T) {
	e := newTestEngine(t, nil)
	seedWorkspace(t, e, serveTransport(t, e), "ws1")
	ctx := t.Context()
	checkout, _, err := e.CreateRunCheckout(ctx, "ws1", "run1", "main", "oversized snapshots", "")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(checkout, "large.bin")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(MaxSnapshotInputBytes + 1); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := e.writeWatchSnapshot(ctx, "run1", checkout, nil); !errors.Is(err, ErrSnapshotStorageLimit) {
		t.Fatalf("snapshot bound: %v", err)
	}
	if _, err := e.RunPatch(ctx, "run1", PatchRequest{}); !errors.Is(err, ErrSnapshotStorageLimit) {
		t.Fatalf("current staging bound: %v", err)
	}
	if _, err := e.FileDiff(ctx, "run1", "large.bin"); !errors.Is(err, ErrSnapshotStorageLimit) {
		t.Fatalf("file staging bound: %v", err)
	}
	if _, err := e.CaptureEvidence(ctx, "run1", "packet1"); !errors.Is(err, ErrEvidenceStorageLimit) {
		t.Fatalf("evidence staging bound: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Size() != MaxSnapshotInputBytes+1 {
		t.Fatalf("source mutated: %v, %v", info, err)
	}
	if err := os.WriteFile(path, []byte("latest useful content\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := e.writeWatchSnapshot(ctx, "run1", checkout, nil); err != nil {
		t.Fatal(err)
	}
	patch, err := e.RunPatch(ctx, "run1", PatchRequest{})
	if err != nil || !strings.Contains(patch.Text, "latest useful content") {
		t.Fatalf("recovery: %+v, %v", patch, err)
	}
}

func TestEvidenceCopyAndRangeReadersSurviveConcurrentSnapshotGC(t *testing.T) {
	e := newTestEngine(t, nil)
	seedWorkspace(t, e, serveTransport(t, e), "ws1")
	ctx := t.Context()
	checkout, _, err := e.CreateRunCheckout(ctx, "ws1", "run1", "main", "concurrent history", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(checkout, "notes.txt"), []byte("valuable evidence\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tree, err := e.writeWatchSnapshot(ctx, "run1", checkout, nil)
	if err != nil {
		t.Fatal(err)
	}
	store, err := e.snapshotStorePath("run1")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	start := make(chan struct{})
	for worker := range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for i := range 8 {
				switch worker {
				case 0:
					revision, err := e.CaptureEvidence(ctx, "run1", fmt.Sprintf("packet%d", i))
					if err != nil {
						t.Error(err)
						return
					}
					patch, err := e.RenderEvidence(ctx, "ws1", revision.Commit, 0)
					if err != nil || !strings.Contains(patch.Text, "valuable evidence") {
						t.Errorf("evidence copy: %+v, %v", patch, err)
						return
					}
				case 1:
					lock := e.snapshotLock("run1")
					lock.Lock()
					err := e.retainSnapshot(ctx, store, tree, snapshotPolicy{1, 1, time.Nanosecond, time.Now})
					lock.Unlock()
					if err != nil {
						t.Error(err)
						return
					}
				case 2:
					if _, err := e.RunPatch(ctx, "run1", PatchRequest{From: tree, To: tree}); err != nil {
						t.Error(err)
						return
					}
				}
			}
		}()
	}
	close(start)
	wg.Wait()
}

func TestDiffSnapshotFailurePublishesCurrentStatsAndNextIntervalGap(t *testing.T) {
	bus, err := events.NewInProc(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bus.Close() })
	e := newTestEngine(t, bus)
	seedWorkspace(t, e, serveTransport(t, e), "ws1")
	ctx := t.Context()
	checkout, _, err := e.CreateRunCheckout(ctx, "ws1", "run1", "main", "snapshot gap", "")
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
	// Match StartDiffWatch's publication scope without starting its goroutine.
	e.mu.Lock()
	e.registry["run1"] = runInfo{workspace: "ws1", branch: meta.Branch}
	e.mu.Unlock()
	w := &diffWatch{e: e, run: "run1", checkout: checkout, base: meta.Base, lastHead: head}
	diffs := subscribeTypes(t, bus, events.TypeRunDiff)
	path := filepath.Join(checkout, "notes.txt")
	if err := os.WriteFile(path, []byte("first\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	w.snapshot()
	first, ok := nextEvent(t, diffs, time.Second)
	if !ok {
		t.Fatal("missing initial diff")
	}
	store, err := e.snapshotStorePath("run1")
	if err != nil {
		t.Fatal(err)
	}
	blocker := filepath.Join(store, "index.lock")
	if err := os.Mkdir(blocker, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(blocker, "block"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("second\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	w.snapshot()
	failed, ok := nextEvent(t, diffs, time.Second)
	if !ok {
		t.Fatal("same-line-count change lost during snapshot failure")
	}
	gap := failed.Payload.(events.RunDiffPayload)
	if !gap.HistoryGap || gap.SnapshotError == "" || gap.Tree != "" || len(gap.Files) == 0 {
		t.Fatalf("missing current state/gap metadata: %+v", gap)
	}
	if strings.Contains(gap.SnapshotError, checkout) {
		t.Fatal("private path exposed")
	}
	if _, err := os.Stat(filepath.Join(store, "history-gap")); err != nil {
		t.Fatalf("gap not durable: %v", err)
	}
	if err := os.Remove(filepath.Join(blocker, "block")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(blocker); err != nil {
		t.Fatal(err)
	}
	w.snapshot()
	recovered, ok := nextEvent(t, diffs, time.Second)
	if !ok {
		t.Fatal("missing recovered diff")
	}
	payload := recovered.Payload.(events.RunDiffPayload)
	if !payload.HistoryGap || payload.SnapshotError != "" || payload.Tree == "" || payload.ParentTree != first.Payload.(events.RunDiffPayload).Tree {
		t.Fatalf("recovered interval hides gap: %+v", payload)
	}
	if _, err := e.RunPatch(ctx, "run1", PatchRequest{From: payload.ParentTree, To: payload.Tree}); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotCountAndAgeBoundariesProtectPublishedInterval(t *testing.T) {
	for _, byAge := range []bool{false, true} {
		t.Run(fmt.Sprintf("age=%t", byAge), func(t *testing.T) {
			e := newTestEngine(t, nil)
			seedWorkspace(t, e, serveTransport(t, e), "ws1")
			ctx := t.Context()
			checkout, _, err := e.CreateRunCheckout(ctx, "ws1", "run1", "main", "history boundary", "")
			if err != nil {
				t.Fatal(err)
			}
			var trees []string
			for i := range 4 {
				if err := os.WriteFile(filepath.Join(checkout, "notes.txt"), []byte(fmt.Sprintf("revision %d\n", i)), 0o600); err != nil {
					t.Fatal(err)
				}
				tree, err := e.writeWatchSnapshot(ctx, "run1", checkout, nil)
				if err != nil {
					t.Fatal(err)
				}
				trees = append(trees, tree)
			}
			store, err := e.snapshotStorePath("run1")
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now()
			policy := snapshotPolicy{512 << 20, 2, 7 * 24 * time.Hour, func() time.Time { return now }}
			if byAge {
				policy.trees = 1024
				now = now.Add(8 * 24 * time.Hour)
			}
			lock := e.snapshotLock("run1")
			lock.Lock()
			err = e.retainSnapshot(ctx, store, trees[3], policy)
			lock.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := e.RunPatch(ctx, "run1", PatchRequest{From: trees[0], To: trees[3]}); !errors.Is(err, ErrSnapshotTreeMissing) {
				t.Fatalf("expired range: %v", err)
			}
			if _, err := e.RunPatch(ctx, "run1", PatchRequest{From: trees[2], To: trees[3]}); err != nil {
				t.Fatalf("latest published interval lost: %v", err)
			}
			refs, _, err := e.gitBareBounded(ctx, store, -1, "for-each-ref", "--format=%(objectname)", snapshotHistoryRoot)
			if err != nil || len(strings.Fields(refs)) > 2 {
				t.Fatalf("unbounded catalog: %q, %v", refs, err)
			}
		})
	}
}
