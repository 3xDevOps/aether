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
	path := filepath.Join(store, lastTreeFile)
	if err := os.WriteFile(path, []byte(tree+"\n"), 0o600); err != nil {
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

// snapshotTreeExists reports whether id names a tree the run's snapshot
// store can resolve. An id the store no longer holds - or one that is not a
// tree - is ErrSnapshotTreeMissing: git rev-parse --verify --quiet exits 1
// for that, which is an answer, not a failure. cat-file is not used here
// because it exits 128 on a missing object, indistinguishable from a real
// git failure.
func (e *Engine) snapshotTreeExists(ctx context.Context, checkout, index, id string) error {
	_, _, err := e.gitStaged(ctx, checkout, index, 0, "rev-parse", "--verify", "--quiet", id+"^{tree}")
	if err == nil {
		return nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return fmt.Errorf("%w: %s", ErrSnapshotTreeMissing, id)
	}
	return err
}

// scratchIndex lays out a git scratch object store at dir for the given
// checkout and returns the index file inside it, creating what is missing.
// Both the patch renderer's temporary store and a run's persistent snapshot
// store use this layout.
//
// The checkout's own database is linked in through the scratch directory's
// alternates file, not GIT_ALTERNATE_OBJECT_DIRECTORIES: that env var is
// colon-split with no quoting, so a data dir containing ':' would break it
// apart. Nothing under the checkout's .git is written - crucial because run
// provisioning chowned it to the run's user, and objects or fan-out
// directories created here as the server user would leave the agent unable
// to commit.
func scratchIndex(dir, checkout string, run domain.RunID) (string, error) {
	// Repo discovery checks that the overriding object directory exists.
	objects := filepath.Join(dir, "objects")
	if err := os.MkdirAll(filepath.Join(objects, "info"), 0o700); err != nil {
		return "", fmt.Errorf("gitengine: scratch objects for run %s: %w", run, err)
	}
	alternates := filepath.Join(objects, "info", "alternates")
	if err := os.WriteFile(alternates, []byte(filepath.Join(checkout, ".git", "objects")+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("gitengine: scratch alternates for run %s: %w", run, err)
	}
	index := filepath.Join(dir, "index")
	if _, err := os.Stat(index); err == nil {
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
