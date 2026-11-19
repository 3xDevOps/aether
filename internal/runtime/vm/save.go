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
	// A guest back from a restart holds none of the session's processes;
	// a restore of it takes the event log as the proof.
	if e.rebooted(key) {
		return fmt.Errorf("vm runtime: save VM %s: its guest restarted, so it no longer holds the session%s", key, e.logTail(key))
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
	rec.Saved, rec.Resumed, rec.Exited, rec.ExitCode = true, nil, true, killedExitCode
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
// zeros. The saved state is kept until the guest answers, and matches the
// disks only until the guest writes one, so the record holds the disks'
// times from before the resume, and reads running from then on: a server
// that stops before the answer leaves a running VM, which the next one saves
// again. A guest that dies before it answers, or with the server, is
// restored again while those times stand; once one moved, the saved state
// is dropped: a second restore would pair old memory with newer disks. A
// guest that still runs is never killed for a later error or the caller's
// ctx ending. A VM with a virtio-fs share is never saved.
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
	// Not ended with ctx: what follows a resume decides what the record
	// says of a guest that ran.
	after := context.WithoutCancel(ctx)
	// moved is why the saved state no longer matches the disks, if it does not.
	moved := func() error {
		now, timesErr := diskTimes(dir)
		if timesErr == nil && !maps.Equal(rec.Resumed, now) {
			timesErr = errors.New("its guest wrote its disks")
		}
		return timesErr
	}
	drop := func(cause, why error) error {
		rec.Saved, rec.Resumed, rec.Exited, rec.ExitCode = false, nil, true, lostExitCode
		dropErr := e.store(rec)
		if dropErr == nil {
			dropErr = os.RemoveAll(dir)
		}
		return errors.Join(fmt.Errorf("%w; %w, so its saved state is dropped: %w", cause, why, runtime.ErrNotSaved), dropErr)
	}
	keep := func(cause error) error {
		rec.Resumed, rec.Exited = nil, true
		return errors.Join(cause, e.store(rec))
	}
	// A monitor an earlier restore resumed holds the guest, which only has
	// to answer. Any other holds it paused at the saved state, as a server
	// that stopped mid-restore or mid-save left it.
	if !up || rec.Resumed == nil {
		if rec.Resumed != nil {
			if why := moved(); why != nil {
				return drop(fmt.Errorf("vm runtime: restore VM %s: the guest an earlier wake resumed is gone", key), why)
			}
		}
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
		if err == nil {
			rec.Resumed, err = diskTimes(dir)
		}
		if err == nil {
			rec.Exited = false
			rec.CacheRan, rec.CacheSynced = rec.Cache.Path != "", false
			err = e.store(rec)
		}
		if err != nil {
			e.kill(after, key)
			return err
		}
	}
	live, cancel := context.WithTimeout(after, bootTimeout)
	defer cancel()
	state := func() string {
		s, _ := api.state(live)
		return s
	}
	// A guest still paused has not run, so a refused resume is tried again.
	var resumeErr error
	for try := 0; try < 3 && state() != "Running"; try++ {
		resumeErr = api.resume(live)
	}
	stuck := state() == "Paused"
	var h helper
	if stuck {
		err = errors.Join(fmt.Errorf("vm runtime: resume restored VM %s: it stays paused%s", key, e.logTail(key)), resumeErr)
	} else if h, err = e.helper(key); err == nil {
		err = e.waitHelper(live, key, h)
	}
	if err == nil {
		if err = h.SyncClock(live, time.Now()); err != nil {
			err = fmt.Errorf("vm runtime: set the clock of restored VM %s: %w", key, err)
		}
	}
	if err != nil {
		if alive, aliveErr := e.running(after, key); stuck || e.rebooted(key) || aliveErr == nil && !alive {
			// A paused guest writes nothing more; Cloud Hypervisor may write
			// a disk as it closes it, so the times are read before the kill.
			held := stuck || api.pause(live) == nil
			why := moved()
			e.kill(after, key)
			if !held {
				why = moved()
			}
			if why != nil {
				return drop(err, why)
			}
			return keep(err)
		}
	}
	rec.Saved, rec.Resumed, rec.Exited = false, nil, false
	if storeErr := e.store(rec); storeErr != nil {
		return errors.Join(err, storeErr)
	}
	return errors.Join(err, os.RemoveAll(dir))
}

// diskTimes is when each disk the saved guest in dir may write was last
// written, from the VM configuration Cloud Hypervisor saved with it.
func diskTimes(dir string) (map[string]int64, error) {
	data, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		return nil, fmt.Errorf("vm runtime: saved VM configuration: %w", err)
	}
	var cfg struct {
		Disks []struct {
			Path     string `json:"path"`
			Readonly bool   `json:"readonly"`
		} `json:"disks"`
	}
	if err = json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("vm runtime: saved VM configuration: %w", err)
	}
	times := map[string]int64{}
	for _, d := range cfg.Disks {
		if d.Readonly || d.Path == "" {
			continue
		}
		info, err := os.Stat(d.Path)
		if err != nil {
			return nil, fmt.Errorf("vm runtime: saved VM's disk: %w", err)
		}
		times[d.Path] = info.ModTime().UnixNano()
	}
	if len(times) == 0 {
		return nil, fmt.Errorf("vm runtime: saved VM configuration in %s names no disk the guest writes", dir)
	}
	return times, nil
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
