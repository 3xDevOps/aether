package gitengine

import (
	"context"

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

// FlushDiffWatch takes the watch's last snapshot, as StopDiffWatch does,
// and starts watching again.
func (e *Engine) FlushDiffWatch(ctx context.Context, workspace domain.WorkspaceID, key domain.RunID) error {
	e.stopDiffWatch(key, true)
	return e.startDiffWatch(ctx, workspace, key)
}
