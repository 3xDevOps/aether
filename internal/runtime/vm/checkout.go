package vm

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/guest"
	"github.com/3xDevOps/Aether/internal/runtime"
	"github.com/3xDevOps/Aether/internal/vmimage"
)

// CheckoutDisks gives the disk of the session checkout whose server path a
// worktree mount names, which the VM mounts at target, its files owned by
// uid:gid. ok is false for a checkout the VM shares from the server
// instead.
type CheckoutDisks func(ctx context.Context, path, target string, uid, gid int) (qcow2 string, checkout guest.Checkout, ok bool, err error)

// checkoutDisk is a worktree mount whose checkout lives on its own disk.
type checkoutDisk struct {
	Target   string         `json:"target"`
	Disk     vmimage.Disk   `json:"disk"`
	Checkout guest.Checkout `json:"checkout"`
}

// checkoutDisks splits the spec's checkouts into those on disks of their
// own and those the VM shares from the server.
func (e *engine) checkoutDisks(ctx context.Context, spec runtime.Spec) (disks []checkoutDisk, shared []runtime.Mount, err error) {
	all := checkouts(spec)
	if e.cfg.CheckoutDisks == nil {
		return nil, all, nil
	}
	uid, gid, err := numericOwner(spec.User)
	if err != nil {
		return nil, nil, err
	}
	for _, m := range all {
		disk, checkout, ok, err := e.cfg.CheckoutDisks(ctx, m.HostPath, m.ContainerPath, uid, gid)
		if err != nil {
			return nil, nil, fmt.Errorf("vm runtime: checkout disk for %s: %w", m.ContainerPath, err)
		}
		if ok {
			disks = append(disks, checkoutDisk{Target: m.ContainerPath, Disk: vmimage.Disk{Path: disk, Format: vmimage.FormatQcow2}, Checkout: checkout})
		} else {
			shared = append(shared, m)
		}
	}
	return disks, shared, nil
}

func numericOwner(user string) (int, int, error) {
	if user == "" {
		return 0, 0, nil
	}
	uidText, gidText, _ := strings.Cut(user, ":")
	uid, err := strconv.Atoi(uidText)
	if err != nil {
		return 0, 0, fmt.Errorf("vm runtime: a session with its checkout on a disk needs a numeric user, not %q", user)
	}
	gid := uid
	if gidText != "" {
		if gid, err = strconv.Atoi(gidText); err != nil {
			return 0, 0, fmt.Errorf("vm runtime: a session with its checkout on a disk needs a numeric group, not %q", user)
		}
	}
	return uid, gid, nil
}

func checkoutSerial(i int) string { return "repo" + strconv.Itoa(i) }

func (rec *record) checkoutIdentities() []guest.Checkout {
	out := make([]guest.Checkout, len(rec.CheckoutDisks))
	for i, c := range rec.CheckoutDisks {
		out[i] = c.Checkout
	}
	return out
}

// Git returns the git service of the session VM of run, which serves while
// the VM runs, also after the session's main process exited.
func (r *Runtime) Git(ctx context.Context, run domain.RunID) (*guest.Git, error) {
	key := string(run)
	if err := checkKey(key); err != nil {
		return nil, err
	}
	rec, err := r.vms.load(key)
	if errors.Is(err, runtime.ErrNotFound) {
		return nil, fmt.Errorf("vm runtime: VM %s: %w: %w", key, guest.ErrVMNotRunning, err)
	}
	if err != nil {
		return nil, err
	}
	up, err := r.vms.running(ctx, key)
	if err != nil {
		return nil, err
	}
	// A paused guest answers nothing until it resumes.
	if !up || rec.Saved || r.vms.paused(ctx, key) {
		return nil, fmt.Errorf("vm runtime: VM %s: %w", key, guest.ErrVMNotRunning)
	}
	return r.vms.gitClient(key)
}
