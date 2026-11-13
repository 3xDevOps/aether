package vm

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/3xDevOps/Aether/internal/guest"
	"github.com/3xDevOps/Aether/internal/runtime"
)

// The development browser runs inside its session's VM, from the browser
// image's disk attached at boot (docs/vm-guest.md, "The development
// browser"). Its runtime ID is the session VM's key under browserPrefix.
const (
	browserPrefix  = "vmb-"
	browserProgram = "/usr/local/bin/node"
	browserServer  = "/opt/aether-browser/server.mjs"
	browserBuild   = time.Hour
)

// companion is the browser companion created in a VM.
type companion struct {
	CreationKey string  `json:"creation_key"`
	Control     string  `json:"control"`
	MemoryBytes int64   `json:"memory_bytes"`
	CPULimit    float64 `json:"cpu_limit,omitempty"`
	Started     bool    `json:"started,omitempty"`
	Paused      bool    `json:"paused,omitempty"`
}

var warming atomic.Bool

// browserUser is never the session's user, so the session's processes can
// neither signal nor trace the companion's.
func browserUser(session string) string {
	if uid, _, _ := strings.Cut(session, ":"); uid == "65533" {
		return "65532:65532"
	}
	return "65533:65533"
}

func browserKey(id runtime.ID) (string, bool) {
	return strings.CutPrefix(string(id), browserPrefix)
}

// attachBrowser gives a new session VM the browser's disk, if its image was
// built: one not built yet is built in the background for the next VM, so
// no session waits on a browser it may never open.
func (e *engine) attachBrowser(ctx context.Context, rec *record) error {
	if e.cfg.BrowserImage == "" {
		return nil
	}
	base, err := e.cfg.Images.LookupBase(e.cfg.BrowserImage)
	if errors.Is(err, runtime.ErrNotFound) {
		e.warmBrowser()
		return nil
	}
	if err != nil {
		return fmt.Errorf("vm runtime: the browser's disk: %w", err)
	}
	if rec.Browser, err = e.cfg.Images.NewRootDisk(ctx, base, e.path(rec.Key, browserDisk)); err != nil {
		return fmt.Errorf("vm runtime: the browser's disk: %w", err)
	}
	rec.BrowserImage, rec.BrowserEnv = e.cfg.BrowserImage, base.Config.Env
	return nil
}

func (e *engine) warmBrowser() {
	if e.cfg.BrowserImage == "" || !warming.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer warming.Store(false)
		ctx, cancel := context.WithTimeout(context.Background(), browserBuild)
		defer cancel()
		if _, err := e.cfg.Images.BaseDisk(ctx, e.cfg.BrowserImage); err != nil {
			slog.Warn("vm runtime: build the browser's disk", "image", e.cfg.BrowserImage, "error", err)
		}
	}()
}

func (e *engine) createBrowser(ctx context.Context, key string, spec runtime.BrowserSpec) (runtime.ID, error) {
	if err := spec.Validate(); err != nil {
		return "", err
	}
	unlock := e.lock(key)
	defer unlock()
	rec, err := e.requireRunning(ctx, key, "open the browser in")
	if err != nil {
		return "", err
	}
	// A VM runs the companion of the browser image it booted with, which
	// the client's protocol check holds to one the server speaks; the next
	// cold start takes the current one.
	switch {
	case e.paused(ctx, key):
		return "", fmt.Errorf("vm runtime: open the browser in VM %s: it is paused", key)
	case rec.Browser.Path == "":
		return "", fmt.Errorf("vm runtime: VM %s started while the server was still building the browser's disk; the browser works once the session's VM starts again, as after it sleeps", key)
	}
	if rec.Companion != nil {
		if err = e.stopCompanion(ctx, key); err != nil {
			return "", err
		}
	}
	rec.Companion = &companion{CreationKey: spec.CreationKey, Control: spec.ControlHostPath, MemoryBytes: spec.MemoryLimitBytes, CPULimit: spec.CPULimit}
	if err = e.store(rec); err != nil {
		return "", err
	}
	return runtime.ID(browserPrefix + key), nil
}

func (e *engine) startBrowser(ctx context.Context, key string) error {
	unlock := e.lock(key)
	defer unlock()
	rec, err := e.requireRunning(ctx, key, "start the browser in")
	if err != nil {
		return err
	}
	c := rec.Companion
	if c == nil {
		return fmt.Errorf("vm runtime: VM %s has no browser: %w", key, runtime.ErrNotFound)
	}
	h, err := e.helper(key)
	if err != nil {
		return err
	}
	env := overrideEnv(rec.BrowserEnv, []string{
		"HOME=/tmp/aether-browser-home", "NODE_ENV=production", "PLAYWRIGHT_BROWSERS_PATH=/opt/playwright",
		"AETHER_BROWSER_CREATION_KEY=" + c.CreationKey,
	})
	err = h.StartBrowser(ctx, guest.BrowserSpec{
		Process: guest.ProcessSpec{
			ID: c.CreationKey, Argv: []string{browserProgram, browserServer}, Env: env, User: browserUser(rec.User),
			Dir: filepath.Dir(browserServer), NoNewPrivileges: true,
		},
		MemoryBytes: uint64(max(c.MemoryBytes, 0)), CPUMillis: uint32(math.Ceil(c.CPULimit * 1000)),
		ShmBytes: uint64(runtime.BrowserSharedMemoryBytes),
	})
	if err != nil {
		return fmt.Errorf("vm runtime: start the browser in VM %s: %w", key, err)
	}
	c.Started, c.Paused = true, false
	if err = e.store(rec); err != nil {
		return err
	}
	return e.relay(key, guest.BrowserPort, filepath.Join(c.Control, "browser.sock"))
}

func (e *engine) pauseBrowser(ctx context.Context, key string, paused bool) error {
	unlock := e.lock(key)
	defer unlock()
	rec, err := e.requireRunning(ctx, key, "pause the browser in")
	if err != nil {
		return err
	}
	if rec.Companion == nil {
		return fmt.Errorf("vm runtime: VM %s has no browser: %w", key, runtime.ErrNotFound)
	}
	h, err := e.helper(key)
	if err != nil {
		return err
	}
	if err = h.PauseBrowser(ctx, paused); err != nil {
		return fmt.Errorf("vm runtime: browser in VM %s: %w", key, err)
	}
	rec.Companion.Paused = paused
	return e.store(rec)
}

// destroyBrowser ends the companion; a VM or companion already gone is not
// an error.
func (e *engine) destroyBrowser(ctx context.Context, key string) error {
	unlock := e.lock(key)
	defer unlock()
	rec, err := e.load(key)
	if errors.Is(err, runtime.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if rec.Companion == nil {
		return nil
	}
	if err = e.stopCompanion(ctx, key); err != nil {
		return err
	}
	rec.Companion = nil
	return e.store(rec)
}

// stopCompanion kills the companion in a running VM and drops its relay; it
// holds the key's lock. A paused VM, as a closed session's is, runs just
// long enough for the helper to kill the companion; its session processes
// stay as frozen as the companion's cgroup is.
func (e *engine) stopCompanion(ctx context.Context, key string) (err error) {
	e.stopRelay(key)
	up, err := e.running(ctx, key)
	if err != nil || !up {
		return err
	}
	h, err := e.helper(key)
	if err != nil {
		return err
	}
	if e.paused(ctx, key) {
		api := newCHAPI(e.path(key, apiSocket))
		if err = api.resume(ctx); err != nil {
			return fmt.Errorf("vm runtime: resume VM %s to remove its browser: %w", key, err)
		}
		defer func() {
			if pauseErr := api.pause(context.WithoutCancel(ctx)); pauseErr != nil {
				err = errors.Join(err, fmt.Errorf("vm runtime: pause VM %s again: %w", key, pauseErr))
			}
		}()
	}
	stopCtx, cancel := context.WithTimeout(ctx, helperCallTimeout+browserStopWait)
	defer cancel()
	if err = h.StopBrowser(stopCtx); err != nil {
		return fmt.Errorf("vm runtime: stop the browser in VM %s: %w", key, err)
	}
	return nil
}

// browserStopWait is how long the helper waits for a killed companion.
const browserStopWait = 10 * time.Second

// inspectBrowser reads the companion's state and, for a running one whose
// relay a server restart dropped, listens for it again.
func (e *engine) inspectBrowser(ctx context.Context, key string) (runtime.BrowserInfo, error) {
	rec, err := e.load(key)
	if err != nil {
		return runtime.BrowserInfo{}, err
	}
	c := rec.Companion
	if c == nil {
		return runtime.BrowserInfo{}, fmt.Errorf("vm runtime: VM %s has no browser: %w", key, runtime.ErrNotFound)
	}
	info := runtime.BrowserInfo{
		ContainerID: runtime.ID(browserPrefix + key), RunContainer: runtime.ID(idPrefix + key),
		CreationKey: c.CreationKey, Image: rec.BrowserImage, State: "created",
	}
	up, err := e.running(ctx, key)
	switch {
	case err != nil:
		return runtime.BrowserInfo{}, err
	case !up:
		info.State = "exited"
		return info, nil
	case !c.Started:
		return info, nil
	case c.Paused || e.paused(ctx, key):
		info.State = "paused"
		return info, nil
	}
	h, err := e.helper(key)
	if err != nil {
		return runtime.BrowserInfo{}, err
	}
	statusCtx, cancel := context.WithTimeout(ctx, helperCallTimeout)
	st, err := h.Process(c.CreationKey).Status(statusCtx)
	cancel()
	switch {
	case errors.Is(err, guest.ErrNotFound), err == nil && st.Exited:
		info.State = "exited"
	case err != nil:
		return runtime.BrowserInfo{}, fmt.Errorf("vm runtime: browser in VM %s: %w", key, err)
	default:
		info.State = "running"
		e.mu.Lock()
		relayed := e.relays[key] != nil
		e.mu.Unlock()
		if !relayed {
			if err = e.relay(key, guest.BrowserPort, filepath.Join(c.Control, "browser.sock")); err != nil {
				return runtime.BrowserInfo{}, err
			}
		}
	}
	return info, nil
}

func (e *engine) findBrowser(creationKey string) (runtime.ID, error) {
	if !strings.HasPrefix(creationKey, "browser-") {
		return "", runtime.ErrNotFound
	}
	keys, err := e.keys()
	if err != nil {
		return "", err
	}
	for _, key := range keys {
		if rec, loadErr := e.load(key); loadErr == nil && rec.Companion != nil && rec.Companion.CreationKey == creationKey {
			return runtime.ID(browserPrefix + key), nil
		}
	}
	return "", fmt.Errorf("vm runtime: browser %s: %w", creationKey, runtime.ErrNotFound)
}
