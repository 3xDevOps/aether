package scheduler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/3xDevOps/Aether/internal/browser"
	"github.com/3xDevOps/Aether/internal/domain"
	containerruntime "github.com/3xDevOps/Aether/internal/runtime"
)

const browserReservationBytes int64 = (1 << 30) + containerruntime.BrowserSharedMemoryBytes

func (s *Scheduler) reserveBrowser(id domain.RunID) error {
	d := s.developmentState()
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.reserved[id] {
		return nil
	}
	available, total, err := developmentMemory()
	if err != nil {
		return fmt.Errorf("browser resource admission: %w", err)
	}
	count := len(d.reserved) + 1
	if count > runtime.NumCPU() || int64(count)*browserReservationBytes > total || available < browserReservationBytes {
		return errors.New("browser resource admission: insufficient CPU/memory capacity (requires 1 CPU, 1 GiB memory and 256 MiB shared memory)")
	}
	d.reserved[id] = true
	return nil
}
func (s *Scheduler) developmentClosedPath(id domain.RunID) string {
	sum := sha256.Sum256([]byte(id))
	return filepath.Join(s.cfg.StateDir, "development-closed", hex.EncodeToString(sum[:16]))
}
func (s *Scheduler) checkDevelopmentOpen(id domain.RunID, cid containerruntime.ID) error {
	data, err := os.ReadFile(s.developmentClosedPath(id))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if string(data) == string(cid) {
		return errors.New("development environment was closed; explicitly relaunch the run before starting new surfaces")
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
	if status.State != "paused" {
		return reconcileErr
	}
	if err := s.reserveBrowser(id); err != nil {
		return err
	}
	run := browser.Run{ID: string(id), ContainerID: cid}
	if _, err := d.manager.Resume(ctx, run); err != nil {
		return err
	}
	status, client, reconcileErr = d.manager.Reconcile(ctx, run)
	if reconcileErr != nil {
		return reconcileErr
	}
	return s.clearRecoveredBrowserInput(id, status, client)
}

// StopDevelopmentRun is called only after the terminal report/evidence commit.
// It never clears a mission takeover hold or destroys the run's checkout.
func (s *Scheduler) StopDevelopmentRun(ctx context.Context, id domain.RunID) error {
	cid := s.developmentContainer(id)
	if cid == "" {
		return nil
	}
	if err := s.closeDevelopmentAdmission(id, cid); err != nil {
		return err
	}
	if err := s.stopDevelopmentProcesses(ctx, id, cid); err != nil {
		return err
	}
	d := s.developmentState()
	lock := d.lock(id)
	lock.Lock()
	defer lock.Unlock()
	if d.manager != nil {
		if err := d.manager.Remove(ctx, browser.Run{ID: string(id), ContainerID: cid}); err != nil {
			return err
		}
	}
	d.mu.Lock()
	delete(d.reserved, id)
	d.mu.Unlock()
	return nil
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
	d := s.developmentState()
	for _, entry := range entries {
		if err := s.RecoverDevelopmentTerminals(ctx, entry.runID); err != nil {
			return err
		}
		if d.manager == nil || entry.containerID == "" {
			continue
		}
		lock := d.lock(entry.runID)
		lock.Lock()
		status, client, err := d.manager.Reconcile(ctx, browser.Run{ID: string(entry.runID), ContainerID: entry.containerID})
		if !errors.Is(err, os.ErrNotExist) {
			d.mu.Lock()
			d.reserved[entry.runID] = true
			d.mu.Unlock()
		}
		if err == nil {
			if clearErr := s.clearRecoveredBrowserInput(entry.runID, status, client); clearErr != nil {
				lock.Unlock()
				return clearErr
			}
		}
		// Unknown/lost sessions stay unavailable; no create/restart on recovery.
		if entry.freeze && status.State == "running" {
			_, err = d.manager.Pause(ctx, browser.Run{ID: string(entry.runID), ContainerID: entry.containerID})
			if err != nil {
				lock.Unlock()
				return err
			}
		}
		lock.Unlock()
	}
	return nil
}

// RunCoAuthors shares the established run attribution convention with native
// Git/gh operations inside the same account environment.
func (s *Scheduler) RunCoAuthors(ctx context.Context, run domain.Run, authorEmail string) ([]string, error) {
	return s.containerCoAuthors(ctx, &run, authorEmail)
}

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
			if !terminal.State.Exited {
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
		stopErr := s.StopDevelopmentTerminals(ctx, id)
		pauseErr := s.cfg.Runtime.Pause(context.WithoutCancel(ctx), cid)
		if pauseErr == nil {
			s.setPaused(entry, true)
		}
		return errors.Join(stopErr, pauseErr)
	}
	return s.StopDevelopmentTerminals(ctx, id)
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
	d := s.developmentState()
	lock := d.lock(id)
	lock.Lock()
	defer lock.Unlock()
	if d.manager != nil {
		if err := d.manager.Remove(ctx, browser.Run{ID: string(id), ContainerID: cid}); err != nil {
			return err
		}
	}
	d.mu.Lock()
	delete(d.reserved, id)
	d.mu.Unlock()
	return nil
}
