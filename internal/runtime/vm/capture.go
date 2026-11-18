package vm

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/3xDevOps/Aether/internal/runtime"
)

var _ runtime.CaptureRuntime = (*Runtime)(nil)

// captureMain is the main process of a VM booted for capture.
var captureMain = []string{"/bin/sh", "-c", "exec sleep infinity"}

// BootForCapture starts the stopped session VM id with captureMain in place
// of its command, which for a Standard session would run the agent with its
// task again, and without its setup script. Its checkouts are set up and
// served as in any boot. A saved VM is restored instead, as its saved memory
// may hold writes its disks do not, with its session frozen, and cold-starts
// only when the restore fails. A VM that runs with its vCPUs stopped, as the disk guard or a
// failed save leaves it, only has them started. Either way its session's
// processes stay frozen if they were.
func (r *Runtime) BootForCapture(ctx context.Context, id runtime.ID) error {
	key, ok := vmKey(id)
	if !ok {
		return fmt.Errorf("vm runtime: only a session VM boots for capture, not %s", id)
	}
	rec, err := r.vms.load(key)
	if err != nil {
		return err
	}
	if !rec.session() {
		return fmt.Errorf("vm runtime: only a session VM boots for capture, not %s", key)
	}
	if rec.Saved {
		err := r.vms.restore(ctx, key)
		// A sleep saves a session running; it must not work on past its end.
		if err == nil && !rec.Frozen && len(rec.CheckoutDisks) > 0 {
			err = r.vms.pause(ctx, key)
		}
		if err == nil {
			return nil
		}
		// A restore can fail after its guest resumed with its own command.
		if stopErr := r.vms.stop(ctx, key, 0); stopErr != nil {
			return errors.Join(err, stopErr)
		}
		slog.Warn("vm runtime: restore a saved VM for capture; starting it from its disks", "vm", key, "error", err)
	}
	unlock := r.vms.lock(key)
	defer unlock()
	if r.vms.halted(ctx, key) {
		if err := newCHAPI(r.vms.path(key, apiSocket)).resume(ctx); err != nil {
			return fmt.Errorf("vm runtime: start the vCPUs of VM %s for capture: %w", key, err)
		}
		return nil
	}
	r.vms.setFlag(r.vms.capture, key, true)
	defer r.vms.setFlag(r.vms.capture, key, false)
	return r.vms.startLocked(ctx, key)
}
