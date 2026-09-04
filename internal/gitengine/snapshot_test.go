//go:build integration

package gitengine

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/events"
)

// TestRunPatchRendersSnapshotInterval is the per-interval diff's server
// half: the dashboard asks "what changed in the last five minutes", which
// is one snapshot tree diffed against the previous one. A cumulative render
// cannot answer it - it shows the whole run, and it shows nothing at all
// for work that was made and then undone.
func TestRunPatchRendersSnapshotInterval(t *testing.T) {
	e := newTestEngine(t, nil)
	url := serveTransport(t, e)
	seedWorkspace(t, e, url, "ws1")
	ctx := t.Context()

	checkout, _, err := e.CreateRunCheckout(ctx, "ws1", "run1", "main", "interval diffs")
	if err != nil {
		t.Fatalf("CreateRunCheckout: %v", err)
	}
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(checkout, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	snap := func() string {
		t.Helper()
		tree, err := e.writeSnapshotTree(ctx, "run1", checkout)
		if err != nil {
			t.Fatalf("writeSnapshotTree: %v", err)
		}
		return tree
	}
	rangePatch := func(from, to string) Patch {
		t.Helper()
		p, err := e.RunPatch(ctx, "run1", PatchRequest{From: from, To: to})
		if err != nil {
			t.Fatalf("RunPatch %s..%s: %v", from, to, err)
		}
		return p
	}

	// Two edits to the same file, one snapshot apart. The interval between
	// them must show only the second.
	write("notes.txt", "first\n")
	tree1 := snap()
	write("notes.txt", "first\nsecond\n")
	tree2 := snap()
	if tree1 == tree2 {
		t.Fatal("two different worktrees produced the same snapshot tree")
	}

	interval := rangePatch(tree1, tree2)
	if interval.Base != tree1 {
		t.Errorf("interval base = %q, want the from tree %q", interval.Base, tree1)
	}
	if !strings.Contains(interval.Text, "+second") {
		t.Errorf("interval patch is missing the second edit:\n%s", interval.Text)
	}
	if strings.Contains(interval.Text, "+first") {
		t.Errorf("interval patch replays the previous interval's edit:\n%s", interval.Text)
	}

	// The cumulative render still answers the whole-run question.
	whole, err := e.RunPatch(ctx, "run1", PatchRequest{})
	if err != nil {
		t.Fatalf("cumulative RunPatch: %v", err)
	}
	base := bareRevParse(t, e, "ws1", "refs/heads/main")
	if whole.Base != base {
		t.Errorf("cumulative base = %q, want the fork point %q", whole.Base, base)
	}
	for _, want := range []string{"+first", "+second"} {
		if !strings.Contains(whole.Text, want) {
			t.Errorf("cumulative patch is missing %q:\n%s", want, whole.Text)
		}
	}

	// A change that is then undone: the interval shows the revert, while
	// the cumulative render shows nothing at all - the case a filtered
	// cumulative view gets wrong.
	write("file.txt", "one\ntwo\nthree\n")
	tree3 := snap()
	write("file.txt", "one\ntwo\n")
	tree4 := snap()

	revert := rangePatch(tree3, tree4)
	if !strings.Contains(revert.Text, "-three") {
		t.Errorf("interval patch is missing the revert:\n%s", revert.Text)
	}
	whole, err = e.RunPatch(ctx, "run1", PatchRequest{})
	if err != nil {
		t.Fatalf("cumulative RunPatch after revert: %v", err)
	}
	if strings.Contains(whole.Text, "file.txt") {
		t.Errorf("cumulative patch reports a file that was restored:\n%s", whole.Text)
	}

	// An empty interval renders empty rather than failing.
	if p := rangePatch(tree4, tree4); p.Text != "" {
		t.Errorf("tree against itself = %q, want an empty patch", p.Text)
	}

	// Client-supplied ids reach git only after validation, and only resolve
	// inside this run's own store.
	unknown := strings.Repeat("0", len(tree1))
	if _, err := e.RunPatch(ctx, "run1", PatchRequest{From: tree1, To: unknown}); !errors.Is(err, ErrSnapshotTreeMissing) {
		t.Errorf("unknown tree error = %v, want ErrSnapshotTreeMissing", err)
	}
	for _, bad := range []string{"HEAD", "../../etc", strings.ToUpper(tree1), tree1[:10]} {
		if _, err := e.RunPatch(ctx, "run1", PatchRequest{From: tree1, To: bad}); !errors.Is(err, ErrInvalidObjectID) {
			t.Errorf("to=%q error = %v, want ErrInvalidObjectID", bad, err)
		}
	}
	// A range needs both ends.
	if _, err := e.RunPatch(ctx, "run1", PatchRequest{To: tree2}); !errors.Is(err, ErrInvalidObjectID) {
		t.Errorf("half a range error = %v, want ErrInvalidObjectID", err)
	}

	// The store lives exactly as long as the checkout.
	store, err := e.snapshotStorePath("run1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(store); err != nil {
		t.Fatalf("snapshot store missing after snapshots: %v", err)
	}
	if err := e.RemoveRunCheckout(ctx, "run1"); err != nil {
		t.Fatalf("RemoveRunCheckout: %v", err)
	}
	if _, err := os.Stat(store); !os.IsNotExist(err) {
		t.Errorf("snapshot store survived RemoveRunCheckout: %v", err)
	}
}

// TestSnapshotStoreSurvivesAStaleIndexLock covers the store's one failure
// mode with no self-repair: git killed at the snapshot timeout, or a server
// that died mid-staging, leaves index.lock behind, and a persistent index
// means every later snapshot would hit it. The store is single-writer, so a
// lock found here is stale by construction and clearing it is safe.
func TestSnapshotStoreSurvivesAStaleIndexLock(t *testing.T) {
	e := newTestEngine(t, nil)
	url := serveTransport(t, e)
	seedWorkspace(t, e, url, "ws1")
	ctx := t.Context()

	checkout, _, err := e.CreateRunCheckout(ctx, "ws1", "run1", "main", "stale lock")
	if err != nil {
		t.Fatalf("CreateRunCheckout: %v", err)
	}
	if err := os.WriteFile(filepath.Join(checkout, "notes.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	first, err := e.writeSnapshotTree(ctx, "run1", checkout)
	if err != nil {
		t.Fatalf("writeSnapshotTree: %v", err)
	}

	store, err := e.snapshotStorePath("run1")
	if err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(store, "index.lock")
	if err := os.WriteFile(lock, []byte("stale\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(checkout, "notes.txt"), []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	second, err := e.writeSnapshotTree(ctx, "run1", checkout)
	if err != nil {
		t.Fatalf("writeSnapshotTree over a stale lock: %v", err)
	}
	if second == first {
		t.Error("the snapshot after the stale lock did not see the edit")
	}
	if _, err := os.Stat(lock); !os.IsNotExist(err) {
		t.Errorf("index.lock survived the snapshot: %v", err)
	}
}

// TestSnapshotLeavesTheCheckoutGitAlone is the ownership constraint the
// whole scratch-store design exists for: run provisioning chowns the
// checkout's .git to the run's user, so an object or fan-out directory
// written there by the server would leave the agent unable to commit. The
// cumulative render is covered in patch_test.go; this is the snapshot path,
// which writes far more often.
func TestSnapshotLeavesTheCheckoutGitAlone(t *testing.T) {
	e := newTestEngine(t, nil)
	url := serveTransport(t, e)
	seedWorkspace(t, e, url, "ws1")
	ctx := t.Context()

	checkout, _, err := e.CreateRunCheckout(ctx, "ws1", "run1", "main", "read only git")
	if err != nil {
		t.Fatalf("CreateRunCheckout: %v", err)
	}
	if err := os.WriteFile(filepath.Join(checkout, "notes.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitDir := filepath.Join(checkout, ".git")
	before := listTree(t, gitDir)

	trees := make([]string, 0, 2)
	for _, body := range []string{"one\ntwo\n", "one\ntwo\nthree\n"} {
		if err := os.WriteFile(filepath.Join(checkout, "notes.txt"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		tree, err := e.writeSnapshotTree(ctx, "run1", checkout)
		if err != nil {
			t.Fatalf("writeSnapshotTree: %v", err)
		}
		trees = append(trees, tree)
	}
	if _, err := e.RunPatch(ctx, "run1", PatchRequest{From: trees[0], To: trees[1]}); err != nil {
		t.Fatalf("interval RunPatch: %v", err)
	}
	if after := listTree(t, gitDir); after != before {
		t.Errorf("the checkout's .git changed:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

// TestRangePatchRefusesACommitID keeps the range ends to what the timeline
// actually offered. The store's alternates reach the checkout's whole
// object database, so a commit id from the cloned history resolves there; a
// peeled committish would render a diff no run.diff event ever named.
func TestRangePatchRefusesACommitID(t *testing.T) {
	e := newTestEngine(t, nil)
	url := serveTransport(t, e)
	seedWorkspace(t, e, url, "ws1")
	ctx := t.Context()

	checkout, _, err := e.CreateRunCheckout(ctx, "ws1", "run1", "main", "commit ids")
	if err != nil {
		t.Fatalf("CreateRunCheckout: %v", err)
	}
	if err := os.WriteFile(filepath.Join(checkout, "notes.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tree, err := e.writeSnapshotTree(ctx, "run1", checkout)
	if err != nil {
		t.Fatalf("writeSnapshotTree: %v", err)
	}
	head, err := e.git(ctx, checkout, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("rev-parse HEAD: %v", err)
	}
	if _, err := e.RunPatch(ctx, "run1", PatchRequest{From: head, To: tree}); !errors.Is(err, ErrInvalidObjectID) {
		t.Errorf("commit id as a range end = %v, want ErrInvalidObjectID", err)
	}

	// Rendering is a read: a request that arrives after the store was
	// reclaimed says so rather than laying the store down again, which
	// would leave a directory nothing ever collects.
	store, err := e.snapshotStorePath("run1")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(store); err != nil {
		t.Fatal(err)
	}
	if _, err := e.RunPatch(ctx, "run1", PatchRequest{From: tree, To: tree}); !errors.Is(err, ErrSnapshotTreeMissing) {
		t.Errorf("range against a reclaimed store = %v, want ErrSnapshotTreeMissing", err)
	}
	if _, err := os.Stat(store); !os.IsNotExist(err) {
		t.Errorf("a read recreated the snapshot store: %v", err)
	}
}

// TestDiffWatchResumesItsIntervalChainAfterARestart covers what
// docs/failure-handling.md promises about a server that came back: the watch
// picks the chain up from the tree its last snapshot wrote, so the next
// interval starts where the previous one ended. It is also the case that
// forces the tree, not the stat set, to be the publication gate - a fresh
// watch has no stat set to compare against, so a stats-based gate would open
// with an interval whose two ends are the same tree.
func TestDiffWatchResumesItsIntervalChainAfterARestart(t *testing.T) {
	bus, err := events.NewInProc(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bus.Close() })
	e := newTestEngine(t, bus)
	url := serveTransport(t, e)
	seedWorkspace(t, e, url, "ws1")
	ctx := t.Context()

	checkout, _, err := e.CreateRunCheckout(ctx, "ws1", "run1", "main", "restart")
	if err != nil {
		t.Fatalf("CreateRunCheckout: %v", err)
	}
	notes := filepath.Join(checkout, "notes.txt")
	diffs := subscribeTypes(t, bus, events.TypeRunDiff)
	if err := e.StartDiffWatch(ctx, "ws1", "run1"); err != nil {
		t.Fatalf("StartDiffWatch: %v", err)
	}

	if err := os.WriteFile(notes, []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ev, ok := nextEvent(t, diffs, 5*time.Second)
	if !ok {
		t.Fatal("no run.diff event after the first write")
	}
	first := ev.Payload.(events.RunDiffPayload)
	if first.Tree == "" {
		t.Fatal("the first snapshot recorded no tree")
	}
	baseTree, err := e.git(ctx, checkout, "rev-parse", bareRevParse(t, e, "ws1", "refs/heads/main")+"^{tree}")
	if err != nil {
		t.Fatalf("rev-parse base tree: %v", err)
	}
	if first.ParentTree != baseTree {
		t.Errorf("first parent tree = %q, want the fork-point tree %q", first.ParentTree, baseTree)
	}
	if got := e.lastSnapshotTree("run1"); got != first.Tree {
		t.Errorf("recorded tree = %q, want the published one %q", got, first.Tree)
	}

	e.StopDiffWatch("run1")
	if err := e.StartDiffWatch(ctx, "ws1", "run1"); err != nil {
		t.Fatalf("StartDiffWatch after the restart: %v", err)
	}

	// Rewriting the same bytes wakes the watch but moves no tree, and the
	// restarted watch has no stat set to compare against.
	if err := os.WriteFile(notes, []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if extra, more := nextEvent(t, diffs, 2*time.Second); more {
		p := extra.Payload.(events.RunDiffPayload)
		t.Fatalf("a restart published an unchanged interval: %s..%s", p.ParentTree, p.Tree)
	}

	// A real edit publishes, and its interval starts at the tree the previous
	// watch left behind rather than back at the fork point.
	if err := os.WriteFile(notes, []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ev, ok = nextEvent(t, diffs, 5*time.Second)
	if !ok {
		t.Fatal("no run.diff event after the post-restart edit")
	}
	second := ev.Payload.(events.RunDiffPayload)
	if second.ParentTree != first.Tree {
		t.Errorf("resumed parent tree = %q, want the pre-restart tree %q", second.ParentTree, first.Tree)
	}
	if second.Tree == first.Tree {
		t.Error("the post-restart edit produced the same tree")
	}
}
