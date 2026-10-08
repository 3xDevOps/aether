//go:build integration

package gitengine

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
)

func TestSnapshotHistorySurvivesCheckoutCleanupAndExplicitDeletion(t *testing.T) {
	e := newTestEngine(t, nil)
	seedWorkspace(t, e, serveTransport(t, e), "ws1")
	ctx := t.Context()
	checkout, branch, err := e.CreateRunCheckout(ctx, "ws1", "run1", "main", "durable snapshots", "")
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
		// Snapshot trees may borrow committed blobs from checkout alternates.
		if _, err := e.CommitAll(ctx, "run1", fmt.Sprintf("version %d", i), domain.GitIdentity{}, nil); err != nil {
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
	sourceTip := bareRevParse(t, e, "ws1", "refs/heads/main")
	if _, err := e.PublishRunBranch(ctx, "run1"); err != nil {
		t.Fatal(err)
	}
	branchTip := bareRevParse(t, e, "ws1", "refs/heads/"+branch)
	if err := e.RemoveRunCheckout(ctx, "run1"); err != nil {
		t.Fatal(err)
	}
	if err := e.RemoveRunCheckout(ctx, "run1"); err != nil {
		t.Fatalf("idempotent checkout removal: %v", err)
	}
	if _, err := os.Stat(checkout); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("checkout survived cleanup: %v", err)
	}
	restarted, err := New(e.cfg)
	if err != nil {
		t.Fatal(err)
	}
	e = restarted
	for i := range len(trees) - 1 {
		patch, err := e.RunPatch(ctx, "run1", PatchRequest{From: trees[i], To: trees[i+1]})
		if err != nil || !strings.Contains(patch.Text, fmt.Sprintf("+version %d", i+1)) {
			t.Fatalf("recorded interval %d after restart/cleanup = %+v, %v", i, patch, err)
		}
	}
	patch, err := e.RunPatch(ctx, "run1", PatchRequest{})
	if err != nil || !patch.Recorded || !strings.Contains(patch.Text, "+version 4") {
		t.Fatalf("current diff = %+v, %v", patch, err)
	}
	patch, err = e.RenderEvidence(ctx, "ws1", evidence.Commit, 0)
	if err != nil || !strings.Contains(patch.Text, "+version 4") {
		t.Fatalf("retained evidence = %+v, %v", patch, err)
	}
	for range 2 {
		if err := e.RemoveRunHistory(ctx, "run1"); err != nil {
			t.Fatalf("explicit history deletion: %v", err)
		}
	}
	if _, err := os.Stat(store); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("snapshot store survived explicit deletion: %v", err)
	}
	if _, err := e.RunPatch(ctx, "run1", PatchRequest{From: trees[0], To: trees[4]}); !errors.Is(err, ErrSnapshotTreeMissing) {
		t.Fatalf("deleted history = %v", err)
	}
	patch, err = e.RenderEvidence(ctx, "ws1", evidence.Commit, 0)
	if err != nil || !strings.Contains(patch.Text, "+version 4") {
		t.Fatalf("explicit history deletion removed evidence: %+v, %v", patch, err)
	}
	if got := bareRevParse(t, e, "ws1", "refs/heads/main"); got != sourceTip {
		t.Fatalf("source branch changed: %s != %s", got, sourceTip)
	}
	if got := bareRevParse(t, e, "ws1", "refs/heads/"+branch); got != branchTip {
		t.Fatalf("run branch changed: %s != %s", got, branchTip)
	}
}

func TestSnapshotHistoryMigratesLegacyAndDeduplicatesUnchangedTrees(t *testing.T) {
	e := newTestEngine(t, nil)
	seedWorkspace(t, e, serveTransport(t, e), "ws1")
	ctx := t.Context()
	log, err := events.OpenSQLiteLog(filepath.Join(t.TempDir(), "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = log.Close() })
	bus, err := events.NewInProc(ctx, log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bus.Close() })
	e.cfg.EventLog = log
	checkout, _, err := e.CreateRunCheckout(ctx, "ws1", "run1", "main", "legacy snapshots", "")
	if err != nil {
		t.Fatal(err)
	}
	// Source ancestry is available through checkout alternates, but is not
	// owned snapshot history and must not all be copied during migration.
	if err := os.WriteFile(filepath.Join(checkout, "unrelated.txt"), []byte("unrelated source history\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	unrelated, err := e.CommitAll(ctx, "run1", "unrelated source", domain.GitIdentity{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	unrelatedTree, err := e.git(ctx, checkout, "rev-parse", unrelated+"^{tree}")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(checkout, "unrelated.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(checkout, "notes.txt"), []byte("older\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := e.CommitAll(ctx, "run1", "borrowed snapshot blob", domain.GitIdentity{}, nil); err != nil {
		t.Fatal(err)
	}
	borrowed, err := e.git(ctx, checkout, "rev-parse", "HEAD^{tree}")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.PublishRunBranch(ctx, "run1"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(checkout, "local.txt"), []byte("local snapshot root\n"), 0o600); err != nil {
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
	older, _, err := e.gitStaged(ctx, checkout, index, 256, "write-tree")
	if err != nil {
		t.Fatal(err)
	}
	older = strings.TrimSpace(older)
	// Exercise a legacy local packed tree as well as the loose latest tree.
	if _, _, err := e.gitBareBounded(ctx, store, 0, "update-ref", "refs/legacy-temporary", older); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.gitBareBounded(ctx, store, 0, "repack", "-a", "-d", "-l"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.gitBareBounded(ctx, store, 0, "update-ref", "-d", "refs/legacy-temporary"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(checkout, "local.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(checkout, "notes.txt"), []byte("legacy\n"), 0o600); err != nil {
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
	// Durable events, not arbitrary alternate objects, identify old history.
	// Cross a page boundary and include an already-pruned endpoint.
	for range 256 {
		if _, err := bus.Publish(ctx, events.Event{WorkspaceID: "ws1", RunID: "run1", Payload: events.RunDiffPayload{Tree: borrowed}}); err != nil {
			t.Fatal(err)
		}
	}
	for _, payload := range []events.RunDiffPayload{
		{ParentTree: borrowed, Tree: older},
		{ParentTree: older, Tree: tree},
		{ParentTree: strings.Repeat("0", len(borrowed)), Tree: tree},
	} {
		if _, err := bus.Publish(ctx, events.Event{WorkspaceID: "ws1", RunID: "run1", Payload: payload}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := bus.Publish(ctx, events.Event{WorkspaceID: "ws1", RunID: "other-run", Payload: events.RunDiffPayload{Tree: unrelatedTree}}); err != nil {
		t.Fatal(err)
	}
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
	if err != nil || strings.Count(refs, tree+"\n") != 1 {
		t.Fatalf("duplicate refs for captured tree: %q, %v", refs, err)
	}
	if strings.Contains(refs, unrelatedTree) {
		t.Fatal("migration cataloged unrelated alternate source history")
	}
	if err := e.RemoveRunCheckout(ctx, "run1"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(store, "objects", "info", "alternates")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("checkout alternate survived detachment: %v", err)
	}
	patch, err = e.RunPatch(ctx, "run1", PatchRequest{From: borrowed, To: tree})
	if err != nil || !strings.Contains(patch.Text, "-older") || !strings.Contains(patch.Text, "+legacy") {
		t.Fatalf("recorded alternate-only root after cleanup: %+v, %v", patch, err)
	}
	if _, err := e.RunPatch(ctx, "run1", PatchRequest{From: strings.Repeat("0", len(borrowed)), To: tree}); !errors.Is(err, ErrSnapshotTreeMissing) {
		t.Fatalf("already-pruned recorded endpoint was not reported unavailable: %v", err)
	}
	patch, err = e.RunPatch(ctx, "run1", PatchRequest{From: older, To: tree})
	if err != nil || !strings.Contains(patch.Text, "-older") || !strings.Contains(patch.Text, "+legacy") {
		t.Fatalf("uncatalogued packed legacy interval after checkout cleanup: %+v, %v", patch, err)
	}
	if _, err := e.RunPatch(ctx, "run1", PatchRequest{From: base, To: unrelatedTree}); !errors.Is(err, ErrSnapshotTreeMissing) {
		t.Fatalf("unrelated alternate history was copied or unavailability hidden: %v", err)
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

func TestEvidenceCopyAndRangeReadersSurviveConcurrentSnapshots(t *testing.T) {
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
					_, err := e.writeSnapshotTree(ctx, "run1", checkout)
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

func TestSnapshotHistorySurvivesFormerCountAndAgeLimits(t *testing.T) {
	for _, age := range []time.Duration{0, 30 * 24 * time.Hour} {
		t.Run(age.String(), func(t *testing.T) {
			e := newTestEngine(t, nil)
			seedWorkspace(t, e, serveTransport(t, e), "ws1")
			ctx := t.Context()
			checkout, _, err := e.CreateRunCheckout(ctx, "ws1", "run1", "main", "long history", "")
			if err != nil {
				t.Fatal(err)
			}
			store, err := e.snapshotStorePath("run1")
			if err != nil {
				t.Fatal(err)
			}
			if err := e.initSnapshotStore(ctx, "run1", checkout, store); err != nil {
				t.Fatal(err)
			}
			blob, err := e.gitInput(ctx, store, gitEnv(), []byte("recorded\n"), "hash-object", "-w", "--stdin")
			if err != nil {
				t.Fatal(err)
			}
			// Batch real Git tree creation to cover the former 1,024-tree
			// boundary without thousands of staging subprocesses.
			var input strings.Builder
			for i := range 1025 {
				fmt.Fprintf(&input, "100644 blob %s\tversion-%d.txt\n\n", strings.TrimSpace(blob), i)
			}
			out, err := e.gitInput(ctx, store, gitEnv(), []byte(input.String()), "mktree", "--batch")
			if err != nil {
				t.Fatal(err)
			}
			trees := strings.Fields(out)
			if len(trees) != 1025 {
				t.Fatalf("created %d trees", len(trees))
			}
			var refs strings.Builder
			refs.WriteString("start\n")
			stamp := time.Now().Add(-age).UnixNano()
			for i, tree := range trees {
				fmt.Fprintf(&refs, "update %s%019d-%s %s\n", snapshotHistoryRoot, stamp+int64(i), tree, tree)
			}
			refs.WriteString("prepare\ncommit\n")
			if _, err := e.gitInput(ctx, store, gitEnv(), []byte(refs.String()), "update-ref", "--stdin"); err != nil {
				t.Fatal(err)
			}
			if _, err := e.writeWatchSnapshot(ctx, "run1", checkout, nil); err != nil {
				t.Fatal(err)
			}
			if err := e.RemoveRunCheckout(ctx, "run1"); err != nil {
				t.Fatal(err)
			}
			e, err = New(e.cfg)
			if err != nil {
				t.Fatal(err)
			}
			patch, err := e.RunPatch(ctx, "run1", PatchRequest{From: trees[0], To: trees[len(trees)-1]})
			if err != nil || !strings.Contains(patch.Text, "version-0.txt") || !strings.Contains(patch.Text, "version-1024.txt") {
				t.Fatalf("oldest recorded range after new capture/restart/cleanup: %+v, %v", patch, err)
			}
		})
	}
}

func TestSnapshotHistorySurvivesFormerByteLimit(t *testing.T) {
	if testing.Short() {
		t.Skip("writes more than the former 512 MiB history limit")
	}
	e := newTestEngine(t, nil)
	seedWorkspace(t, e, serveTransport(t, e), "ws1")
	ctx := t.Context()
	checkout, _, err := e.CreateRunCheckout(ctx, "ws1", "run1", "main", "large history", "")
	if err != nil {
		t.Fatal(err)
	}
	var trees []string
	for range 9 {
		f, err := os.Create(filepath.Join(checkout, "recorded.bin"))
		if err != nil {
			t.Fatal(err)
		}
		// Incompressible, unrelated captures exceed 512 MiB even after
		// repacking; each capture remains below the 128 MiB input limit.
		_, copyErr := io.CopyN(f, rand.Reader, 64<<20)
		closeErr := f.Close()
		if err := errors.Join(copyErr, closeErr); err != nil {
			t.Fatal(err)
		}
		tree, err := e.writeSnapshotTree(ctx, "run1", checkout)
		if err != nil {
			t.Fatal(err)
		}
		trees = append(trees, tree)
	}
	patch, err := e.RunPatch(ctx, "run1", PatchRequest{From: trees[0], To: trees[len(trees)-1]})
	if err != nil || !strings.Contains(patch.Text, "Binary files") {
		t.Fatalf("oldest range after byte pressure: %+v, %v", patch, err)
	}
}

func TestSnapshotCheckoutCleanupWaitsForReadableHistoryLog(t *testing.T) {
	e := newTestEngine(t, nil)
	seedWorkspace(t, e, serveTransport(t, e), "ws1")
	ctx := t.Context()
	checkout, _, err := e.CreateRunCheckout(ctx, "ws1", "run1", "main", "history recovery", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(checkout, "notes.txt"), []byte("keep until history is durable\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tree, err := e.writeSnapshotTree(ctx, "run1", checkout)
	if err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(t.TempDir(), "events.db")
	log, err := events.OpenSQLiteLog(logPath)
	if err != nil {
		t.Fatal(err)
	}
	e.cfg.EventLog = log
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	if err := e.RemoveRunCheckout(ctx, "run1"); err == nil {
		t.Fatal("cleanup ignored an unavailable durable history log")
	}
	if _, err := os.Stat(checkout); err != nil {
		t.Fatalf("failed history detachment removed the checkout: %v", err)
	}
	log, err = events.OpenSQLiteLog(logPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = log.Close() })
	e.cfg.EventLog = log
	if err := e.RemoveRunCheckout(ctx, "run1"); err != nil {
		t.Fatalf("cleanup retry: %v", err)
	}
	if _, err := e.RunPatch(ctx, "run1", PatchRequest{From: tree, To: tree}); err != nil {
		t.Fatalf("retry lost captured history: %v", err)
	}
}
