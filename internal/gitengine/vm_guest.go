package gitengine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/guest"
)

// The git service in a VM runs an Engine over the checkouts on the VM's
// disks; these are the parts of it only that engine uses.

// AdoptCheckout records the identity of the checkout c names, which is
// already at <CheckoutsDir>/<c.Key>.
func (e *Engine) AdoptCheckout(c guest.Checkout) error {
	return e.writeRunMeta(domain.RunID(c.Key), runMeta{
		Base: c.Base, Branch: c.Branch, BaseBranch: c.BaseBranch, Workspace: domain.WorkspaceID(c.Workspace),
	})
}

// CheckoutWorkspace returns the workspace of key's checkout, which
// AdoptCheckout recorded.
func (e *Engine) CheckoutWorkspace(key domain.RunID) (domain.WorkspaceID, error) {
	meta, err := e.readRunMeta(key)
	return meta.Workspace, err
}

// CheckoutHead returns the commit key's checkout HEAD names.
func (e *Engine) CheckoutHead(ctx context.Context, key domain.RunID) (string, error) {
	checkout, err := e.existingCheckoutPath(key)
	if err != nil {
		return "", err
	}
	return e.checkoutHead(ctx, key, checkout)
}

// WatchCheckout starts the diff watch of key's checkout alone, unless it
// runs.
func (e *Engine) WatchCheckout(ctx context.Context, workspace domain.WorkspaceID, key domain.RunID) error {
	return e.startDiffWatch(ctx, workspace, key)
}

// FinishWatch takes the diff watch's last snapshot of key's checkout and
// stops it, as StopDiffWatch does.
func (e *Engine) FinishWatch(key domain.RunID) {
	e.stopDiffWatch(key, true)
}

// StageCheckout writes a tree of key's whole checkout and returns it with
// the commit HEAD names. For evidence it keeps the evidence bound; for a
// commit it stages over a copy of the checkout's own index, as commitAll
// stages into that index, and keeps the copy for CommitIndex.
func (e *Engine) StageCheckout(ctx context.Context, key domain.RunID, evidence bool) (tree, head string, err error) {
	checkout, err := e.existingCheckoutPath(key)
	if err != nil {
		return "", "", err
	}
	lock := e.snapshotLock(key)
	lock.Lock()
	defer lock.Unlock()
	if evidence {
		if err = e.checkEvidenceCaptureBounds(ctx, key, checkout); err != nil {
			return "", "", err
		}
		tree, err = e.writeSnapshotTreeLocked(ctx, key, checkout)
	} else {
		tree, err = e.stageCommitLocked(ctx, key, checkout)
	}
	if err != nil {
		return "", "", err
	}
	head, err = e.checkoutHead(ctx, key, checkout)
	return tree, head, err
}

const commitIndexName = "commit-index"

// maxCommitIndexBytes bounds the checkout index a commit stages over.
const maxCommitIndexBytes = 1 << 30

// stageCommitLocked has no size bound, as commitAll has none.
func (e *Engine) stageCommitLocked(ctx context.Context, key domain.RunID, checkout string) (string, error) {
	store, err := e.snapshotStorePath(key)
	if err != nil {
		return "", err
	}
	if err = e.initSnapshotStore(ctx, key, checkout, store); err != nil {
		return "", err
	}
	index := filepath.Join(store, commitIndexName)
	data, err := e.readCheckoutGitFile(checkout, "index", maxCommitIndexBytes)
	switch {
	case errors.Is(err, os.ErrNotExist):
		err = os.Remove(index)
		if errors.Is(err, os.ErrNotExist) {
			err = nil
		}
	case err == nil:
		err = os.WriteFile(index, data, 0o600)
	}
	if err != nil {
		return "", fmt.Errorf("gitengine: copy the index of %s: %w", key, err)
	}
	if _, _, err = e.gitStaged(ctx, checkout, index, 0, "add", "-A"); err != nil {
		return "", err
	}
	out, _, err := e.gitStaged(ctx, checkout, index, 256, "write-tree")
	if err != nil {
		return "", err
	}
	tree := strings.TrimSpace(out)
	if !validObjectID(tree) {
		return "", fmt.Errorf("gitengine: git write-tree for session %s returned %q, not an object id", key, tree)
	}
	return tree, nil
}

// CommitIndex returns the index StageCheckout last staged a commit in,
// provided it still holds tree.
func (e *Engine) CommitIndex(ctx context.Context, key domain.RunID, tree string) ([]byte, error) {
	checkout, err := e.existingCheckoutPath(key)
	if err != nil {
		return nil, err
	}
	store, err := e.snapshotStorePath(key)
	if err != nil {
		return nil, err
	}
	lock := e.snapshotLock(key)
	lock.Lock()
	defer lock.Unlock()
	index := filepath.Join(store, commitIndexName)
	out, _, err := e.gitStaged(ctx, checkout, index, 256, "write-tree")
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(out) != tree {
		return nil, fmt.Errorf("gitengine: the staged index of %s no longer holds tree %s", key, tree)
	}
	return os.ReadFile(index)
}
