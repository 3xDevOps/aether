package vm

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"
)

const (
	restoreTimeout = 60 * time.Second
	syncTimeout    = 30 * time.Second
)

// save pauses the VM, writes its memory and device state to the snapshot
// directory and ends the monitor. A VM with a checkout is refused: its
// checkouts are virtio-fs shares, and a fresh virtiofsd after restore knows
// none of the guest's open files, so every access to them fails with EBADF.
func (e *engine) save(ctx context.Context, key string) error {
	unlock := e.lock(key)
	defer unlock()
	rec, err := e.requireRunning(ctx, key, "save")
	if err != nil {
		return err
	}
	if len(rec.Checkouts) > 0 {
		targets := make([]string, len(rec.Checkouts))
		for i, m := range rec.Checkouts {
			targets[i] = m.ContainerPath
		}
		return fmt.Errorf("vm runtime: cannot save VM %s: its checkouts (%s) are virtio-fs mounts, which would come back unusable after a restore; work item 6 moves them onto the VM's disk", key, strings.Join(targets, ", "))
	}
	api := newCHAPI(e.path(key, apiSocket))
	state, err := api.state(ctx)
	if err != nil {
		return fmt.Errorf("vm runtime: save VM %s: %w", key, err)
	}
	// Cloud Hypervisor refuses to pause a paused VM; one the caller paused
	// stays paused if the save fails, and cannot sync first.
	paused := state == "Paused"
	synced := !paused && e.syncGuest(ctx, key)
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
	if err = api.snapshot(ctx, dir); err != nil {
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
// cache disk that is never restored still holds what the session wrote.
func (e *engine) syncGuest(ctx context.Context, key string) bool {
	h, err := e.helper(key)
	if err == nil {
		syncCtx, cancel := context.WithTimeout(ctx, syncTimeout)
		err = h.Sync(syncCtx)
		cancel()
	}
	if err != nil {
		slog.Warn("vm runtime: sync the guest before a save; its cache will not be promoted", "vm", key, "error", err)
		return false
	}
	return true
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
// it would pair old memory with newer disks. Cloud Hypervisor has read all
// of it by then. With no snapshot left, the guest is never killed for a
// later error or the caller's ctx ending.
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
	if up, upErr := e.running(ctx, key); upErr != nil || up {
		return errors.Join(upErr, fmt.Errorf("vm runtime: restore VM %s: it is already running", key))
	}
	// The guest's mounts were made at boot: the shares keep the indexes they
	// had then, whatever the host holds now.
	booted, err := readLaunch(e.dir(key))
	if err != nil {
		return err
	}
	dir := e.path(key, snapshotDir)
	l := launch{Home: booted.Home, Args: []string{
		"--api-socket", "path=" + e.path(key, apiSocket), "--event-monitor", "path=" + e.path(key, eventsFile),
		"--restore", "source_url=file://" + dir,
	}}
	if err = e.ensureNAT(ctx); err != nil {
		return err
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
