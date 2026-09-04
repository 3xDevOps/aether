package gitengine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/3xDevOps/Aether/internal/domain"
)

// snapshotStoreSuffix names the per-run object store that holds diff
// snapshot trees. It sits beside the run checkout rather than inside it,
// the same sidecar convention runMetaPath uses: only <CheckoutsDir>/<runID>
// is mounted into the run container, and the checkout's .git belongs to the
// run's user.
const snapshotStoreSuffix = ".diffsnap"

// lastTreeFile records the most recent snapshot tree inside the store, so
// the interval chain survives a watch restart or a server restart.
const lastTreeFile = "last"

// snapshotStorePath validates run and returns its snapshot store path
// without checking existence.
func (e *Engine) snapshotStorePath(run domain.RunID) (string, error) {
	path, err := e.checkoutPath(run)
	if err != nil {
		return "", err
	}
	return path + snapshotStoreSuffix, nil
}

// writeSnapshotTree stages the whole checkout into the run's snapshot store
// and writes the resulting tree, returning its object id. The store's index
// is reused across snapshots so staging costs a worktree scan, and the
// objects written are only the blobs the checkout's own database does not
// already hold - uncommitted edits - deduplicated by git across snapshots.
func (e *Engine) writeSnapshotTree(ctx context.Context, run domain.RunID, checkout string) (string, error) {
	store, err := e.snapshotStorePath(run)
	if err != nil {
		return "", err
	}
	index, err := scratchIndex(store, checkout, run)
	if err != nil {
		return "", err
	}
	// The store is single-writer: one watch goroutine stages into it, and
	// RemoveRunCheckout stops that watch before deleting the store. A lock
	// file here is therefore stale by construction - left by a git killed at
	// the snapshot timeout, or by a server crash - and leaving it would fail
	// every later staging with "Unable to create index.lock: File exists",
	// killing this run's per-interval diffs until checkout GC.
	if rmErr := os.Remove(index + ".lock"); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
		return "", fmt.Errorf("gitengine: clear stale index lock for run %s: %w", run, rmErr)
	}
	if _, _, addErr := e.gitStaged(ctx, checkout, index, 0, "add", "-A"); addErr != nil {
		return "", addErr
	}
	// A tree id is 64 bytes at most; the cap only keeps a surprising reply
	// from being kept whole.
	out, _, err := e.gitStaged(ctx, checkout, index, 256, "write-tree")
	if err != nil {
		return "", err
	}
	tree := strings.TrimSpace(out)
	if !validObjectID(tree) {
		return "", fmt.Errorf("gitengine: git write-tree for run %s returned %q, not an object id", run, tree)
	}
	return tree, nil
}

// lastSnapshotTree reads the store's record of the most recent snapshot
// tree. An absent, unreadable, or malformed record reads as "unknown"
// rather than an error: the watch then falls back to the fork-point tree.
func (e *Engine) lastSnapshotTree(run domain.RunID) string {
	store, err := e.snapshotStorePath(run)
	if err != nil {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(store, lastTreeFile))
	if err != nil {
		return ""
	}
	tree := strings.TrimSpace(string(data))
	if !validObjectID(tree) {
		return ""
	}
	return tree
}

// setLastSnapshotTree records tree as the store's most recent snapshot.
func (e *Engine) setLastSnapshotTree(run domain.RunID, tree string) error {
	store, err := e.snapshotStorePath(run)
	if err != nil {
		return err
	}
	// Written through a temporary file so a crash mid-write leaves the
	// previous record intact rather than a truncated id that reads as
	// "unknown" and costs the run an interval boundary.
	tmp, err := os.CreateTemp(store, lastTreeFile+"-*.tmp")
	if err != nil {
		return fmt.Errorf("gitengine: record snapshot tree for run %s: %w", run, err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.WriteString(tree + "\n"); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("gitengine: record snapshot tree for run %s: %w", run, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("gitengine: record snapshot tree for run %s: %w", run, err)
	}
	if err := os.Rename(tmp.Name(), filepath.Join(store, lastTreeFile)); err != nil {
		return fmt.Errorf("gitengine: record snapshot tree for run %s: %w", run, err)
	}
	return nil
}

// removeSnapshotStore deletes a run's snapshot store. Idempotent on a
// missing path.
func (e *Engine) removeSnapshotStore(run domain.RunID) error {
	store, err := e.snapshotStorePath(run)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(store); err != nil {
		return fmt.Errorf("gitengine: remove snapshot store for run %s: %w", run, err)
	}
	return nil
}

// requireSnapshotTree checks that id names a tree object the run's snapshot
// store can resolve. An id the store no longer holds is
// ErrSnapshotTreeMissing: git rev-parse --verify --quiet exits 1 for that,
// which is an answer, not a failure, so it is not confused with a git that
// broke. An id that resolves to something else - a commit from the history
// the checkout was cloned with, say - is ErrInvalidObjectID: the ends of a
// range are snapshot trees off run.diff events, and peeling a committish
// here would render diffs the timeline never offered. cat-file cannot carry
// the existence check on its own; it exits 128 on a missing object, which
// is indistinguishable from a real git failure.
func (e *Engine) requireSnapshotTree(ctx context.Context, checkout, index, id string) error {
	// The ^{object} peel is what forces the lookup: rev-parse --verify on a
	// bare full-length id echoes it back without asking the database
	// anything, so the all-zero id would sail through.
	if _, _, err := e.gitStaged(ctx, checkout, index, 0, "rev-parse", "--verify", "--quiet", id+"^{object}"); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return fmt.Errorf("%w: %s", ErrSnapshotTreeMissing, id)
		}
		return err
	}
	out, _, err := e.gitStaged(ctx, checkout, index, 64, "cat-file", "-t", id)
	if err != nil {
		return err
	}
	if kind := strings.TrimSpace(out); kind != "tree" {
		return fmt.Errorf("%w: %s is a %s, not a snapshot tree", ErrInvalidObjectID, id, kind)
	}
	return nil
}

// scratchObjects lays out a git scratch object store at dir for the given
// checkout and returns the index path inside it, which callers pass to
// gitStaged as GIT_INDEX_FILE. The file itself is not created: a command
// that only reads objects (a tree-to-tree diff, an object-type check) never
// touches it. Both the patch renderer's temporary store and a run's
// persistent snapshot store use this layout.
//
// The checkout's own database is linked in through the scratch directory's
// alternates file, not GIT_ALTERNATE_OBJECT_DIRECTORIES: that env var is
// colon-split with no quoting, so a data dir containing ':' would break it
// apart. Nothing under the checkout's .git is written - crucial because run
// provisioning chowned it to the run's user, and objects or fan-out
// directories created here as the server user would leave the agent unable
// to commit.
func scratchObjects(dir, checkout string, run domain.RunID) (string, error) {
	// Repo discovery checks that the overriding object directory exists.
	objects := filepath.Join(dir, "objects")
	if err := os.MkdirAll(filepath.Join(objects, "info"), 0o700); err != nil {
		return "", fmt.Errorf("gitengine: scratch objects for run %s: %w", run, err)
	}
	alternates := filepath.Join(objects, "info", "alternates")
	if err := os.WriteFile(alternates, []byte(filepath.Join(checkout, ".git", "objects")+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("gitengine: scratch alternates for run %s: %w", run, err)
	}
	return filepath.Join(dir, "index"), nil
}

// scratchIndex is scratchObjects plus a staging index, for the commands
// that write one.
func scratchIndex(dir, checkout string, run domain.RunID) (string, error) {
	index, err := scratchObjects(dir, checkout, run)
	if err != nil {
		return "", err
	}
	if _, statErr := os.Stat(index); statErr == nil {
		// A persistent store keeps its own index: re-seeding would throw
		// away the stat cache every snapshot has been refreshing.
		return index, nil
	}
	// Seeding from the checkout's own index carries git's stat cache over,
	// so staging costs a scan of the worktree rather than a rehash of every
	// tracked file. A checkout without one still works.
	data, err := os.ReadFile(filepath.Join(checkout, ".git", "index"))
	if err != nil {
		return index, nil
	}
	if err := os.WriteFile(index, data, 0o600); err != nil {
		return "", fmt.Errorf("gitengine: seed scratch index for run %s: %w", run, err)
	}
	return index, nil
}

// validObjectID reports whether id is a full git object id: 40 hex digits
// (SHA-1) or 64 (SHA-256), lowercase. Client-supplied ids reach git only
// through this check and only inside the run's own snapshot store.
func validObjectID(id string) bool {
	if len(id) != 40 && len(id) != 64 {
		return false
	}
	for _, r := range id {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f':
		default:
			return false
		}
	}
	return true
}
