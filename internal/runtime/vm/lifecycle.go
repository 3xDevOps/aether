package vm

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"regexp"
	"slices"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/3xDevOps/Aether/internal/guest"
	"github.com/3xDevOps/Aether/internal/runtime"
)

const (
	defaultStopGrace = 10 * time.Second
	killGrace        = 5 * time.Second
	powerOffTimeout  = 15 * time.Second
	// helperCallTimeout bounds one short call to the helper, whose client
	// otherwise waits for a guest that may never answer again.
	helperCallTimeout = 5 * time.Second
	// lostExitCode reports a VM that ended without its helper reporting
	// Main's exit: the host killed it, the guest crashed or the host
	// rebooted.
	lostExitCode = 255
	// killedExitCode is 128+SIGKILL, what a container reports after a stop
	// had to kill.
	killedExitCode = 137
	waitPoll       = 200 * time.Millisecond
	// lingerTimeout bounds how long a VM stays up after its main process
	// exited: long enough for the server to finish the session's checkout,
	// which is on the VM's disk.
	lingerTimeout = 10 * time.Minute
)

func (e *engine) running(ctx context.Context, key string) (bool, error) {
	st, err := e.units.State(ctx, unitName(key))
	return st.Active, err
}

func (e *engine) requireRunning(ctx context.Context, key, action string) (*record, error) {
	rec, err := e.load(key)
	if err != nil {
		return nil, err
	}
	up, err := e.running(ctx, key)
	if err != nil {
		return nil, err
	}
	if !up || rec.Exited {
		return nil, fmt.Errorf("vm runtime: %s VM %s: it is not running", action, key)
	}
	return rec, nil
}

// stop ends Main as docker stop ends a container's main process, and a
// saved VM's saved state with it. Every helper call is bounded, so a guest
// that stopped answering is killed instead of holding the VM's lock.
func (e *engine) stop(ctx context.Context, key string, grace time.Duration) error {
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
	if up && rec.Exited {
		return nil
	}
	if !up {
		if !rec.Saved {
			return nil
		}
		if err = os.RemoveAll(e.path(key, snapshotDir)); err != nil {
			return fmt.Errorf("vm runtime: discard saved state: %w", err)
		}
		rec.Saved = false
		if err = e.store(rec); err != nil {
			return err
		}
		e.promoteCache(ctx, rec)
		return nil
	}
	api := newCHAPI(e.path(key, apiSocket))
	if state, stateErr := api.state(ctx); stateErr == nil && state == "Paused" {
		if err = api.resume(ctx); err != nil {
			return fmt.Errorf("vm runtime: resume before stop: %w", err)
		}
	}
	if grace < 0 {
		grace = defaultStopGrace
	}
	h, err := e.helper(key)
	if err != nil {
		return err
	}
	code, ended := endMain(ctx, h.Process(guest.MainID), grace)
	if err = e.finish(ctx, rec, code); err != nil || ended || len(rec.CheckoutDisks) == 0 {
		return err
	}
	// A guest that never confirmed Main's end serves no git work either.
	return e.shutDown(ctx, rec)
}

// endMain ends Main and reports its exit code, and whether the helper
// confirmed that it ended.
func endMain(ctx context.Context, main process, grace time.Duration) (int, bool) {
	signal := func(sig syscall.Signal) error {
		sigCtx, cancel := context.WithTimeout(ctx, helperCallTimeout)
		defer cancel()
		return main.Signal(sigCtx, sig)
	}
	waitFor := func(d time.Duration) (int, bool) {
		waitCtx, cancel := context.WithTimeout(ctx, d)
		defer cancel()
		code, err := main.Wait(waitCtx)
		return code, err == nil
	}
	err := signal(syscall.SIGTERM)
	if errors.Is(err, guest.ErrNotFound) {
		return 0, true
	}
	if err != nil {
		return killedExitCode, false
	}
	if code, ok := waitFor(grace); ok {
		return code, true
	}
	if signal(syscall.SIGKILL) == nil {
		if code, ok := waitFor(killGrace); ok {
			return code, true
		}
	}
	return killedExitCode, false
}

// finish records Main's exit and powers the VM off; it holds the key's
// lock. A VM with checkouts on its disks lingers instead, with only the
// helper and its git service up, which the server still needs for the
// session's checkout, until Destroy, a new Start or lingerTimeout: the
// helper ends every session process when Main exits.
func (e *engine) finish(ctx context.Context, rec *record, code int) error {
	rec.Exited, rec.ExitCode, rec.ExitedAt = true, code, time.Now()
	if err := e.store(rec); err != nil {
		return err
	}
	if len(rec.CheckoutDisks) == 0 {
		return e.shutDown(ctx, rec)
	}
	e.endLinger(rec.Key, rec.Boots, lingerTimeout)
	return nil
}

// shutDown powers the VM off and, when it went down clean, makes its cache
// its pool's newest.
func (e *engine) shutDown(ctx context.Context, rec *record) error {
	clean, err := e.powerOff(ctx, rec)
	if err != nil || !clean {
		return err
	}
	rec.CacheSynced = true
	if err = e.store(rec); err != nil {
		return err
	}
	e.promoteCache(ctx, rec)
	return nil
}

// endLinger powers the VM off after d unless a new boot started.
func (e *engine) endLinger(key string, boot int, d time.Duration) {
	time.AfterFunc(d, func() {
		unlock := e.lock(key)
		defer unlock()
		rec, err := e.load(key)
		if err != nil || !rec.Exited || rec.Boots != boot {
			return
		}
		if err = e.shutDown(context.Background(), rec); err != nil {
			slog.Warn("vm runtime: power off a VM whose session ended", "vm", key, "error", err)
		}
	})
}

// lingering schedules the end of every VM that was lingering when the
// server stopped.
func (e *engine) lingering(ctx context.Context) {
	keys, err := e.keys()
	if err != nil {
		slog.Warn("vm runtime: list VMs", "error", err)
		return
	}
	for _, key := range keys {
		rec, err := e.load(key)
		if err != nil || !rec.Exited || rec.Saved {
			continue
		}
		if up, err := e.running(ctx, key); err == nil && up {
			e.endLinger(key, rec.Boots, max(0, lingerTimeout-time.Since(rec.ExitedAt)))
		}
	}
}

// powerOff reports a VM that powered itself off, which flushes its disks,
// as clean; one the unit's stop had to kill is not.
func (e *engine) powerOff(ctx context.Context, rec *record) (bool, error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), powerOffTimeout)
	defer cancel()
	h, err := e.helper(rec.Key)
	if err == nil {
		offCtx, offCancel := context.WithTimeout(ctx, helperCallTimeout)
		err = h.PowerOff(offCtx)
		offCancel()
	}
	e.forgetHelper(rec.Key)
	if err != nil {
		return false, e.units.Stop(context.WithoutCancel(ctx), unitName(rec.Key))
	}
	for {
		up, err := e.running(ctx, rec.Key)
		if err == nil && !up {
			return true, nil
		}
		select {
		case <-ctx.Done():
			return false, e.units.Stop(context.WithoutCancel(ctx), unitName(rec.Key))
		case <-time.After(waitPoll):
		}
	}
}

func (e *engine) kill(ctx context.Context, key string) {
	e.forgetHelper(key)
	_ = e.units.Stop(ctx, unitName(key))
}

// wait blocks until Main of the current boot exits, the VM is saved, or a
// later boot starts. On a VM that never booted it waits for the first boot.
func (e *engine) wait(ctx context.Context, key string) (runtime.ExitStatus, error) {
	boot := -1
	for {
		rec, err := e.load(key)
		if err != nil {
			return runtime.ExitStatus{}, err
		}
		if boot < 0 && rec.Booted {
			boot = rec.Boots
		}
		if rec.Exited || (boot >= 0 && rec.Boots != boot) {
			return runtime.ExitStatus{Code: rec.ExitCode}, nil
		}
		if rec.Booted && !e.flag(e.booting, key) {
			up, err := e.running(ctx, key)
			if err != nil {
				return runtime.ExitStatus{}, err
			}
			if !up {
				if code, err := e.lost(ctx, key); err != nil || code >= 0 {
					return runtime.ExitStatus{Code: code}, err
				}
				continue
			}
			if code, ok := e.waitMain(ctx, key, boot); ok {
				return runtime.ExitStatus{Code: code}, nil
			}
		}
		select {
		case <-ctx.Done():
			return runtime.ExitStatus{}, ctx.Err()
		case <-time.After(waitPoll):
		}
	}
}

// waitMain waits for Main of boot through the helper and finishes the VM;
// false means the wait ended first and the caller looks again. The helper's
// client retries a VM that died until its context ends, so the wait also
// ends when the unit does, or when Cloud Hypervisor reports that the guest
// rebooted on its own: its helper and Main are gone then.
func (e *engine) waitMain(ctx context.Context, key string, boot int) (int, bool) {
	h, err := e.helper(key)
	if err != nil {
		return 0, false
	}
	waitCtx, cancel, rebooted := e.whileUp(ctx, key)
	defer cancel()
	code, err := h.Process(guest.MainID).Wait(waitCtx)
	// A guest back from its reboot within a tick of whileUp answers at once
	// that it has no Main, so the event log is read here as well.
	gone := rebooted.Load() || (err != nil && e.rebooted(key))
	if err != nil && !gone {
		return 0, false
	}
	unlock := e.lock(key)
	defer unlock()
	rec, err := e.load(key)
	if err != nil {
		return 0, false
	}
	if rec.Exited || rec.Boots != boot {
		return rec.ExitCode, true
	}
	if gone {
		e.kill(context.WithoutCancel(ctx), key)
		rec.Exited, rec.ExitCode = true, lostExitCode
		return lostExitCode, e.store(rec) == nil
	}
	if err := e.finish(ctx, rec, code); err != nil {
		return 0, false
	}
	return code, true
}

// whileUp is ctx ending as soon as the VM's unit stops or Cloud Hypervisor
// reports that the guest rebooted, for calls into the guest: the helper's
// client retries a VM that died until its context ends. The flag tells a
// reboot apart.
func (e *engine) whileUp(ctx context.Context, key string) (context.Context, context.CancelFunc, *atomic.Bool) {
	upCtx, cancel := context.WithCancel(ctx)
	rebooted := &atomic.Bool{}
	go func() {
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		for {
			select {
			case <-upCtx.Done():
				return
			case <-tick.C:
				if e.rebooted(key) {
					rebooted.Store(true)
					cancel()
					return
				}
				if up, upErr := e.running(upCtx, key); upErr == nil && !up {
					cancel()
					return
				}
			}
		}
	}()
	return upCtx, cancel, rebooted
}

// rebooted reads Cloud Hypervisor's event log for a guest reset.
func (e *engine) rebooted(key string) bool {
	data, err := os.ReadFile(e.path(key, eventsFile))
	return err == nil && rebootEvent.Match(data)
}

var rebootEvent = regexp.MustCompile(`"event":\s*"rebooting"`)

// lost records a booted VM whose unit is gone without an exit, and returns
// -1 when the VM turns out to run again.
func (e *engine) lost(ctx context.Context, key string) (int, error) {
	unlock := e.lock(key)
	defer unlock()
	rec, err := e.load(key)
	if err != nil {
		return 0, err
	}
	if rec.Exited {
		return rec.ExitCode, nil
	}
	up, err := e.running(ctx, key)
	if err != nil || up {
		return -1, err
	}
	rec.Exited, rec.ExitCode = true, lostExitCode
	e.forgetHelper(key)
	return lostExitCode, e.store(rec)
}

// destroy removes the VM and every file it had; a missing VM is not an
// error. The key's lock stays: a caller may be waiting on it.
func (e *engine) destroy(ctx context.Context, key string) error {
	if checkKey(key) != nil {
		return nil
	}
	unlock := e.lock(key)
	defer unlock()
	if _, err := os.Stat(e.dir(key)); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	up, err := e.running(ctx, key)
	if err != nil {
		e.forgetHelper(key)
		return err
	}
	rec, loadErr := e.load(key)
	// The session's database disk outlives this VM, as when an account move
	// replaces it: the guest's unwritten data goes to it first. A paused VM
	// cannot sync, and is destroyed only with its session, and the disk; a
	// wedged one gets destroySyncTimeout, not a kill held up behind it.
	if up && loadErr == nil && rec.State.Path != "" {
		syncCtx, cancel := context.WithTimeout(ctx, destroySyncTimeout)
		if state, stateErr := newCHAPI(e.path(key, apiSocket)).state(syncCtx); stateErr == nil && state != "Paused" {
			e.syncGuest(syncCtx, key)
		}
		cancel()
	}
	e.forgetHelper(key)
	if err = e.units.Stop(ctx, unitName(key)); err != nil {
		return err
	}
	if loadErr == nil && !up {
		e.promoteCache(ctx, rec)
	}
	if err = os.RemoveAll(e.dir(key)); err != nil {
		return fmt.Errorf("vm runtime: remove VM %s: %w", key, err)
	}
	e.mu.Lock()
	delete(e.fresh, key)
	delete(e.events, key)
	e.mu.Unlock()
	return nil
}

// pause stops the vCPUs; the guest keeps its memory, as a frozen container
// does.
func (e *engine) pause(ctx context.Context, key string) error {
	if _, err := e.requireRunning(ctx, key, "pause"); err != nil {
		return err
	}
	if err := newCHAPI(e.path(key, apiSocket)).pause(ctx); err != nil {
		return fmt.Errorf("vm runtime: pause VM %s: %w", key, err)
	}
	return nil
}

func (e *engine) resume(ctx context.Context, key string) error {
	if _, err := e.requireRunning(ctx, key, "resume"); err != nil {
		return err
	}
	if err := newCHAPI(e.path(key, apiSocket)).resume(ctx); err != nil {
		return fmt.Errorf("vm runtime: resume VM %s: %w", key, err)
	}
	return nil
}

// inspect answers from the record and the unit, never from the guest.
func (e *engine) inspect(ctx context.Context, key string) (runtime.ContainerInfo, error) {
	rec, err := e.load(key)
	if err != nil {
		return runtime.ContainerInfo{}, err
	}
	if rec.Booted && !rec.Exited && !e.flag(e.booting, key) {
		up, err := e.running(ctx, key)
		if err != nil {
			return runtime.ContainerInfo{}, err
		}
		if !up {
			if code, err := e.lost(ctx, key); err != nil {
				return runtime.ContainerInfo{}, err
			} else if code >= 0 {
				rec.Exited, rec.ExitCode = true, code
			}
		}
	}
	var targets []string
	for _, m := range append(slices.Clone(rec.Checkouts), rec.Mounts...) {
		targets = append(targets, m.ContainerPath)
	}
	for _, c := range rec.CheckoutDisks {
		targets = append(targets, c.Target)
	}
	info := runtime.ContainerInfo{
		Image: rec.Image, User: rec.User, Env: append([]string(nil), rec.Env...), MountTargets: targets,
		Fingerprint: rec.Fingerprint, Exited: rec.Exited,
	}
	if rec.Exited {
		info.ExitCode = rec.ExitCode
	}
	return info, nil
}

func (e *engine) find(key string) (runtime.ID, error) {
	if _, err := e.load(key); err != nil {
		return "", err
	}
	return runtime.ID(idPrefix + key), nil
}

// initPID is the VM monitor's PID, whose cgroup is the VM's unit.
func (e *engine) initPID(ctx context.Context, key string) (int, error) {
	if _, err := e.load(key); err != nil {
		return 0, err
	}
	st, err := e.units.State(ctx, unitName(key))
	if err != nil || !st.Active {
		return 0, err
	}
	return st.MainPID, nil
}

func (e *engine) containerIP(ctx context.Context, key string) (string, error) {
	rec, err := e.requireRunning(ctx, key, "address")
	if err != nil {
		return "", err
	}
	if rec.Net == offline {
		return "", fmt.Errorf("vm runtime: VM %s has no network", key)
	}
	_, addr := netAddrs(rec.Net)
	return addr.String(), nil
}
