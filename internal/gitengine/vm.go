package gitengine

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/guest"
)

// VM is where checkouts that live on their session VM's disks come from.
type VM struct {
	Disks VMDisks
	// Guest returns the git service of the VM named vm, the session's run
	// ID; it fails with guest.ErrVMNotRunning when the VM is known not to
	// run.
	Guest func(ctx context.Context, vm domain.RunID) (*guest.Git, error)
}

// VMDisks is the part of the VM image store checkout disks come from.
type VMDisks interface {
	NewCheckoutDisk(ctx context.Context, key string, size int64, fill func(ctx context.Context, dir string) error, path string) error
}

const (
	// vmCheckoutDiskBytes is the size of every checkout filesystem. The
	// disks are sparse, so only what a session writes takes space.
	vmCheckoutDiskBytes = 128 << 30
	vmCheckoutDisk      = "disk.qcow2"
	// PreparedBranch is the branch a prepared checkout disk is on. The VM
	// renames it to the run branch on the checkout's first boot.
	PreparedBranch = "aether-prepared"
	// PreparedDir is the checkout's directory on a prepared disk, which
	// keeps the filesystem's lost+found out of it.
	PreparedDir = "checkout"
)

// createVMCheckout claims the run branch and records the checkout; its
// disk is made when the VM is created, which knows the user who owns it.
func (e *Engine) createVMCheckout(ctx context.Context, repo string, ws domain.WorkspaceID, run domain.RunID, checkoutPath, baseCommit, baseBranch, task, origin string) (string, string, error) {
	branch, err := e.uniqueRunBranch(ctx, repo, baseCommit, run, task)
	if err != nil {
		return "", "", err
	}
	release := func() {
		_ = os.RemoveAll(checkoutPath)
		if _, delErr := e.git(ctx, repo, "update-ref", "-d", "refs/heads/"+branch, baseCommit); delErr != nil {
			slog.Warn("gitengine: release reserved run branch", "run", run, "branch", branch, "error", delErr)
		}
	}
	if err := os.Mkdir(checkoutPath, 0o700); err != nil {
		release()
		return "", "", fmt.Errorf("gitengine: create checkout for session %s: %w", run, err)
	}
	meta := runMeta{Base: baseCommit, Branch: branch, BaseBranch: baseBranch, Workspace: ws, VM: true, Origin: origin}
	if err := e.writeRunMeta(run, meta); err != nil {
		release()
		return "", "", err
	}
	return checkoutPath, branch, nil
}

// IsVMCheckout reports whether run's checkout lives on a VM disk.
func (e *Engine) IsVMCheckout(run domain.RunID) bool {
	meta, err := e.readRunMeta(run)
	return err == nil && meta.VM
}

// VMCheckoutDisk returns the qcow2 disk of the checkout at path, a checkout
// path CreateRunCheckoutAt returned, making it on first use: a copy-on-write
// disk over the prepared disk of the checkout's workspace and base, whose
// files belong to uid:gid. ok is false for a checkout on the server's disk.
func (e *Engine) VMCheckoutDisk(ctx context.Context, path string, uid, gid int) (string, guest.Checkout, bool, error) {
	key, err := e.checkoutRunID(filepath.Clean(path))
	if err != nil {
		return "", guest.Checkout{}, false, nil
	}
	meta, err := e.readRunMeta(key)
	if errors.Is(err, ErrRunMetadataNotFound) {
		return "", guest.Checkout{}, false, nil
	}
	if err != nil {
		return "", guest.Checkout{}, false, err
	}
	if !meta.VM {
		return "", guest.Checkout{}, false, nil
	}
	if e.cfg.VM == nil {
		return "", guest.Checkout{}, false, fmt.Errorf("gitengine: session %s has its checkout in a VM, and this server runs no VMs", key)
	}
	id := guest.Checkout{
		Key: string(key), Workspace: string(meta.Workspace), Branch: meta.Branch,
		Base: meta.Base, BaseBranch: meta.BaseBranch, Origin: meta.Origin, UID: uid, GID: gid,
	}
	lock := e.snapshotLock(key)
	lock.Lock()
	defer lock.Unlock()
	disk := filepath.Join(path, vmCheckoutDisk)
	if _, statErr := os.Stat(disk); statErr == nil {
		return disk, id, true, nil
	} else if !errors.Is(statErr, fs.ErrNotExist) {
		return "", guest.Checkout{}, false, fmt.Errorf("gitengine: checkout disk of session %s: %w", key, statErr)
	}
	repo, err := e.existingRepoPath(meta.Workspace)
	if err != nil {
		return "", guest.Checkout{}, false, err
	}
	prepared := "checkout-" + string(meta.Workspace) + "-" + meta.Base + "-" + strconv.Itoa(uid) + "-" + strconv.Itoa(gid)
	err = e.cfg.VM.Disks.NewCheckoutDisk(ctx, prepared, vmCheckoutDiskBytes, func(buildCtx context.Context, dir string) error {
		return e.fillPreparedCheckout(buildCtx, repo, meta.Base, filepath.Join(dir, PreparedDir), uid, gid)
	}, disk)
	if err != nil {
		return "", guest.Checkout{}, false, fmt.Errorf("gitengine: checkout disk of session %s: %w", key, err)
	}
	return disk, id, true, nil
}

// fillPreparedCheckout makes the checkout a prepared disk holds: a
// self-contained clone at base on PreparedBranch, owned by uid:gid. The
// clone copies the objects, so the ownership change never reaches the
// workspace repository.
func (e *Engine) fillPreparedCheckout(ctx context.Context, repo, base, dir string, uid, gid int) error {
	if _, err := e.git(ctx, "", "clone", "--quiet", "--no-hardlinks", "--no-checkout", repo, dir); err != nil {
		return err
	}
	if _, err := e.git(ctx, dir, "checkout", "--quiet", "-b", PreparedBranch, base); err != nil {
		return err
	}
	return ChownCheckout(dir, uid, gid)
}

// ChownCheckout gives every file of the checkout at dir to uid:gid. It is
// for a checkout on a disk of its own, which shares no file with the
// workspace repository.
func ChownCheckout(dir string, uid, gid int) error {
	return filepath.WalkDir(dir, func(path string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return os.Lchown(path, uid, gid)
	})
}

// errVMCheckout reports an operation the server does not yet run inside a
// session's VM.
var errVMCheckout = errors.New("gitengine: not available for a session whose checkout is in a VM")

func vmUnsupported(run domain.RunID) error {
	return fmt.Errorf("%w: session %s", errVMCheckout, run)
}

// publishVM publishes the run branch at the HEAD of the checkout in the VM.
func (e *Engine) publishVM(ctx context.Context, run domain.RunID, meta runMeta) (string, error) {
	g, err := e.vmGit(ctx, run)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrCheckoutNotFound, err)
	}
	head, err := g.Head(ctx, string(run))
	if err != nil {
		return "", vmErr(err)
	}
	return head, e.publishVMHead(ctx, run, meta, head)
}

// removeVMCheckout deletes the checkout's disk and identity. Its snapshot
// store keeps the history: every recorded object arrived from the VM, and
// the fork point is in the workspace repository it borrows from.
func (e *Engine) removeVMCheckout(ctx context.Context, run domain.RunID, checkout, metaPath string) error {
	lock := e.snapshotLock(run)
	lock.Lock()
	defer lock.Unlock()
	if err := e.cfg.RemoveCheckout(ctx, checkout); err != nil {
		return fmt.Errorf("gitengine: remove checkout disk of session %s: %w", run, err)
	}
	if err := os.Remove(metaPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("gitengine: remove session metadata for %s: %w", run, err)
	}
	return nil
}
