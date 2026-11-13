package vm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"
)

const (
	// freezeTimeout is how long the guest keeps its root filesystem frozen
	// when no thaw comes, as after a server crash, and so the longest a
	// copy may take.
	freezeTimeout = 5 * time.Minute
	// freezeCallTimeout covers flushing the guest's dirty pages.
	freezeCallTimeout = time.Minute
)

// commit saves the VM's root disk as a new base disk tagged ref. A running
// VM's root filesystem is frozen only while its copy-on-write layer is
// copied, so the copy is whole and every process keeps running; the base is
// built from the copy afterwards.
func (e *engine) commit(ctx context.Context, key, ref string, labels map[string]string) error {
	unlock := e.lock(key)
	defer unlock()
	rec, err := e.load(key)
	if err != nil {
		return err
	}
	up, err := e.running(ctx, key)
	if err != nil {
		return err
	}
	disk := rec.Root.Path
	if up {
		disk = e.path(key, rootCopy)
		defer func() { _ = os.Remove(disk) }()
		if err = e.copyRoot(ctx, rec, disk); err != nil {
			return fmt.Errorf("vm runtime: save VM %s: %w", key, err)
		}
	}
	if _, err = e.cfg.Images.SaveBase(ctx, disk, ref, labels); err != nil {
		return fmt.Errorf("vm runtime: save VM %s as %s: %w", key, ref, err)
	}
	return nil
}

// copyRoot copies the root disk under a freeze. The VM is not paused: a
// server that dies mid-copy leaves only the freeze, which the guest ends at
// its deadline. A copy that outlasted the deadline is refused.
func (e *engine) copyRoot(ctx context.Context, rec *record, dst string) (err error) {
	if e.paused(ctx, rec.Key) {
		return errors.New("it is paused")
	}
	h, err := e.helper(rec.Key)
	if err != nil {
		return err
	}
	id := processID("save")
	defer func() {
		thawCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), helperCallTimeout)
		defer cancel()
		frozen, thawErr := h.Thaw(thawCtx, id)
		switch {
		case thawErr != nil:
			err = errors.Join(err, fmt.Errorf("its root filesystem stays frozen for up to %s: %w", freezeTimeout, thawErr))
		case err == nil && !frozen:
			err = errTooSlow
		}
	}()
	freezeCtx, cancel := context.WithTimeout(ctx, freezeCallTimeout)
	err = h.Freeze(freezeCtx, id, freezeTimeout)
	cancel()
	if err != nil {
		return err
	}
	if err = os.Remove(dst); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	copyCtx, cancel := context.WithTimeout(ctx, freezeTimeout)
	defer cancel()
	err = e.run(copyCtx, "cp", "--reflink=auto", "--sparse=always", "--", rec.Root.Path, dst)
	if err != nil && ctx.Err() == nil && copyCtx.Err() != nil {
		return errTooSlow
	}
	return err
}

var errTooSlow = fmt.Errorf("the copy took longer than the %s the root filesystem stays frozen; save again", freezeTimeout)
