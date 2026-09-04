//go:build integration

package gitengine

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
