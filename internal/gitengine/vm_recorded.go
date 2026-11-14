package gitengine

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/3xDevOps/Aether/internal/domain"
)

// While a session's VM does not run, as when it sleeps or after it ended,
// its checkout is shown from the last snapshot the server recorded of it:
// nothing can have changed the checkout since.

// recordedTrees returns the run's store, its last recorded snapshot tree and
// the tree that snapshot's stats count from.
func (e *Engine) recordedTrees(ctx context.Context, run domain.RunID) (store, tree, base string, err error) {
	store, err = e.snapshotStorePath(run)
	if err != nil {
		return "", "", "", err
	}
	if _, statErr := os.Stat(store); statErr != nil {
		return "", "", "", fmt.Errorf("%w: session %s recorded no snapshot", ErrCheckoutNotFound, run)
	}
	refs, _, err := e.gitBareBounded(ctx, store, 1024, "for-each-ref", "--format=%(refname) %(objectname)",
		"refs/aether/base", snapshotUpstreamRef, snapshotPublishedRef)
	if err != nil {
		return "", "", "", err
	}
	for line := range strings.SplitSeq(refs, "\n") {
		name, id, _ := strings.Cut(line, " ")
		switch name {
		case snapshotPublishedRef:
			tree = id
		case snapshotUpstreamRef:
			base = id
		case "refs/aether/base":
			if base == "" {
				base = id
			}
		}
	}
	if tree == "" {
		tree = base
	}
	if tree == "" {
		return "", "", "", fmt.Errorf("%w: session %s recorded no snapshot", ErrCheckoutNotFound, run)
	}
	return store, tree, base, nil
}

func (e *Engine) recordedFilesTree(ctx context.Context, run domain.RunID, dir string) ([]TreeEntry, error) {
	store, tree, _, err := e.recordedTrees(ctx, run)
	if err != nil {
		return nil, err
	}
	return e.listTreeAt(ctx, store, tree, dir, true)
}

func (e *Engine) recordedFilesRead(ctx context.Context, run domain.RunID, path string, maxBytes int) (FileRead, error) {
	store, tree, _, err := e.recordedTrees(ctx, run)
	if err != nil {
		return FileRead{}, err
	}
	entry, err := e.fileEntryAt(ctx, store, tree, path)
	if err != nil {
		return FileRead{}, err
	}
	content, truncated, size, err := e.readEntry(ctx, store, entry, maxBytes)
	if err != nil {
		return FileRead{}, err
	}
	return fileRead(content, truncated, size), nil
}

func (e *Engine) recordedFileDiff(ctx context.Context, run domain.RunID, path string) (Patch, error) {
	store, tree, base, err := e.recordedTrees(ctx, run)
	if err != nil {
		return Patch{}, err
	}
	emptyTree, err := e.git(ctx, store, "hash-object", "-t", "tree", os.DevNull)
	if err != nil {
		return Patch{}, err
	}
	text, truncated, err := e.gitBareBoundedEnv(ctx, store, append(e.gitEnv(), "GIT_ATTR_SOURCE="+emptyTree), MaxPatchBytes,
		"diff", "--no-ext-diff", "--no-textconv", "--no-color", "--no-renames", base, tree, "--", path)
	if err != nil {
		return Patch{}, err
	}
	return Patch{Base: base, Text: trimToLastLine(text, truncated), Truncated: truncated, Recorded: true}, nil
}
