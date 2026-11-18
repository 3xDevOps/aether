package vm

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/3xDevOps/Aether/internal/runtime"
	"github.com/3xDevOps/Aether/internal/sysstats"
)

const (
	restoreTimeout     = 60 * time.Second
	syncTimeout        = 30 * time.Second
	destroySyncTimeout = 5 * time.Second
	// Free page reporting hands the guest's free memory to the host in
	// passes 2 s apart, each about a sixteenth of it, so a check every
	// engine.reportPoll, 4 s, spans a pass; one that freed under
	// reportSettled ends the wait. The kernel's own estimate for all of it
	// is about 30 s.
	reportSettled = 16 << 20
	reportTimeout = 40 * time.Second
)

// save pauses the VM, writes its memory and device state to the snapshot
// directory and ends the monitor. A VM with a checkout is refused: its
// checkouts are virtio-fs shares, and a fresh virtiofsd after restore knows
// none of the guest's open files, so every access to them fails with EBADF.
// A frozen VM is thawed only for its monitor to pause it, and saved with
// what it holds: a sync would let its guest run, and take memory, first.
func (e *engine) save(ctx context.Context, key string) error {
	unlock := e.lock(key)
	defer unlock()
	rec, err := e.requireRunning(ctx, key, "save")
	if err != nil {
		return err
	}
	booted, err := readLaunch(e.dir(key))
	if err != nil {
		return err
	}
	if len(booted.Shares) > 0 {
		sources := make([]string, len(booted.Shares))
		for i, s := range booted.Shares {
			sources[i] = s.Source
		}
		return fmt.Errorf("vm runtime: cannot save VM %s: it shares %s over virtio-fs, which would come back unusable after a restore", key, strings.Join(sources, ", "))
	}
	frozen := e.frozen(key)
	if frozen {
		if err = e.thaw(key); err != nil {
			return err
		}
	}
	api := newCHAPI(e.path(key, apiSocket))
	state, err := api.state(ctx)
	if err != nil {
		return fmt.Errorf("vm runtime: save VM %s: %w", key, err)
	}
	// Cloud Hypervisor refuses to pause a paused VM; one the caller paused
	// stays paused if the save fails, and cannot sync first.
	paused := state == "Paused"
	synced := !paused && !frozen && e.syncGuest(ctx, key)
	if synced {
		// The second sync writes what the session wrote during the wait.
		e.awaitReported(ctx, key)
		synced = e.syncGuest(ctx, key)
	}
	if !paused {
		if err = api.pause(ctx); err != nil {
			return fmt.Errorf("vm runtime: save VM %s: %w", key, err)
		}
	}
	resumeAfter := func(cause error) error {
		if paused {
			return fmt.Errorf("vm runtime: save VM %s: %w", key, cause)
		}
		return e.resumeAfter(ctx, api, key, cause)
	}
	dir := e.path(key, snapshotDir)
	if err = os.RemoveAll(dir); err != nil {
		return resumeAfter(err)
	}
	if err = os.Mkdir(dir, 0o700); err != nil {
		return resumeAfter(err)
	}
	if err = snapshotRestored(ctx, api, key, dir); err == nil {
		err = syncSnapshot(dir)
	}
	if err != nil {
		_ = os.RemoveAll(dir)
		return resumeAfter(err)
	}
	rec.Saved, rec.Exited, rec.ExitCode = true, true, killedExitCode
	rec.CacheSynced = synced
	if err = e.store(rec); err != nil {
		return resumeAfter(err)
	}
	e.forgetHelper(key)
	if err := api.shutdownVMM(ctx); err != nil {
		return e.units.Stop(context.WithoutCancel(ctx), unitName(key))
	}
	return e.awaitStopped(ctx, key)
}

// syncGuest writes the guest's dirty data to the VM's disks, so a saved
// cache disk that is never restored, or a database disk the next VM takes
// over, holds what the session wrote.
func (e *engine) syncGuest(ctx context.Context, key string) bool {
	h, err := e.helper(key)
	if err == nil {
		syncCtx, cancel := context.WithTimeout(ctx, syncTimeout)
		err = h.Sync(syncCtx)
		cancel()
	}
	if err != nil {
		slog.Warn("vm runtime: sync the guest's disks; a cache saved now will not be promoted", "vm", key, "error", err)
		return false
	}
	return true
}

// awaitReported waits while the host gets back the page cache Sync freed
// in the guest, so the save writes only what the session uses.
func (e *engine) awaitReported(ctx context.Context, key string) {
	dir := filepath.Join(e.cgroupRoot, runtime.SessionSlice, unitName(key))
	held, err := sysstats.CgroupWorkingSet(dir)
	for deadline := time.Now().Add(reportTimeout); err == nil && time.Now().Before(deadline); {
		select {
		case <-ctx.Done():
			return
		case <-time.After(e.reportPoll):
		}
		var now uint64
		if now, err = sysstats.CgroupWorkingSet(dir); err == nil && held < now+reportSettled {
			return
		}
		held = now
	}
}

// snapshotRestored snapshots the paused VM once Cloud Hypervisor has loaded
// all of the memory of the restore that woke it, which it refuses to
// snapshot before; it keeps loading while the VM is paused.
func snapshotRestored(ctx context.Context, api chAPI, key, dir string) error {
	deadline := time.Now().Add(restoreTimeout)
	for waited := false; ; waited = true {
		err := api.snapshot(ctx, dir)
		if err == nil || !strings.Contains(err.Error(), "memory restore is still in progress") || time.Now().After(deadline) {
			return err
		}
		if !waited {
			slog.Info("vm runtime: wait for the VM to load the memory it was restored with before saving it", "vm", key)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%w: %w", err, ctx.Err())
		case <-time.After(waitPoll):
		}
	}
}

// syncSnapshot makes the snapshot durable before the record says it is
// saved: a restore reads the memory file only as the guest touches it, so
// one a crash left short would fail the restored guest, not the restore.
func syncSnapshot(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	paths := []string{dir}
	for _, entry := range entries {
		paths = append(paths, filepath.Join(dir, entry.Name()))
	}
	for _, path := range paths {
		f, openErr := os.Open(path)
		if openErr != nil {
			return openErr
		}
		if err = errors.Join(f.Sync(), f.Close()); err != nil {
			return fmt.Errorf("vm runtime: sync %s: %w", path, err)
		}
	}
	return nil
}

// savedMemory is the disk the saved state of a VM holds, once a save or
// restore under way has finished. Cloud Hypervisor writes only the memory
// the host held for the guest, and a restore loads all of it back. A saved
// state that cannot be read counts as the unit's cap, as memoryUse counts
// a cgroup it cannot read; one that is gone counts nothing, since the wake
// then starts the VM from its disks.
func (e *engine) savedMemory(key string) (uint64, error) {
	unlock := e.lock(key)
	defer unlock()
	rec, err := e.load(key)
	if errors.Is(err, runtime.ErrNotFound) {
		return 0, nil
	}
	if err != nil || !rec.Saved {
		return 0, err
	}
	entries, err := os.ReadDir(e.path(key, snapshotDir))
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	var n int64
	for _, entry := range entries {
		info, infoErr := entry.Info()
		if infoErr != nil {
			err = infoErr
			break
		}
		n += info.Sys().(*syscall.Stat_t).Blocks * 512
	}
	if err != nil {
		return uint64(unitMemoryMiB(rec)) << 20, fmt.Errorf("vm runtime: saved memory of VM %s: %w", key, err)
	}
	return uint64(n), nil
}

func (e *engine) resumeAfter(ctx context.Context, api chAPI, key string, cause error) error {
	if err := api.resume(context.WithoutCancel(ctx)); err != nil {
		return fmt.Errorf("vm runtime: save VM %s: %w; and it stays paused: %w", key, cause, err)
	}
	return fmt.Errorf("vm runtime: save VM %s: %w", key, cause)
}

func (e *engine) awaitStopped(ctx context.Context, key string) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), powerOffTimeout)
	defer cancel()
	for {
		up, err := e.running(ctx, key)
		if err == nil && !up {
			return nil
		}
		select {
		case <-ctx.Done():
			return e.units.Stop(context.WithoutCancel(ctx), unitName(key))
		case <-time.After(waitPoll):
		}
	}
}

// restore starts the monitor on the saved state, resumes the guest and sets
// its clock. Once the guest runs, its disks move past the snapshot, so the
// snapshot is dropped durably before the guest resumes: a second restore of
// it would pair old memory with newer disks. Cloud Hypervisor, loading the
// memory on demand, holds the memory file open until it has read all of it.
// With no snapshot left, the guest is never killed for a later error or the
// caller's ctx ending. On-demand restore takes no vhost-user device, and a
// VM with a virtio-fs share is never saved.
func (e *engine) restore(ctx context.Context, key string) (err error) {
	unlock := e.lock(key)
	defer unlock()
	rec, err := e.load(key)
	if err != nil {
		return err
	}
	if !rec.Saved {
		return fmt.Errorf("vm runtime: restore VM %s: it has no saved state", key)
	}
	up, err := e.running(ctx, key)
	if err != nil {
		return err
	}
	// A monitor a server left mid-restore or mid-save holds the guest
	// paused at the saved state, which still matches the disks.
	if up {
		e.kill(ctx, key)
	}
	// The guest's mounts were made at boot: the shares keep the indexes they
	// had then, whatever the host holds now.
	booted, err := readLaunch(e.dir(key))
	if err != nil {
		return err
	}
	dir := e.path(key, snapshotDir)
	l := launch{Home: booted.Home, Reveal: booted.Reveal, Args: []string{
		"--api-socket", "path=" + e.path(key, apiSocket), "--event-monitor", "path=" + e.path(key, eventsFile),
		"--restore", "source_url=file://" + dir + ",memory_restore_mode=ondemand",
	}}
	if rec.Net != offline {
		if err = e.ensureNetwork(ctx); err != nil {
			return err
		}
	}
	if err = e.launch(ctx, rec, l); err != nil {
		return err
	}
	committed := false
	defer func() {
		if err != nil && !committed {
			e.kill(context.WithoutCancel(ctx), key)
		}
	}()
	api := newCHAPI(e.path(key, apiSocket))
	if err = e.awaitRestored(ctx, key, api); err != nil {
		return err
	}
	rec.Saved, rec.Exited = false, false
	rec.CacheRan, rec.CacheSynced = rec.Cache.Path != "", false
	if err = e.store(rec); err != nil {
		return err
	}
	// From here the monitor holds the only copy of the guest: a failed or
	// lost resume leaves it for the caller to resume or stop.
	committed = true
	if err = os.RemoveAll(dir); err != nil {
		return fmt.Errorf("vm runtime: remove the saved state of VM %s: %w", key, err)
	}
	if err = api.resume(ctx); err != nil {
		return fmt.Errorf("vm runtime: resume restored VM %s: %w", key, err)
	}
	live, cancel := context.WithTimeout(context.WithoutCancel(ctx), bootTimeout)
	defer cancel()
	h, err := e.helper(key)
	if err != nil {
		return err
	}
	if err = e.waitHelper(live, key, h); err != nil {
		return err
	}
	if err = h.SyncClock(live, time.Now()); err != nil {
		return fmt.Errorf("vm runtime: set the clock of restored VM %s: %w", key, err)
	}
	return nil
}

// awaitRestored waits until Cloud Hypervisor holds the restored, still
// paused VM.
func (e *engine) awaitRestored(ctx context.Context, key string, api chAPI) error {
	ctx, cancel := context.WithTimeout(ctx, restoreTimeout)
	defer cancel()
	for {
		state, err := api.state(ctx)
		if err == nil && state == "Paused" {
			return nil
		}
		if up, upErr := e.running(ctx, key); upErr == nil && !up {
			return fmt.Errorf("vm runtime: VM %s stopped while restoring%s", key, e.logTail(key))
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("vm runtime: restore VM %s: %w (last state %q, %v)%s", key, ctx.Err(), state, err, e.logTail(key))
		case <-time.After(waitPoll):
		}
	}
}
