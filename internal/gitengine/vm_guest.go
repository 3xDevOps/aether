package gitengine

import (
	"context"
	"errors"
	"fmt"
	"io"
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
// runs. A watch it starts records what changed while none ran, as while the
// git service was down, without waiting for another change.
func (e *Engine) WatchCheckout(ctx context.Context, workspace domain.WorkspaceID, key domain.RunID) error {
	return e.startDiffWatch(ctx, workspace, key, true)
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
	// Every writer holds snapshotLock, so an index lock here is stale: left
	// by a git killed with its service.
	if err = os.Remove(index + ".lock"); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("gitengine: clear the stale commit index lock of %s: %w", key, err)
	}
	if err = e.copyCheckoutIndex(checkout, index); err != nil {
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

// CommitIndex copies the index StageCheckout last staged a commit in to w,
// provided it still holds tree. Neither index is held whole: a checkout's
// can be larger than the git service may hold.
func (e *Engine) CommitIndex(ctx context.Context, key domain.RunID, tree string, w io.Writer) error {
	checkout, err := e.existingCheckoutPath(key)
	if err != nil {
		return err
	}
	store, err := e.snapshotStorePath(key)
	if err != nil {
		return err
	}
	lock := e.snapshotLock(key)
	lock.Lock()
	defer lock.Unlock()
	index := filepath.Join(store, commitIndexName)
	out, _, err := e.gitStaged(ctx, checkout, index, 256, "write-tree")
	if err != nil {
		return err
	}
	if strings.TrimSpace(out) != tree {
		return fmt.Errorf("gitengine: the staged index of %s no longer holds tree %s", key, tree)
	}
	f, err := os.Open(index)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	_, err = io.Copy(w, f)
	return err
}

// copyCheckoutIndex makes index a copy of the checkout's own, at most
// maxCommitIndexBytes, or removes it when the checkout has none.
func (e *Engine) copyCheckoutIndex(checkout, index string) error {
	src, err := e.openCheckoutGitFile(checkout, "index")
	if errors.Is(err, os.ErrNotExist) {
		if err = os.Remove(index); errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if err != nil {
		return err
	}
	defer func() { _ = src.Close() }()
	dst, err := os.OpenFile(index, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	n, err := io.Copy(dst, io.LimitReader(src, maxCommitIndexBytes+1))
	if err = errors.Join(err, dst.Close()); err != nil {
		return err
	}
	if n > maxCommitIndexBytes {
		return fmt.Errorf("the checkout's index exceeds %d bytes", maxCommitIndexBytes)
	}
	return nil
}
