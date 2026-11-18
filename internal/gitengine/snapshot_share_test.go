//go:build integration

package gitengine

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
)

// seedSources makes main of ws a commit of this module's internal/ sources,
// a realistic repository of about two thousand files.
func seedSources(t *testing.T, e *Engine, ws domain.WorkspaceID) {
	t.Helper()
	url := serveTransport(t, e)
	if _, err := e.InitWorkspaceRepo(t.Context(), ws); err != nil {
		t.Fatal(err)
	}
	src := t.TempDir()
	if err := os.CopyFS(filepath.Join(src, "internal"), os.DirFS("..")); err != nil {
		t.Fatal(err)
	}
	gitc(t, src, "init")
	gitc(t, src, "add", "-A")
	gitc(t, src, "commit", "-q", "-m", "sources")
	gitc(t, src, "push", "-q", url(ws), "main")
}

// sessionHistory records two snapshots of a session that edits one file and
// adds another, and returns them.
func sessionHistory(t *testing.T, e *Engine, run domain.RunID) (string, string) {
	t.Helper()
	checkout, _, err := e.CreateRunCheckout(t.Context(), "ws1", run, "main", "share history", "")
	if err != nil {
		t.Fatal(err)
	}
	var trees []string
	for _, line := range []string{"first", "second"} {
		f, err := os.OpenFile(filepath.Join(checkout, "internal", "gitengine", "engine.go"), os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		_, writeErr := f.WriteString("// " + line + " session edit\n")
		if err = errors.Join(writeErr, f.Close(), os.WriteFile(filepath.Join(checkout, "notes.txt"), []byte(line+"\n"), 0o600)); err != nil {
			t.Fatal(err)
		}
		tree, err := e.writeSnapshotTree(t.Context(), run, checkout)
		if err != nil {
			t.Fatal(err)
		}
		trees = append(trees, tree)
	}
	return trees[0], trees[1]
}

func objectBytes(t *testing.T, store string) int64 {
	t.Helper()
	var total int64
	err := filepath.WalkDir(filepath.Join(store, "objects"), func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		info, err := d.Info()
		total += info.Size()
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return total
}

// orphanForkPoint leaves the workspace repository with no branch or reflog
// that reaches the fork point, then prunes what nothing reaches.
func orphanForkPoint(t *testing.T, e *Engine, repo string) {
	t.Helper()
	ctx := t.Context()
	empty, err := e.git(ctx, repo, "hash-object", "-t", "tree", "-w", os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	unrelated, err := e.git(ctx, repo, "-c", "user.name=Test", "-c", "user.email=test@localhost", "commit-tree", "-m", "unrelated", empty)
	if err != nil {
		t.Fatal(err)
	}
	branches, err := e.git(ctx, repo, "for-each-ref", "--format=%(refname)", "refs/heads/")
	if err != nil {
		t.Fatal(err)
	}
	for ref := range strings.FieldsSeq(branches) {
		if _, err = e.git(ctx, repo, "update-ref", ref, unrelated); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"reflog", "expire", "--expire=now", "--all"}, {"gc", "--prune=now", "--quiet"}} {
		if _, err = e.git(ctx, repo, args...); err != nil {
			t.Fatal(err)
		}
	}
}

func requireHistory(t *testing.T, e *Engine, from, to string) {
	t.Helper()
	patch, err := e.RunPatch(t.Context(), "run1", PatchRequest{From: from, To: to})
	if err != nil || !strings.Contains(patch.Text, "+// second session edit") || !strings.Contains(patch.Text, "+second") {
		t.Fatalf("recorded interval = %+v, %v", patch, err)
	}
	patch, err = e.RunPatch(t.Context(), "run1", PatchRequest{})
	if err != nil || !patch.Recorded || !strings.Contains(patch.Text, "+// first session edit") || !strings.Contains(patch.Text, "+++ b/notes.txt") {
		t.Fatalf("recorded diff against the fork point = %+v, %v", patch, err)
	}
}

func TestRemovedCheckoutHistoryBorrowsOnlyTheForkPoint(t *testing.T) {
	e := newTestEngine(t, nil)
	seedSources(t, e, "ws1")
	ctx := t.Context()
	from, to := sessionHistory(t, e, "run1")
	store, err := e.snapshotStorePath("run1")
	if err != nil {
		t.Fatal(err)
	}
	roots, _, err := e.gitBareBounded(ctx, store, -1, "for-each-ref", "--format=%(objectname)", "refs/aether/")
	if err != nil {
		t.Fatal(err)
	}
	// What the store held after a checkout removal before it borrowed the
	// fork point: a pack of everything its refs reach.
	cmd := exec.Command("git", "-C", store, "pack-objects", "--revs", "--stdout", "-q")
	cmd.Stdin = strings.NewReader(roots)
	pack, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if err = e.RemoveRunCheckout(ctx, "run1"); err != nil {
		t.Fatal(err)
	}
	shared := objectBytes(t, store)
	t.Logf("history store of a two-file session: %d bytes, %d with its own copy of the fork point", shared, len(pack))
	if shared*20 > int64(len(pack)) {
		t.Fatalf("the store holds %d bytes, more than a twentieth of the %d a copy of the fork point takes", shared, len(pack))
	}
	repo, err := e.existingRepoPath("ws1")
	if err != nil {
		t.Fatal(err)
	}
	// A checkout read that raced the removal leaves the store as it is.
	if _, err = e.scratchObjects(store, filepath.Join(filepath.Dir(store), "run1"), "run1"); err != nil {
		t.Fatal(err)
	}
	if got, err := e.borrowedRepo(store); err != nil || got != repo {
		t.Fatalf("the store borrows from %q, %v, not the workspace repository", got, err)
	}
	orphanForkPoint(t, e, repo)
	requireHistory(t, e, from, to)

	// The store names the repository by a relative path, so a data
	// directory moved elsewhere keeps the history.
	root := filepath.Dir(e.cfg.ReposDir)
	if err = os.Rename(root, root+"-moved"); err != nil {
		t.Fatal(err)
	}
	moved := e.cfg
	moved.ReposDir, moved.CheckoutsDir = filepath.Join(root+"-moved", "repos"), filepath.Join(root+"-moved", "checkouts")
	movedEngine, err := New(moved)
	if err != nil {
		t.Fatal(err)
	}
	requireHistory(t, movedEngine, from, to)
	if err = errors.Join(movedEngine.Close(), os.Rename(root+"-moved", root)); err != nil {
		t.Fatal(err)
	}

	base, _, err := e.gitBareBounded(ctx, store, 256, "rev-parse", "refs/aether/base")
	if err != nil {
		t.Fatal(err)
	}
	if err = e.RemoveRunHistory(ctx, "run1"); err != nil {
		t.Fatal(err)
	}
	if pins, _ := e.git(ctx, repo, "for-each-ref", historyPinRoot); pins != "" {
		t.Fatalf("deleted history left its pin: %s", pins)
	}
	orphanForkPoint(t, e, repo)
	if e.hasObject(ctx, repo, strings.TrimSpace(base)) {
		t.Fatal("the fork point survived a prune with no pin, so nothing above proved the pin kept it")
	}
}

func TestShareDetachedHistoryShrinksStoresThatCopiedTheForkPoint(t *testing.T) {
	e := newTestEngine(t, nil)
	seedSources(t, e, "ws1")
	ctx := t.Context()
	from, to := sessionHistory(t, e, "run1")
	store, err := e.snapshotStorePath("run1")
	if err != nil {
		t.Fatal(err)
	}
	checkout, err := e.checkoutPath("run1")
	if err != nil {
		t.Fatal(err)
	}
	// The checkout removal of servers before the store borrowed its fork
	// point: everything the refs reach copied in, alternates dropped.
	roots, _, err := e.gitBareBounded(ctx, store, -1, "for-each-ref", "--format=%(objectname)", "refs/aether/")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.gitInput(ctx, store, e.gitEnv(), []byte(roots), "pack-objects", "--revs", "--non-empty", filepath.Join(store, "objects", "pack", "pack")); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(store, "objects", "info", "alternates"), checkout, checkout + ".json"} {
		if err = os.RemoveAll(path); err != nil {
			t.Fatal(err)
		}
	}
	whole := objectBytes(t, store)

	e, err = New(e.cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })
	if err = e.ShareDetachedHistory(ctx); err != nil {
		t.Fatal(err)
	}
	shared := objectBytes(t, store)
	t.Logf("history store of a two-file session: %d bytes before the share, %d after", whole, shared)
	if shared*20 > whole {
		t.Fatalf("the store shrank from %d bytes to only %d", whole, shared)
	}
	repo, err := e.existingRepoPath("ws1")
	if err != nil {
		t.Fatal(err)
	}
	if pin, _ := e.git(ctx, repo, "for-each-ref", "--format=%(refname)", historyPinRoot); pin != historyPinRoot+"run1" {
		t.Fatalf("pins = %q", pin)
	}
	orphanForkPoint(t, e, repo)
	requireHistory(t, e, from, to)
	if err = e.ShareDetachedHistory(ctx); err != nil {
		t.Fatal(err)
	}
	if again := objectBytes(t, store); again != shared {
		t.Fatalf("a second share rewrote the store: %d bytes, then %d", shared, again)
	}

	// The workspace goes while another workspace's session still shows
	// this history, as an extra repository's does.
	if err = e.RemoveWorkspaceRepo(ctx, "ws1"); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(store, "objects", "info", "alternates")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("history still borrows from a removed workspace: %v", err)
	}
	requireHistory(t, e, from, to)
	// A removal that stopped halfway leaves a directory git cannot read.
	if err = os.MkdirAll(filepath.Join(repo, "objects"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err = e.RemoveWorkspaceRepo(ctx, "ws1"); err != nil {
		t.Fatalf("removing what is left of a workspace repository: %v", err)
	}
}
