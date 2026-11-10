package scheduler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/3xDevOps/Aether/internal/browser"
	"github.com/3xDevOps/Aether/internal/domain"
	containerruntime "github.com/3xDevOps/Aether/internal/runtime"
	"github.com/3xDevOps/Aether/internal/store"
)

func (s *Scheduler) reserveBrowser(id domain.RunID) error {
	d := s.developmentState()
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.reserved[id] {
		return nil
	}
	if len(d.reserved) >= runtime.NumCPU() {
		return fmt.Errorf("%w: each browser requires one CPU; %d already reserved", ErrBrowserCPUCapacity, len(d.reserved))
	}
	d.reserved[id] = true
	return nil
}
func (s *Scheduler) developmentClosedPath(id domain.RunID) string {
	sum := sha256.Sum256([]byte(id))
	return filepath.Join(s.cfg.StateDir, "development-closed", hex.EncodeToString(sum[:16]))
}

// ErrDevelopmentClosed refuses a new surface in the container of a run that
// is being closed.
var ErrDevelopmentClosed = errors.New("development environment was closed; explicitly relaunch the session before starting new surfaces")

func (s *Scheduler) checkDevelopmentOpen(id domain.RunID, cid containerruntime.ID) error {
	data, err := os.ReadFile(s.developmentClosedPath(id))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if string(data) == string(cid) {
		return ErrDevelopmentClosed
	}
	return nil
}
func (s *Scheduler) closeDevelopmentAdmission(id domain.RunID, cid containerruntime.ID) error {
	path := s.developmentClosedPath(id)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(cid), 0o600); err != nil {
		return err
	}
	if s.cfg.Control != nil {
		if _, err := s.cfg.Control.RevokeRunSurfaces(string(id), func() error { return nil }); err != nil {
			return err
		}
	}
	return nil
}
func (s *Scheduler) developmentContainer(id domain.RunID) containerruntime.ID {
	s.mu.Lock()
	entry := s.runs[id]
	var cid containerruntime.ID
	if entry != nil {
		cid = entry.containerID
	}
	s.mu.Unlock()
	if cid == "" {
		if sc, err := s.readSidecar(id); err == nil {
			cid = containerruntime.ID(sc.ContainerID)
		}
	}
	return cid
}

// prepareDevelopmentClose fences new input and stops process groups before the
// parent container is frozen. Their immutable screens remain for evidence.
func (s *Scheduler) prepareDevelopmentClose(ctx context.Context, id domain.RunID) error {
	cid := s.developmentContainer(id)
	if cid == "" {
		return nil
	}
	if err := s.closeDevelopmentAdmission(id, cid); err != nil {
		return err
	}
	if err := s.pauseDevelopmentBrowser(ctx, id, cid); err != nil {
		return err
	}
	return s.stopDevelopmentProcesses(ctx, id, cid)
}
func (s *Scheduler) pauseDevelopmentBrowser(ctx context.Context, id domain.RunID, cid containerruntime.ID) error {
	d := s.developmentState()
	if d.manager == nil {
		return nil
	}
	lock := d.lock(id)
	lock.Lock()
	defer lock.Unlock()
	run := browser.Run{ID: string(id), ContainerID: cid}
	status, client, probeErr := d.manager.Reconcile(ctx, run)
	if errors.Is(probeErr, os.ErrNotExist) {
		return nil
	}
	if status.State == "exited" || status.State == "dead" || status.State == "removed" {
		return nil
	}
	if probeErr == nil {
		if err := s.clearRecoveredBrowserInput(id, status, client); err != nil {
			return err
		}
	}
	_, err := d.manager.Pause(ctx, run)
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, containerruntime.ErrNotFound) {
		return nil
	}
	return err
}
func (s *Scheduler) resumeDevelopmentBrowser(ctx context.Context, id domain.RunID, cid containerruntime.ID) error {
	d := s.developmentState()
	if d.manager == nil {
		return nil
	}
	lock := d.lock(id)
	lock.Lock()
	defer lock.Unlock()
	status, client, reconcileErr := d.manager.Reconcile(ctx, browser.Run{ID: string(id), ContainerID: cid})
	if errors.Is(reconcileErr, os.ErrNotExist) {
		return nil
	}
	if reconcileErr == nil {
		return s.clearRecoveredBrowserInput(id, status, client)
	}
	// A companion recorded unavailable or ended must not hold its session
	// paused: it can be reset only once the session runs.
	if status.State != "paused" {
		if status.Unavailable != "" || status.State == "exited" || status.State == "dead" || status.State == "removed" {
			return nil
		}
		return reconcileErr
	}
	if err := s.reserveBrowser(id); err != nil {
		return err
	}
	run := browser.Run{ID: string(id), ContainerID: cid}
	if resumed, err := d.manager.Resume(ctx, run); err != nil {
		if resumed.Unavailable != "" {
			return nil
		}
		return err
	}
	status, client, reconcileErr = d.manager.Reconcile(ctx, run)
	if reconcileErr != nil {
		return reconcileErr
	}
	return s.clearRecoveredBrowserInput(id, status, client)
}

// StopDevelopmentRun is called only after the terminal report/evidence commit.
// It never destroys the run's checkout.
func (s *Scheduler) StopDevelopmentRun(ctx context.Context, id domain.RunID) error {
	if cid := s.developmentContainer(id); cid != "" {
		if err := s.closeDevelopmentAdmission(id, cid); err != nil {
			return err
		}
		if err := s.stopDevelopmentProcesses(ctx, id, cid); err != nil {
			return err
		}
	}
	return s.removeDevelopmentBrowser(ctx, id)
}

func (s *Scheduler) removeDevelopmentBrowser(ctx context.Context, id domain.RunID) error {
	d := s.developmentState()
	lock := d.lock(id)
	lock.Lock()
	defer lock.Unlock()
	if d.manager != nil {
		if err := d.manager.Remove(ctx, string(id)); err != nil {
			return err
		}
	}
	d.releaseBrowser(id)
	return nil
}

func (d *developmentState) releaseBrowser(id domain.RunID) {
	d.mu.Lock()
	delete(d.reserved, id)
	d.mu.Unlock()
}

const browserSweepTimeout = 30 * time.Second

// sweepBrowsers removes this server's journaled companions that no open
// session can reach: their session ended or is unknown to the store, or the
// session container whose network they share is gone.
func (s *Scheduler) sweepBrowsers(ctx context.Context) {
	d := s.developmentState()
	if d.manager == nil {
		return
	}
	journals, err := d.manager.Journals()
	d.reportSweep("journals", "scheduler: read browser journals", err)
	for _, status := range journals {
		id := domain.RunID(status.RunID)
		d.reportSweep(status.RunID, "scheduler: remove ended session's browser companion", s.sweepBrowser(ctx, id, status))
	}
}

func (s *Scheduler) sweepBrowser(ctx context.Context, id domain.RunID, status browser.Status) error {
	ctx, cancel := context.WithTimeout(ctx, browserSweepTimeout)
	defer cancel()
	d := s.developmentState()
	lock := d.lock(id)
	lock.Lock()
	defer lock.Unlock()
	ended, err := s.browserSessionEnded(ctx, id, status.RunContainer)
	if err != nil || !ended {
		return err
	}
	err = d.manager.RemoveObserved(ctx, status)
	if errors.Is(err, browser.ErrReplaced) {
		return nil
	}
	if err == nil {
		d.releaseBrowser(id)
	}
	return err
}

// reportSweep warns once per distinct failure of key; the hourly sweep would
// otherwise repeat a failure that needs an operator, such as a foreign journal.
func (d *developmentState) reportSweep(key, msg string, err error) {
	d.mu.Lock()
	text := ""
	if err != nil {
		text = err.Error()
	}
	repeated := d.sweepFailures[key] == text
	if text == "" {
		delete(d.sweepFailures, key)
	} else {
		d.sweepFailures[key] = text
	}
	d.mu.Unlock()
	switch {
	case err == nil:
	case repeated:
		slog.Debug(msg, "key", key, "error", err)
	default:
		slog.Warn(msg, "key", key, "error", err)
	}
}

func (s *Scheduler) browserSessionEnded(ctx context.Context, id domain.RunID, runContainer containerruntime.ID) (bool, error) {
	s.mu.Lock()
	entry := s.runs[id]
	open := s.pending[id] != nil || (entry != nil && !entry.status.Terminal())
	s.mu.Unlock()
	if !open {
		r, err := s.cfg.Store.GetRun(ctx, id)
		if errors.Is(err, store.ErrNotFound) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
		if r.Status.Terminal() {
			return true, nil
		}
	}
	return s.containerGone(ctx, runContainer), nil
}

func (s *Scheduler) containerGone(ctx context.Context, id containerruntime.ID) bool {
	if id == "" {
		return false
	}
	_, err := s.cfg.Runtime.Inspect(ctx, id)
	return errors.Is(err, containerruntime.ErrNotFound)
}
func (s *Scheduler) reopenDevelopment(ctx context.Context, id domain.RunID) error {
	if err := s.ReopenDevelopmentTerminals(ctx, id); err != nil {
		return err
	}
	err := os.Remove(s.developmentClosedPath(id))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
func (s *Scheduler) deleteDevelopment(ctx context.Context, id domain.RunID) error {
	if err := s.StopDevelopmentRun(ctx, id); err != nil {
		return err
	}
	if err := s.DeleteDevelopmentTerminals(ctx, id); err != nil {
		return err
	}
	if dir, err := s.captureDir(id); err == nil {
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
	}
	if err := os.Remove(s.developmentClosedPath(id) + ".browser-session"); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	err := os.Remove(s.developmentClosedPath(id))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// recoverDevelopment never fails startup for one session: a session whose
// terminals or browser companion cannot be recovered is logged, and a
// companion that cannot release held input is kept unavailable until the
// session's browser is reset. Sessions recover concurrently, so hung
// companions do not add up past the update trial's deadline.
func (s *Scheduler) recoverDevelopment(ctx context.Context) error {
	type recoveredDevelopment struct {
		runID       domain.RunID
		containerID containerruntime.ID
		freeze      bool
	}
	s.mu.Lock()
	entries := make([]recoveredDevelopment, 0, len(s.runs))
	for _, entry := range s.runs {
		entries = append(entries, recoveredDevelopment{runID: entry.runID, containerID: entry.containerID, freeze: entry.paused || entry.retained || entry.status.Terminal()})
	}
	s.mu.Unlock()
	var wg sync.WaitGroup
	slots := make(chan struct{}, browserRecoveryConcurrency)
	for _, entry := range entries {
		wg.Go(func() {
			slots <- struct{}{}
			defer func() { <-slots }()
			if ctx.Err() != nil {
				return
			}
			if err := s.RecoverDevelopmentTerminals(ctx, entry.runID); err != nil && ctx.Err() == nil {
				slog.Warn("scheduler: recovery: session terminals unavailable", "run", entry.runID, "error", err)
			}
			if err := s.recoverDevelopmentBrowser(ctx, entry.runID, entry.containerID, entry.freeze); err != nil && ctx.Err() == nil {
				slog.Warn("scheduler: recovery: session browser", "run", entry.runID, "error", err)
			}
		})
	}
	wg.Wait()
	return ctx.Err()
}

const (
	browserRecoveryTimeout     = 30 * time.Second
	browserRecoveryConcurrency = 8
)

func (s *Scheduler) recoverDevelopmentBrowser(parent context.Context, id domain.RunID, cid containerruntime.ID, freeze bool) error {
	d := s.developmentState()
	if d.manager == nil || cid == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(parent, browserRecoveryTimeout)
	defer cancel()
	lock := d.lock(id)
	lock.Lock()
	defer lock.Unlock()
	run := browser.Run{ID: string(id), ContainerID: cid}
	status, client, err := d.manager.Reconcile(ctx, run)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	d.mu.Lock()
	d.reserved[id] = true
	d.mu.Unlock()
	var releaseErr error
	if err == nil {
		releaseErr = s.clearRecoveredBrowserInput(id, status, client)
	}
	// Unknown/lost sessions stay unavailable; no create/restart on recovery.
	var pauseErr error
	if freeze && status.State == "running" {
		if _, pauseErr = d.manager.Pause(ctx, run); pauseErr != nil {
			pauseErr = fmt.Errorf("session %s browser companion %s: pause: %w", id, status.ContainerID, pauseErr)
		}
	}
	if releaseErr == nil || parent.Err() != nil {
		return pauseErr
	}
	if markErr := d.manager.MarkUnavailable(run, "startup recovery failed: "+releaseErr.Error()); markErr != nil {
		releaseErr = errors.Join(releaseErr, fmt.Errorf("record the companion as unavailable: %w", markErr))
	}
	return errors.Join(releaseErr, pauseErr)
}

// RunCoAuthors shares the established run attribution convention with native
// Git/gh operations inside the same account environment.
func (s *Scheduler) RunCoAuthors(ctx context.Context, run domain.Run, authorEmail string) ([]string, error) {
	return s.containerCoAuthors(ctx, &run, authorEmail)
}

// stopDevelopmentProcesses stops the run's terminals on the way to freezing,
// stopping or destroying its container, so a terminal whose helper is lost
// does not block it.
func (s *Scheduler) stopDevelopmentProcesses(ctx context.Context, id domain.RunID, cid containerruntime.ID) error {
	s.mu.Lock()
	entry := s.runs[id]
	paused := entry != nil && entry.paused
	s.mu.Unlock()
	lock := s.lockForShell(id)
	lock.Lock()
	set, err := s.loadRunTerminalsLocked(ctx, id)
	needStop := false
	if err == nil {
		for _, terminal := range set.Terminals {
			if !terminal.State.Exited && !terminal.Lost {
				needStop = true
				break
			}
		}
	}
	lock.Unlock()
	if err != nil {
		return err
	}
	if paused && needStop {
		if err := s.cfg.Runtime.Resume(ctx, cid); err != nil {
			return err
		}
		s.setPaused(entry, false)
		stopErr := s.stopDevelopmentTerminals(ctx, id)
		pauseErr := s.cfg.Runtime.Pause(context.WithoutCancel(ctx), cid)
		if pauseErr == nil {
			s.setPaused(entry, true)
		}
		return errors.Join(stopErr, pauseErr)
	}
	return s.stopDevelopmentTerminals(ctx, id)
}

func (s *Scheduler) stopDevelopmentTerminals(ctx context.Context, id domain.RunID) error {
	stopErr := s.StopDevelopmentTerminals(ctx, id)
	if stopErr == nil {
		return stopErr
	}
	settled, err := s.settleLostDevelopmentTerminals(ctx, id)
	if err != nil {
		return errors.Join(stopErr, err)
	}
	if !settled {
		return stopErr
	}
	return nil
}

func (s *Scheduler) destroyDevelopmentContainer(ctx context.Context, id domain.RunID, cid containerruntime.ID) error {
	if err := s.closeDevelopmentAdmission(id, cid); err != nil {
		return err
	}
	if err := s.pauseDevelopmentBrowser(ctx, id, cid); err != nil {
		return err
	}
	if err := s.cfg.Runtime.Destroy(ctx, cid); err != nil && !errors.Is(err, containerruntime.ErrNotFound) {
		return err
	}
	if err := s.MarkDevelopmentContainerEnded(ctx, id); err != nil {
		return err
	}
	return s.removeDevelopmentBrowser(ctx, id)
}
