package vm

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"syscall"
	"time"

	"github.com/3xDevOps/Aether/internal/guest"
	"github.com/3xDevOps/Aether/internal/runtime"
)

const (
	defaultStopGrace = 10 * time.Second
	killGrace        = 5 * time.Second
	powerOffTimeout  = 15 * time.Second
	// lostExitCode reports a VM that ended without its helper reporting
	// Main's exit: the host killed it, the guest crashed or the host
	// rebooted.
	lostExitCode = 255
	// killedExitCode is 128+SIGKILL, what a container reports after a stop
	// had to kill.
	killedExitCode = 137
	waitPoll       = 200 * time.Millisecond
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
	if !up {
		return nil, fmt.Errorf("vm runtime: %s VM %s: it is not running", action, key)
	}
	return rec, nil
}

// stop ends Main as docker stop ends a container's main process.
func (e *engine) stop(ctx context.Context, key string, grace time.Duration) error {
	unlock := e.lock(key)
	defer unlock()
	rec, err := e.load(key)
	if err != nil {
		return err
	}
	up, err := e.running(ctx, key)
	if err != nil || !up {
		return err
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
	code := e.endMain(ctx, h.Process(guest.MainID), grace)
	return e.finish(ctx, rec, code)
}

func (e *engine) endMain(ctx context.Context, main process, grace time.Duration) int {
	if err := main.Signal(ctx, syscall.SIGTERM); errors.Is(err, guest.ErrNotFound) {
		return 0
	}
	waitFor := func(d time.Duration) (int, bool) {
		waitCtx, cancel := context.WithTimeout(ctx, d)
		defer cancel()
		code, err := main.Wait(waitCtx)
		return code, err == nil
	}
	if code, ok := waitFor(grace); ok {
		return code
	}
	_ = main.Signal(ctx, syscall.SIGKILL)
	if code, ok := waitFor(killGrace); ok {
		return code
	}
	return killedExitCode
}

// finish records Main's exit and powers the VM off; it holds the key's lock.
func (e *engine) finish(ctx context.Context, rec *record, code int) error {
	rec.Exited, rec.ExitCode = true, code
	if err := e.store(rec); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), powerOffTimeout)
	defer cancel()
	if h, err := e.helper(rec.Key); err == nil {
		_ = h.PowerOff(ctx)
	}
	e.forgetHelper(rec.Key)
	for {
		up, err := e.running(ctx, rec.Key)
		if err == nil && !up {
			return nil
		}
		select {
		case <-ctx.Done():
			return e.units.Stop(context.WithoutCancel(ctx), unitName(rec.Key))
		case <-time.After(waitPoll):
		}
	}
}

func (e *engine) kill(ctx context.Context, key string) {
	e.forgetHelper(key)
	_ = e.units.Stop(ctx, unitName(key))
}

// wait blocks until Main of the current boot exits; a later boot means it
// did. On a VM that never booted it waits for the first boot, and on a saved
// VM until it is restored, so a save is not mistaken for an exit.
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
		if rec.Booted && !rec.Saved && !e.flag(e.booting, key) {
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
// ends when the unit does.
func (e *engine) waitMain(ctx context.Context, key string, boot int) (int, bool) {
	h, err := e.helper(key)
	if err != nil {
		return 0, false
	}
	waitCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		for {
			select {
			case <-waitCtx.Done():
				return
			case <-tick.C:
				if up, upErr := e.running(waitCtx, key); upErr == nil && !up {
					cancel()
					return
				}
			}
		}
	}()
	code, err := h.Process(guest.MainID).Wait(waitCtx)
	if err != nil {
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
	if err := e.finish(ctx, rec, code); err != nil {
		return 0, false
	}
	return code, true
}

// lost records a booted VM whose unit is gone without an exit, and returns
// -1 when it turns out to have been stopped or saved properly meanwhile.
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
	if rec.Saved {
		return -1, nil
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
// error.
func (e *engine) destroy(ctx context.Context, key string) error {
	if checkKey(key) != nil {
		return nil
	}
	unlock := e.lock(key)
	defer unlock()
	if _, err := os.Stat(e.dir(key)); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	e.forgetHelper(key)
	if err := e.units.Stop(ctx, unitName(key)); err != nil {
		return err
	}
	if err := os.RemoveAll(e.dir(key)); err != nil {
		return fmt.Errorf("vm runtime: remove VM %s: %w", key, err)
	}
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
	if rec.Booted && !rec.Exited && !rec.Saved && !e.flag(e.booting, key) {
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
	targets := make([]string, len(rec.Mounts))
	for i, m := range rec.Mounts {
		targets[i] = m.ContainerPath
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

// initPID is Cloud Hypervisor's PID, whose cgroup is the VM's unit.
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
	_, addr := netAddrs(rec.Net)
	return addr.String(), nil
}
