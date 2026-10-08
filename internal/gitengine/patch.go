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
	// Base is what the diff is taken against: the recorded fork point for a
	// cumulative render, the from tree for an interval render.
	Base string
	// Text is the unified diff, empty when nothing changed against Base.
	Text string
	// Truncated reports that the diff outgrew the byte limit and Text ends
	// early, at the last whole line that fit.
	Truncated bool
	// Recorded means the checkout is gone and this is its last recorded
	// cumulative snapshot, not a live working-tree render.
	Recorded bool
}

// PatchRequest names one rendering. From and To are empty for the run's
// current diff against its fork point; set to snapshot trees recorded by
// run.diff events they render what one interval changed. MaxBytes caps the
// patch text when positive; zero uses MaxPatchBytes.
type PatchRequest struct {
	From     string
	To       string
	MaxBytes int
}

// RunPatch renders a run's live or recorded diff.
//
// With an empty range and an available checkout it covers committed work,
// uncommitted edits and untracked files against the recorded fork point.
// After checkout cleanup it returns the last recorded cumulative snapshot
// with Recorded set. With both range ends set it renders that exact interval;
// one end alone is ErrInvalidObjectID because a range needs both.
//
// It is read-only from the agent's point of view: the worktree is staged
// into a scratch index, and the blobs staging hashes go to a scratch object
// directory beside it, with the checkout's own objects readable as an
// alternate. Nothing under the checkout's .git is written.
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

	if req.From != "" || req.To != "" {
		return e.rangePatch(ctx, run, checkout, req.From, req.To, maxBytes)
	}
	if _, checkoutErr := e.existingCheckoutPath(run); checkoutErr != nil {
		if errors.Is(checkoutErr, ErrCheckoutNotFound) {
			return e.recordedPatch(ctx, run, maxBytes)
		}
		return Patch{}, checkoutErr
	}

	meta, err := e.readRunMeta(run)
	if err != nil {
		return Patch{}, err
	}
	if err = e.checkCaptureBounds(ctx, run, checkout, MaxSnapshotInputBytes, ErrSnapshotStorageLimit); err != nil {
		return Patch{}, err
	}
	dir, err := os.MkdirTemp("", "aether-patch-")
	if err != nil {
		return Patch{}, fmt.Errorf("gitengine: scratch index for run %s: %w", run, err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	index, err := scratchIndex(dir, checkout, run)
	if err != nil {
		return Patch{}, err
	}
	if _, _, addErr := e.gitStaged(ctx, checkout, index, 0, "add", "-A"); addErr != nil {
		return Patch{}, addErr
	}
	if err = e.checkStagedBounds(ctx, checkout, index); err != nil {
		return Patch{}, err
	}
	text, truncated, err := e.gitStaged(ctx, checkout, index, maxBytes,
		"diff", "--cached", "--no-color", "--no-renames", meta.Base)
	if err != nil {
		return Patch{}, err
	}
	return Patch{Base: meta.Base, Text: trimToLastLine(text, truncated), Truncated: truncated}, nil
}

// rangePatch renders one snapshot tree against another out of the run's own
// snapshot store. Besides being complete tree object IDs, both endpoints must
// belong to recorded run history. Merely resolving through checkout alternates
// does not make an arbitrary source-history tree a recorded snapshot.
func (e *Engine) rangePatch(ctx context.Context, run domain.RunID, checkout, from, to string, maxBytes int) (Patch, error) {
	lock := e.snapshotLock(run)
	lock.Lock()
	defer lock.Unlock()
	if from == "" || to == "" {
		return Patch{}, fmt.Errorf("%w: a snapshot range needs both ends", ErrInvalidObjectID)
	}
	for _, id := range []string{from, to} {
		if !validObjectID(id) {
			return Patch{}, fmt.Errorf("%w: %q", ErrInvalidObjectID, id)
		}
	}
	store, err := e.snapshotStorePath(run)
	if err != nil {
		return Patch{}, err
	}
	// Rendering never recreates an explicitly deleted history store.
	if _, statErr := os.Stat(store); statErr != nil {
		return Patch{}, fmt.Errorf("%w: run %s has no snapshot store", ErrSnapshotTreeMissing, run)
	}
	// Quiet pre-upgrade runs migrate their published boundary on a read.
	if _, err = os.Stat(filepath.Join(store, lastTreeFile)); err == nil {
		if err = e.initSnapshotStore(ctx, run, checkout, store); err != nil {
			return Patch{}, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return Patch{}, err
	}
	recorded, err := e.snapshotRangeRecorded(ctx, store, from, to)
	if err != nil {
		return Patch{}, err
	}
	if !recorded {
		// Preserve the distinction between invalid object types and missing
		// history before attempting legacy event-log recovery.
		for _, id := range []string{from, to} {
			if resolveErr := e.requireSnapshotTree(ctx, store, id); resolveErr != nil {
				return Patch{}, resolveErr
			}
		}
		if e.cfg.EventLog != nil {
			meta, metaErr := e.readRunMeta(run)
			if metaErr != nil && !errors.Is(metaErr, ErrRunMetadataNotFound) {
				return Patch{}, metaErr
			}
			if metaErr == nil {
				if retainErr := e.retainRecordedSnapshotTrees(ctx, meta.Workspace, run, store); retainErr != nil {
					return Patch{}, retainErr
				}
				recorded, err = e.snapshotRangeRecorded(ctx, store, from, to)
				if err != nil {
					return Patch{}, err
				}
			}
		}
		if !recorded {
			return Patch{}, fmt.Errorf("%w: range endpoints are not recorded for run %s", ErrSnapshotTreeMissing, run)
		}
	}
	return e.snapshotPatch(ctx, store, from, to, maxBytes)
}

func (e *Engine) snapshotRangeRecorded(ctx context.Context, store, from, to string) (bool, error) {
	// Filter in Git so the response contains only refs for these endpoints,
	// not the run's entire indefinitely retained catalog.
	refs, _, err := e.gitBareBounded(ctx, store, -1, "for-each-ref",
		"--format=%(objectname)", "--points-at="+from, "--points-at="+to,
		snapshotHistoryRoot, "refs/aether/base", "refs/aether/latest", "refs/aether/previous",
		snapshotPublishedRef, snapshotInflightParentRef, snapshotInflightTreeRef)
	if err != nil {
		return false, err
	}
	var haveFrom, haveTo bool
	for id := range strings.FieldsSeq(refs) {
		haveFrom = haveFrom || id == from
		haveTo = haveTo || id == to
	}
	return haveFrom && haveTo, nil
}

func (e *Engine) snapshotPatch(ctx context.Context, store, from, to string, maxBytes int) (Patch, error) {
	for _, id := range []string{from, to} {
		if resolveErr := e.requireSnapshotTree(ctx, store, id); resolveErr != nil {
			return Patch{}, resolveErr
		}
	}
	// Match live staging's attribute isolation: a captured .gitattributes
	// must not change how history is rendered.
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

func (e *Engine) recordedPatch(ctx context.Context, run domain.RunID, maxBytes int) (Patch, error) {
	lock := e.snapshotLock(run)
	lock.Lock()
	defer lock.Unlock()
	store, err := e.snapshotStorePath(run)
	if err != nil {
		return Patch{}, err
	}
	if _, statErr := os.Stat(filepath.Join(store, "HEAD")); errors.Is(statErr, os.ErrNotExist) {
		return Patch{}, ErrSnapshotTreeMissing
	} else if statErr != nil {
		return Patch{}, statErr
	}
	refs, _, err := e.gitBareBounded(ctx, store, 1024, "for-each-ref", "--format=%(refname) %(objectname)",
		"refs/aether/base", snapshotPublishedRef, "refs/aether/latest")
	if err != nil {
		return Patch{}, err
	}
	var base, published, latest string
	for line := range strings.SplitSeq(refs, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		switch fields[0] {
		case "refs/aether/base":
			base = fields[1]
		case snapshotPublishedRef:
			published = fields[1]
		case "refs/aether/latest":
			latest = fields[1]
		}
	}
	if published == "" {
		published = latest
	}
	if base == "" || published == "" {
		return Patch{}, ErrSnapshotTreeMissing
	}
	patch, err := e.snapshotPatch(ctx, store, base, published, maxBytes)
	patch.Recorded = true
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
