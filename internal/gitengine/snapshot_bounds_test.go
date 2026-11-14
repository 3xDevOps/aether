package gitengine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// boundsEngine is a minimal engine over a temp data directory for the
// snapshot-bound checks, which shell out to the real git CLI.
func boundsEngine(t *testing.T) *Engine {
	t.Helper()
	dir := t.TempDir()
	e, err := New(Config{ReposDir: filepath.Join(dir, "repos"), CheckoutsDir: filepath.Join(dir, "checkouts")})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })
	return e
}

// mktree builds one Git tree object in store from a mktree spec.
func mktree(t *testing.T, store, spec string) string {
	t.Helper()
	cmd := exec.Command("git", "-C", store, "mktree")
	cmd.Stdin = strings.NewReader(spec)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git mktree: %v\n%s", err, out)
	}
	return strings.TrimSpace(string(out))
}

// hashBlob writes one blob into store and returns its id.
func hashBlob(t *testing.T, store string, data []byte) string {
	t.Helper()
	cmd := exec.Command("git", "-C", store, "hash-object", "-w", "--stdin")
	cmd.Stdin = bytes.NewReader(data)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git hash-object: %v\n%s", err, out)
	}
	return strings.TrimSpace(string(out))
}

// TestCheckTreeBoundsStopsTreeBomb builds a tree whose recursive expansion is
// enormous from a handful of objects and checks that checkTreeBounds refuses
// it quickly. The empty-folder case has no blob to list, so only the -t that
// prints every descended subtree keeps its listing growing past the bound.
func TestCheckTreeBoundsStopsTreeBomb(t *testing.T) {
	for _, leafKind := range []string{"blob", "tree"} {
		t.Run(leafKind+" leaves", func(t *testing.T) {
			e := boundsEngine(t)
			store := t.TempDir()
			if _, err := e.git(t.Context(), "", "init", "--bare", "--quiet", store); err != nil {
				t.Fatalf("init store: %v", err)
			}
			mode := map[string]string{"blob": "100644", "tree": "040000"}
			fanOut := func(child, kind string) string {
				var spec strings.Builder
				for i := 0; i < 100; i++ {
					fmt.Fprintf(&spec, "%s %s %s\td%03d\n", mode[kind], kind, child, i)
				}
				return mktree(t, store, spec.String())
			}
			var leaf string
			if leafKind == "blob" {
				leaf = hashBlob(t, store, []byte("x"))
			} else {
				leaf = mktree(t, store, "") // the empty tree
			}
			tree := fanOut(leaf, leafKind)
			for i := 0; i < 4; i++ { // depth 5: 100^5 = 1e10 paths from a few objects
				tree = fanOut(tree, "tree")
			}

			// A small limit keeps the test cheap: git is cancelled as soon as
			// its listing passes the bound, a couple of MiB in.
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			start := time.Now()
			err := e.checkTreeBounds(ctx, store, tree, 1<<20, ErrSnapshotStorageLimit)
			elapsed := time.Since(start)
			if !errors.Is(err, ErrSnapshotStorageLimit) {
				t.Fatalf("checkTreeBounds = %v, want ErrSnapshotStorageLimit", err)
			}
			if elapsed > 15*time.Second {
				t.Fatalf("checkTreeBounds took %s; the tree bomb was not cut short", elapsed)
			}
		})
	}
}

// TestCheckTreeBoundsRefusesOversizeContent rejects a tree whose blob sizes
// sum past the limit, and accepts one within it.
func TestCheckTreeBoundsRefusesOversizeContent(t *testing.T) {
	e := boundsEngine(t)
	store := t.TempDir()
	if _, err := e.git(t.Context(), "", "init", "--bare", "--quiet", store); err != nil {
		t.Fatalf("init store: %v", err)
	}
	big := hashBlob(t, store, bytes.Repeat([]byte("A"), 3<<20))
	tree := mktree(t, store, fmt.Sprintf("100644 blob %s\tbig\n", big))

	if err := e.checkTreeBounds(t.Context(), store, tree, 2<<20, ErrSnapshotStorageLimit); !errors.Is(err, ErrSnapshotStorageLimit) {
		t.Fatalf("checkTreeBounds over the limit = %v, want ErrSnapshotStorageLimit", err)
	}
	if err := e.checkTreeBounds(t.Context(), store, tree, 8<<20, ErrSnapshotStorageLimit); err != nil {
		t.Fatalf("checkTreeBounds within the limit = %v, want nil", err)
	}
}

// TestReserveVMReceiveSharesHeadroom proves concurrent receives of VM objects
// share the free-disk headroom: a first receive that claims nearly the whole
// disk leaves the next below the floor, where two independent disk.Free checks
// would each have proceeded.
func TestReserveVMReceiveSharesHeadroom(t *testing.T) {
	e := boundsEngine(t)
	dir := t.TempDir()
	grant, err := e.reserveVMReceive(dir, 1<<62)
	if err != nil {
		t.Fatalf("first reserve: %v", err)
	}
	if grant <= 0 {
		t.Fatalf("first grant = %d, want > 0", grant)
	}
	if _, err := e.reserveVMReceive(dir, 1<<20); !errors.Is(err, ErrSnapshotStorageLimit) {
		t.Fatalf("second reserve while the first holds the headroom = %v, want ErrSnapshotStorageLimit", err)
	}
	if err := e.checkVMReceiveSpace(dir); !errors.Is(err, ErrSnapshotStorageLimit) {
		t.Fatalf("checkVMReceiveSpace while reserved = %v, want ErrSnapshotStorageLimit", err)
	}
	e.releaseVMReceive(grant)
	if _, err := e.reserveVMReceive(dir, 1<<20); err != nil {
		t.Fatalf("reserve after release: %v", err)
	}
}
