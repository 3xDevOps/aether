package vm

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"syscall"

	"github.com/3xDevOps/Aether/internal/runtime"
)

func (r *Runtime) DiskUse(context.Context) (map[runtime.ID]int64, error) {
	return r.vms.diskUse()
}

func (r *Runtime) HoldDisks(ctx context.Context, id runtime.ID) error {
	key, ok := vmKey(id)
	if !ok {
		return fmt.Errorf("vm runtime: %s is not a VM", id)
	}
	return r.vms.holdDisks(ctx, key)
}

// holdDisks stops the vCPUs of a VM whose session Pause froze: root in the
// guest can leave the frozen cgroup. resume starts them again first.
func (e *engine) holdDisks(ctx context.Context, key string) error {
	unlock := e.lock(key)
	defer unlock()
	if _, err := e.requireRunning(ctx, key, "hold the disks of"); err != nil {
		return err
	}
	if e.halted(ctx, key) {
		return nil
	}
	if err := newCHAPI(e.path(key, apiSocket)).pause(ctx); err != nil {
		return fmt.Errorf("vm runtime: hold the disks of VM %s: %w", key, err)
	}
	return nil
}

func (e *engine) diskUse() (map[runtime.ID]int64, error) {
	keys, err := e.keys()
	if err != nil {
		return nil, err
	}
	use := make(map[runtime.ID]int64, len(keys))
	for _, key := range keys {
		// One VM the server cannot read is skipped, not left to blind the
		// disk guard to every other VM.
		rec, err := e.load(key)
		if err != nil {
			if !errors.Is(err, runtime.ErrNotFound) {
				slog.Warn("vm runtime: disk use: read VM record", "vm", key, "error", err)
			}
			continue
		}
		paths := []string{rec.Root.Path, rec.Data.Path, rec.Cache.Path, rec.State.Path}
		for _, c := range rec.CheckoutDisks {
			paths = append(paths, c.Disk.Path)
		}
		var n int64
		for _, p := range paths {
			if p == "" {
				continue
			}
			info, err := os.Stat(p)
			if err != nil {
				if !errors.Is(err, fs.ErrNotExist) {
					slog.Warn("vm runtime: disk use: measure disk", "vm", key, "disk", p, "error", err)
				}
				continue
			}
			n += info.Sys().(*syscall.Stat_t).Blocks * 512
		}
		use[runtime.ID(idPrefix+key)] = n
	}
	return use, nil
}
