package vm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"maps"
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
	// snapshotRate is the slowest write a snapshot gets time for: a 24 GiB
	// guest gets over 20 minutes. A monitor still busy after that is taken
	// to hang; the VM it holds paused is resumed by the next wake.
	snapshotRate = 20 << 20
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
	// Not cut short with ctx: a monitor cut off goes on writing, and answers
	// no resume until it is done.
	snapCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), chAPITimeout+time.Duration(unitMemoryMiB(rec)<<20/snapshotRate)*time.Second)
	err = api.snapshot(snapCtx, dir)
	cancel()
	if err == nil {
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

// syncSnapshot makes the snapshot durable before the record says it is
// saved, so a crash cannot leave a saved VM whose memory file is short.
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
// its clock. Cloud Hypervisor reads all of the saved memory before it
// answers: loaded on demand, a page something else had touched first kept
// zeros. The saved state is kept until the guest answers. A guest that dies
// first, with no disk written, is restored from it again by the next wake.
// Once a disk is written the saved state no longer matches it, and is
// dropped: a second restore would pair old memory with newer disks. A guest
// that still runs is never killed for a later error or the caller's ctx
// ending. A VM with a virtio-fs share is never saved.
func (e *engine) restore(ctx context.Context, key string) error {
	unlock := e.lock(key)
	defer unlock()
	rec, err := e.load(key)
	if err != nil {
		return err
	}
	if !rec.Saved {
		return fmt.Errorf("vm runtime: restore VM %s: %w", key, runtime.ErrNotSaved)
	}
	up, err := e.running(ctx, key)
	if err != nil {
		return err
	}
	api := newCHAPI(e.path(key, apiSocket))
	dir := e.path(key, snapshotDir)
	var written map[string]time.Time
	// A monitor a server left mid-restore or mid-save holds the guest paused
	// at the saved state, which still matches the disks. One it left after
	// the resume runs the guest, which only has to answer.
	if state, _ := api.state(ctx); !up || state != "Running" {
		if up {
			e.kill(ctx, key)
		}
		// The guest's mounts were made at boot: the shares keep the indexes
		// they had then, whatever the host holds now.
		var booted launch
		if booted, err = readLaunch(e.dir(key)); err != nil {
			return err
		}
		l := launch{Home: booted.Home, Reveal: booted.Reveal, Args: []string{
			"--api-socket", "path=" + e.path(key, apiSocket), "--event-monitor", "path=" + e.path(key, eventsFile),
			"--restore", "source_url=file://" + dir,
		}}
		if rec.Net != offline {
			if err = e.ensureNetwork(ctx); err != nil {
				return err
			}
		}
		if err = e.launch(ctx, rec, l); err != nil {
			return err
		}
		loadCtx, cancel := context.WithTimeout(ctx, restoreTimeout+time.Duration(unitMemoryMiB(rec)<<20/snapshotRate)*time.Second)
		err = e.awaitRestored(loadCtx, key, api)
		cancel()
		if err != nil {
			e.kill(context.WithoutCancel(ctx), key)
			return err
		}
		written = diskTimes(dir)
		// A resume that fails, or whose reply is lost, may have run the
		// guest: the monitor is left for the caller to resume or stop.
		if err = api.resume(ctx); err != nil {
			err = fmt.Errorf("vm runtime: resume restored VM %s: %w", key, err)
		}
	}
	live, cancel := context.WithTimeout(context.WithoutCancel(ctx), bootTimeout)
	defer cancel()
	var h helper
	if err == nil {
		h, err = e.helper(key)
	}
	if err == nil {
		err = e.waitHelper(live, key, h)
	}
	if err == nil {
		if err = h.SyncClock(live, time.Now()); err != nil {
			err = fmt.Errorf("vm runtime: set the clock of restored VM %s: %w", key, err)
		}
	}
	rec.Saved, rec.Exited = false, false
	gone := context.WithoutCancel(ctx)
	if st, stErr := e.units.State(gone, unitName(key)); err != nil && (e.rebooted(key) || stErr == nil && !st.Active) {
		// Read before the kill, at which Cloud Hypervisor closes the disks.
		kept := len(written) > 0 && maps.Equal(written, diskTimes(dir))
		e.kill(gone, key)
		if kept {
			return fmt.Errorf("%w; the guest wrote no disk, so its saved state is kept", err)
		}
		rec.Exited = true
		err = fmt.Errorf("%w; the guest wrote its disks first, so its saved state is dropped: %w", err, runtime.ErrNotSaved)
	}
	rec.CacheRan, rec.CacheSynced = rec.Cache.Path != "", false
	return errors.Join(err, e.store(rec), os.RemoveAll(dir))
}

// diskTimes is when each disk the saved guest in dir may write was last
// written, from the VM configuration Cloud Hypervisor saved with it; nil
// when that cannot be read.
func diskTimes(dir string) map[string]time.Time {
	data, err := os.ReadFile(filepath.Join(dir, "config.json"))
	var cfg struct {
		Disks []struct {
			Path     string `json:"path"`
			Readonly bool   `json:"readonly"`
		} `json:"disks"`
	}
	if err != nil || json.Unmarshal(data, &cfg) != nil {
		return nil
	}
	times := map[string]time.Time{}
	for _, d := range cfg.Disks {
		info, err := os.Stat(d.Path)
		if err != nil {
			return nil
		}
		if !d.Readonly {
			times[d.Path] = info.ModTime()
		}
	}
	return times
}

// awaitRestored waits until Cloud Hypervisor holds the restored, still
// paused VM.
func (e *engine) awaitRestored(ctx context.Context, key string, api chAPI) error {
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
