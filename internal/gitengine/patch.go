package gitengine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/3xDevOps/Aether/internal/domain"
)

const (
	// MaxPatchBytes is the maximum dashboard diff response.
	MaxPatchBytes = 64 << 20
	// DefaultPatchBytes bounds evidence patch output.
	DefaultPatchBytes = 1 << 20
)

// Patch is one rendered diff of a run checkout, as unified patch text.
type Patch struct {
	// Base is what the diff is taken against: the run's effective base commit
	// for a live cumulative render, its tree for a recorded one, the from tree
	// for a render that starts at a recorded tree.
	Base string
	// Text is the unified diff, empty when nothing changed against Base.
	Text string
	// Truncated reports that the diff outgrew the byte limit and Text ends
	// early, at the last whole line that fit.
	Truncated bool
	// Recorded means the checkout is gone and this is its last recorded
	// cumulative snapshot, not a live working-tree render.
	Recorded bool
	// ForkTree is the tree of the commit the run was forked from, set on
	// renders that name no From. Passed as From it renders everything the
	// run has changed since it started, merged upstream work included.
	ForkTree string
}

// PatchRequest names one rendering. From and To are empty for the run's
// current diff against its effective base. With From alone, a snapshot tree
// recorded by a run.diff event, it renders everything that changed since that
// snapshot. With both it renders what that interval changed. MaxBytes caps the
// patch text when positive; zero uses MaxPatchBytes.
type PatchRequest struct {
	From     string
	To       string
	MaxBytes int
}

// RunPatch renders a run's live or recorded diff.
//
// With an empty range and an available checkout it covers committed work,
// uncommitted edits and untracked files against the run's effective base: the
// newest upstream commit it contains, so merged upstream work is not counted as
// the run's. After checkout cleanup it returns the last recorded cumulative snapshot
// with Recorded set. With From alone it renders from that recorded tree to the
// live worktree, or to the last recorded snapshot once the checkout is gone.
// With both ends set it renders that exact interval; To alone is
// ErrInvalidObjectID.
//
// It is read-only from the agent's point of view: the worktree is staged
// into a scratch index, and the blobs staging hashes go to a scratch object
// directory beside it, with the checkout's own objects readable as an
// alternate (and the snapshot store's, for a render that starts at a tree).
// Nothing under the checkout's .git is written.
func (e *Engine) RunPatch(ctx context.Context, run domain.RunID, req PatchRequest) (Patch, error) {
	checkout, err := e.checkoutPath(run)
	if err != nil {
		return Patch{}, err
	}
	maxBytes := req.MaxBytes
	if maxBytes <= 0 {
		maxBytes = MaxPatchBytes
	}
	// Staging re-hashes every untracked file, which the seeded stat cache
	// cannot cover, so a worktree holding a large un-ignored tree makes this
	// arbitrarily slow. Bounded like a diff snapshot's git work, rather than
	// by however long the browser is willing to wait.
	ctx, cancel := context.WithTimeout(ctx, snapshotTimeout)
	defer cancel()

	patch, err := e.renderPatch(ctx, run, checkout, req, maxBytes)
	if err != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		// A git killed by the deadline reports "signal: killed", not the
		// deadline.
		return Patch{}, fmt.Errorf("gitengine: render diff for session %s: %w: %w", run, ctx.Err(), err)
	}
	return patch, err
}

func (e *Engine) renderPatch(ctx context.Context, run domain.RunID, checkout string, req PatchRequest, maxBytes int) (Patch, error) {
	switch {
	case req.To != "" && req.From == "":
		return Patch{}, fmt.Errorf("%w: a snapshot range needs a from tree", ErrInvalidObjectID)
	case req.To != "":
		return e.rangePatch(ctx, run, checkout, req.From, req.To, maxBytes)
	}
	if _, checkoutErr := e.existingCheckoutPath(run); checkoutErr != nil {
		if errors.Is(checkoutErr, ErrCheckoutNotFound) {
			return e.recordedPatch(ctx, run, req.From, maxBytes)
		}
		return Patch{}, checkoutErr
	}

	if req.From != "" {
		store, storeErr := e.recordedStore(ctx, run, checkout, req.From)
		if storeErr != nil {
			return Patch{}, storeErr
		}
		return e.livePatch(ctx, run, checkout, req.From, filepath.Join(store, "objects"), maxBytes)
	}
	meta, err := e.readRunMeta(run)
	if err != nil {
		return Patch{}, err
	}
	head, headErr := e.checkoutHead(ctx, run, checkout)
	if headErr != nil {
		head = ""
	}
	base := e.effectiveBase(ctx, run, checkout, head, meta.Base, meta.BaseBranch)
	patch, err := e.livePatch(ctx, run, checkout, base, "", maxBytes)
	if err != nil {
		return Patch{}, err
	}
	patch.ForkTree, err = e.gitCheckout(ctx, run, checkout, "rev-parse", meta.Base+"^{tree}")
	if err != nil {
		return Patch{}, err
	}
	return patch, nil
}

// livePatch renders the checkout's worktree against base, a commit or tree.
// snapshotObjects is a snapshot store's object directory to read base from
// when it is a recorded tree, empty for a commit the checkout holds.
func (e *Engine) livePatch(ctx context.Context, run domain.RunID, checkout, base, snapshotObjects string, maxBytes int) (Patch, error) {
	if err := e.checkCaptureBounds(ctx, run, checkout, MaxSnapshotInputBytes, ErrSnapshotStorageLimit); err != nil {
		return Patch{}, err
	}
	dir, err := os.MkdirTemp("", "aether-patch-")
	if err != nil {
		return Patch{}, fmt.Errorf("gitengine: scratch index for session %s: %w", run, err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	index, err := scratchIndex(dir, checkout, run)
	if err != nil {
		return Patch{}, err
	}
	if snapshotObjects != "" {
		if err = addAlternate(dir, snapshotObjects); err != nil {
			return Patch{}, fmt.Errorf("gitengine: scratch alternates for session %s: %w", run, err)
		}
	}
	if _, _, addErr := e.gitStaged(ctx, checkout, index, 0, "add", "-A"); addErr != nil {
		return Patch{}, addErr
	}
	if err = e.checkStagedBounds(ctx, checkout, index); err != nil {
		return Patch{}, err
	}
	text, truncated, err := e.gitStaged(ctx, checkout, index, maxBytes,
		"diff", "--cached", "--no-color", "--no-renames", base)
	if err != nil {
		return Patch{}, err
	}
	return Patch{Base: base, Text: trimToLastLine(text, truncated), Truncated: truncated}, nil
}

// addAlternate lets the scratch store at dir read another object directory.
func addAlternate(dir, objects string) error {
	f, err := os.OpenFile(filepath.Join(dir, "objects", "info", "alternates"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	if _, err = f.WriteString(objects + "\n"); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// rangePatch renders one snapshot tree against another out of the run's own
// snapshot store. Besides being complete tree object IDs, both endpoints must
// belong to recorded run history. Merely resolving through checkout alternates
// does not make an arbitrary source-history tree a recorded snapshot.
func (e *Engine) rangePatch(ctx context.Context, run domain.RunID, checkout, from, to string, maxBytes int) (Patch, error) {
	lock := e.snapshotLock(run)
	lock.Lock()
	defer lock.Unlock()
	store, err := e.recordedStoreLocked(ctx, run, checkout, from, to)
	if err != nil {
		return Patch{}, err
	}
	return e.snapshotPatch(ctx, store, from, to, maxBytes)
}

// recordedStore returns the run's snapshot store after proving every id is a
// tree recorded in it.
func (e *Engine) recordedStore(ctx context.Context, run domain.RunID, checkout string, ids ...string) (string, error) {
	lock := e.snapshotLock(run)
	lock.Lock()
	defer lock.Unlock()
	return e.recordedStoreLocked(ctx, run, checkout, ids...)
}

func (e *Engine) recordedStoreLocked(ctx context.Context, run domain.RunID, checkout string, ids ...string) (string, error) {
	for _, id := range ids {
		if !validObjectID(id) {
			return "", fmt.Errorf("%w: %q", ErrInvalidObjectID, id)
		}
	}
	store, err := e.snapshotStorePath(run)
	if err != nil {
		return "", err
	}
	// Rendering never recreates an explicitly deleted history store.
	if _, statErr := os.Stat(store); statErr != nil {
		return "", fmt.Errorf("%w: session %s has no snapshot store", ErrSnapshotTreeMissing, run)
	}
	// Quiet pre-upgrade runs migrate their published boundary on a read.
	if _, err = os.Stat(filepath.Join(store, lastTreeFile)); err == nil {
		if err = e.initSnapshotStore(ctx, run, checkout, store); err != nil {
			return "", err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	recorded, err := e.snapshotRangeRecorded(ctx, store, ids...)
	if err != nil {
		return "", err
	}
	if !recorded {
		// Preserve the distinction between invalid object types and missing
		// history before attempting legacy event-log recovery.
		for _, id := range ids {
			if resolveErr := e.requireSnapshotTree(ctx, store, id); resolveErr != nil {
				return "", resolveErr
			}
		}
		if e.cfg.EventLog != nil {
			meta, metaErr := e.readRunMeta(run)
			if metaErr != nil && !errors.Is(metaErr, ErrRunMetadataNotFound) {
				return "", metaErr
			}
			if metaErr == nil {
				if retainErr := e.retainRecordedSnapshotTrees(ctx, meta.Workspace, run, store); retainErr != nil {
					return "", retainErr
				}
				recorded, err = e.snapshotRangeRecorded(ctx, store, ids...)
				if err != nil {
					return "", err
				}
			}
		}
		if !recorded {
			return "", fmt.Errorf("%w: endpoints are not recorded for session %s", ErrSnapshotTreeMissing, run)
		}
	}
	return store, nil
}

func (e *Engine) snapshotRangeRecorded(ctx context.Context, store string, ids ...string) (bool, error) {
	// Filter in Git so the response contains only refs for these endpoints,
	// not the run's entire indefinitely retained catalog.
	args := []string{"for-each-ref", "--format=%(objectname)"}
	for _, id := range ids {
		args = append(args, "--points-at="+id)
	}
	args = append(args, snapshotHistoryRoot, "refs/aether/base", "refs/aether/latest", "refs/aether/previous",
		snapshotPublishedRef, snapshotInflightParentRef, snapshotInflightTreeRef)
	refs, _, err := e.gitBareBounded(ctx, store, -1, args...)
	if err != nil {
		return false, err
	}
	found := make(map[string]bool)
	for id := range strings.FieldsSeq(refs) {
		found[id] = true
	}
	for _, id := range ids {
		if !found[id] {
			return false, nil
		}
	}
	return true, nil
}

func (e *Engine) snapshotPatch(ctx context.Context, store, from, to string, maxBytes int) (Patch, error) {
	for _, id := range []string{from, to} {
		if resolveErr := e.requireSnapshotTree(ctx, store, id); resolveErr != nil {
			return Patch{}, resolveErr
		}
	}
	// Stored trees need no conversion, and a captured .gitattributes must
	// not change how history is rendered.
	emptyTree, err := e.git(ctx, store, "hash-object", "-t", "tree", os.DevNull)
	if err != nil {
		return Patch{}, err
	}
	text, truncated, err := e.gitBareBoundedEnv(ctx, store, append(gitEnv(), "GIT_ATTR_SOURCE="+emptyTree), maxBytes,
		"diff", "--no-ext-diff", "--no-textconv", "--no-color", "--no-renames", from, to, "--")
	if err != nil {
		return Patch{}, err
	}
	return Patch{Base: from, Text: trimToLastLine(text, truncated), Truncated: truncated}, nil
}

// recordedPatch renders a removed checkout's last recorded snapshot against
// the effective base, or against the recorded tree from when it is set.
func (e *Engine) recordedPatch(ctx context.Context, run domain.RunID, from string, maxBytes int) (Patch, error) {
	lock := e.snapshotLock(run)
	lock.Lock()
	defer lock.Unlock()
	store, err := e.snapshotStorePath(run)
	if err != nil {
		return Patch{}, err
	}
	if from != "" {
		checkout, checkoutErr := e.checkoutPath(run)
		if checkoutErr != nil {
			return Patch{}, checkoutErr
		}
		if _, err = e.recordedStoreLocked(ctx, run, checkout, from); err != nil {
			return Patch{}, err
		}
	}
	if _, statErr := os.Stat(filepath.Join(store, "HEAD")); errors.Is(statErr, os.ErrNotExist) {
		return Patch{}, ErrSnapshotTreeMissing
	} else if statErr != nil {
		return Patch{}, statErr
	}
	refs, _, err := e.gitBareBounded(ctx, store, 1024, "for-each-ref", "--format=%(refname) %(objectname)",
		"refs/aether/base", snapshotUpstreamRef, snapshotPublishedRef, "refs/aether/latest")
	if err != nil {
		return Patch{}, err
	}
	var base, upstream, published, latest string
	for line := range strings.SplitSeq(refs, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		switch fields[0] {
		case "refs/aether/base":
			base = fields[1]
		case snapshotUpstreamRef:
			upstream = fields[1]
		case snapshotPublishedRef:
			published = fields[1]
		case "refs/aether/latest":
			latest = fields[1]
		}
	}
	forkTree := base
	if upstream != "" {
		base = upstream
	}
	if from != "" {
		base = from
		forkTree = ""
	}
	if published == "" {
		published = latest
	}
	if base == "" || published == "" {
		return Patch{}, ErrSnapshotTreeMissing
	}
	patch, err := e.snapshotPatch(ctx, store, base, published, maxBytes)
	patch.Recorded = true
	patch.ForkTree = forkTree
	return patch, err
}

// trimToLastLine cuts truncated patch text back to its last whole line, so
// a reader never sees half a hunk header.
func trimToLastLine(text string, truncated bool) string {
	if !truncated {
		return text
	}
	if i := strings.LastIndexByte(text, '\n'); i >= 0 {
		return text[:i+1]
	}
	return text
}
