package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/ptyhost"
	"github.com/3xDevOps/Aether/internal/runtime"
)

// woke is how a wake brought a sleeping session's container back.
type woke struct {
	// restored is a VM restored with every process it had.
	restored bool
	// restoreErr is why a VM's saved state could not be restored, so it
	// started again from its disks.
	restoreErr error
}

func (w woke) notice() string {
	switch {
	case w.restored:
		return "Aether had saved this idle session to disk and restored it. Its processes, terminals and conversation carry on where they were; connections to outside the session may have dropped while it slept."
	case w.restoreErr != nil:
		return "Aether could not restore this idle session's saved state, so it started the session again from its disk. Processes and terminals that were running were stopped; the conversation, files and checkout are kept. The restore failed: " + w.restoreErr.Error()
	}
	return "Aether had stopped this idle session's container. Processes and terminals that were running were stopped; the conversation, files and checkout are kept."
}

// saver is the runtime's SaveRuntime when it can save cid, which a session
// VM sleeps by instead of a stop.
func (s *Scheduler) saver(cid runtime.ID) (runtime.SaveRuntime, bool) {
	saver, ok := s.cfg.Runtime.(runtime.SaveRuntime)
	if !ok || cid == "" || !saver.Saves(cid) {
		return nil, false
	}
	return saver, true
}

func (s *Scheduler) entrySaver(entry *supervised) (runtime.SaveRuntime, bool) {
	s.mu.Lock()
	cid := entry.containerID
	s.mu.Unlock()
	return s.saver(cid)
}

// saveLive saves an idle open session's VM with every process in it. The
// agent, its terminals and the diff watch are only let go of, and the wake
// reattaches them. A save that fails reattaches them at once, and the run
// stays awake until it is idle again; a VM nothing could reattach to, or a
// server shutting down, stays running and marked asleep, and the wake or
// the next server's recovery takes it up. Caller holds entry.lifecycleMu,
// set sleeping and marked the run asleep.
func (s *Scheduler) saveLive(ctx context.Context, entry *supervised, saver runtime.SaveRuntime) error {
	s.mu.Lock()
	cid, enhanced := entry.containerID, entry.acp && entry.launchMode == domain.LaunchACP
	facts := s.liveFactsLocked(entry)
	s.mu.Unlock()
	// The agent may have started a turn of its own since the sweep looked;
	// once detached, nothing here would see it.
	if s.sessionFacts(entry.runID, enhanced, true, facts).busy != "" {
		return errors.Join(errStayAwake, s.unmarkAsleep(entry))
	}
	s.mu.Lock()
	// From here exec and terminal recovery treat the VM as frozen.
	entry.paused = true
	s.mu.Unlock()
	// A shutdown must not cut a save short: it would leave the VM half
	// detached.
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), wakeTimeout)
	defer cancel()
	detachErr := s.detachForSave(saveCtx, entry)
	saveErr := saver.Save(saveCtx, cid)
	if saveErr == nil {
		recordErr := s.recordSaved(entry, true)
		s.endSleep(entry, true)
		return errors.Join(detachErr, recordErr)
	}
	saveErr = errors.Join(fmt.Errorf("save the idle session: %w", saveErr), detachErr)
	s.mu.Lock()
	entry.saveFailedAt = time.Now().UTC()
	s.mu.Unlock()
	if ctx.Err() != nil {
		s.endSleep(entry, true)
		return saveErr
	}
	s.mu.Lock()
	entry.paused = false
	s.mu.Unlock()
	if err := s.reattach(ctx, entry, true); err != nil {
		s.endSleep(entry, true)
		return errors.Join(saveErr, err)
	}
	return errors.Join(saveErr, s.unmarkAsleep(entry))
}

// detachForSave lets go of everything this server holds in entry's VM: the
// agent's session and terminal, the development terminals and the diff
// watch. What they connect to keeps running, and is saved with the VM, so
// the save goes ahead whatever this returns: a keeper or terminal left
// attached is preempted by the next generation that attaches after the
// restore.
func (s *Scheduler) detachForSave(ctx context.Context, entry *supervised) error {
	run := entry.runID
	s.cfg.Git.StopDiffWatch(run)
	var errs []error
	if err := s.detachDevelopmentTerminals(ctx, run); err != nil {
		errs = append(errs, fmt.Errorf("let go of the terminals: %w", err))
	}
	s.cfg.PTY.StopSessionsWithPrefix(ctx, string(ptyhost.RunShellSession(run, "")))
	s.mu.Lock()
	acpDriven := entry.acp && !entry.appServer
	s.mu.Unlock()
	if !acpDriven {
		// The Standard drivers only hold the terminal and the bridge.
		if err := s.entryDriver(entry).Stop(ctx, run); err != nil {
			errs = append(errs, fmt.Errorf("let go of the agent: %w", err))
		}
		return errors.Join(errs...)
	}
	if err := s.acp.detach(ctx, run); err != nil {
		errs = append(errs, fmt.Errorf("let go of the agent: %w", err))
	}
	if err := s.cfg.PTY.StopSession(ctx, ptyhost.RunSession(run)); err != nil && !errors.Is(err, ptyhost.ErrNoSession) {
		errs = append(errs, fmt.Errorf("let go of the agent's terminal: %w", err))
	}
	return errors.Join(errs...)
}

// reattach takes up the session in entry's running container again: its
// agent, terminal and diff watch, and the development terminals a save kept
// running, or reopens them after a start ended them.
func (s *Scheduler) reattach(ctx context.Context, entry *supervised, restored bool) error {
	s.mu.Lock()
	cid, workspace := entry.containerID, entry.workspaceID
	s.mu.Unlock()
	att, err := s.cfg.Runtime.Attach(ctx, cid)
	if err != nil {
		return err
	}
	if err = s.entryDriver(entry).Resume(ctx, entry, att); err != nil {
		_ = att.Close()
		return fmt.Errorf("restore the agent's conversation (if this keeps failing, launch a new session on this session's branch, which is kept with its checkout): %w", err)
	}
	if err = s.cfg.Git.StartDiffWatch(ctx, workspace, entry.runID); err != nil {
		return err
	}
	if restored {
		if err = s.RecoverDevelopmentTerminals(ctx, entry.runID); err != nil {
			return fmt.Errorf("reattach to the terminals: %w", err)
		}
		return nil
	}
	return s.reopenDevelopment(ctx, entry.runID)
}

// restoreAsleep restores entry's saved VM under a fresh wait. A saved state
// that cannot be restored falls back to a cold start from the VM's disks
// when the agent restores its conversation there; otherwise the wake fails
// with the restore's error and the VM keeps its disks. Caller holds
// entry.lifecycleMu and has reserved capacity.
func (s *Scheduler) restoreAsleep(ctx context.Context, entry *supervised, saver runtime.SaveRuntime) (woke, error) {
	s.mu.Lock()
	cid, closed := entry.containerID, entry.retained
	s.mu.Unlock()
	restoreErr := saver.Restore(ctx, cid)
	if restoreErr == nil {
		// A closed session was saved paused, and comes back paused.
		if closed {
			if err := s.cfg.Runtime.Resume(ctx, cid); err != nil {
				return woke{}, fmt.Errorf("resume the restored session: %w", err)
			}
		}
		s.superviseWoken(entry)
		return woke{restored: true}, nil
	}
	if errors.Is(restoreErr, runtime.ErrNotFound) {
		return woke{}, restoreErr
	}
	// A save that failed, or a restore that failed once the guest was
	// restored, left the VM up with every process: it is taken up as it is.
	// One the restore left paused runs again; resuming a running one fails,
	// which is harmless, and the reattach that follows proves it runs.
	if info, err := s.cfg.Runtime.Inspect(ctx, cid); err == nil && !info.Exited {
		_ = s.cfg.Runtime.Resume(ctx, cid)
		s.superviseWoken(entry)
		return woke{restored: true}, nil
	}
	if reason := s.coldStartRefusal(ctx, entry); reason != "" {
		return woke{}, fmt.Errorf("restore the saved session: %w; %s", restoreErr, reason)
	}
	slog.Warn("scheduler: restore a sleeping session; starting it from its disk", "run", entry.runID, "error", restoreErr)
	// Stop discards the saved state and a half-restored VM alike.
	if err := s.cfg.Runtime.Stop(ctx, cid, s.cfg.StopGrace); err != nil {
		return woke{}, errors.Join(restoreErr, fmt.Errorf("discard the saved state: %w", err))
	}
	if err := s.MarkDevelopmentContainerEnded(ctx, entry.runID); err != nil {
		return woke{}, fmt.Errorf("record the terminals the cold start ended: %w", err)
	}
	// The browser in the VM did not survive; the next Browser call starts
	// another.
	if err := s.removeDevelopmentBrowser(ctx, entry.runID); err != nil {
		s.mu.Lock()
		entry.browserLeft = true
		s.mu.Unlock()
	}
	if err := s.cfg.Runtime.Start(ctx, cid); err != nil {
		return woke{}, errors.Join(restoreErr, err)
	}
	s.mu.Lock()
	// The agent and its keeper did not survive, so the open restores the
	// conversation rather than trying to reattach.
	entry.agentExec, entry.agentKeeper = nil, nil
	s.mu.Unlock()
	s.superviseWoken(entry)
	return woke{restoreErr: restoreErr}, nil
}

// coldStartRefusal is why entry cannot start again from its disk, empty
// when it can: only an Enhanced session of an agent that restores its
// conversation, in a VM created for Enhanced, whose main process is a bare
// shell. A Standard session's would run its launch command, with the task,
// again.
func (s *Scheduler) coldStartRefusal(ctx context.Context, entry *supervised) string {
	s.mu.Lock()
	enhanced, saved := entry.acp && entry.launchMode == domain.LaunchACP, entry.asleepSaved
	s.mu.Unlock()
	keep := "it cannot be restored, and its disk is kept: launch a new session on its branch"
	if saved {
		keep = "its saved state and disk are kept: try waking it again, or launch a new session on its branch"
	}
	if !enhanced || !s.launchedEnhanced(ctx, entry) {
		return "a Standard session cannot start again from its disk without running its task again; " + keep
	}
	run, err := s.cfg.Store.GetRun(ctx, entry.runID)
	if err != nil {
		return "load the session: " + err.Error()
	}
	if !s.resumes(run.Harness) {
		return fmt.Sprintf("%s cannot restore its conversation in a new process; %s", run.Harness, keep)
	}
	return ""
}

// superviseWoken waits on entry's container again after a wake started or
// restored it.
func (s *Scheduler) superviseWoken(entry *supervised) {
	s.mu.Lock()
	// The ACP driver will not exec into a container it thinks is frozen.
	entry.paused = false
	entry.asleepSaved = false
	entry.sleepGen++
	entry.waitStarted = true
	s.wg.Add(1)
	s.mu.Unlock()
	go s.superviseWait(entry)
}

// recordSaved records whether entry's sleeping VM holds its saved memory.
func (s *Scheduler) recordSaved(entry *supervised, saved bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry.asleepSaved = saved
	if err := s.writeSidecar(entry.sidecar()); err != nil {
		return fmt.Errorf("record whether the sleeping session is saved: %w", err)
	}
	return nil
}

// resaveAsleep saves entry's VM again after a failed wake reattached part of
// it. A closed session is paused first, as saveClosed saves it, for the
// next reopen's restore to resume. A VM that cannot be saved stays up, for
// the next wake to take up. Caller holds entry.lifecycleMu.
func (s *Scheduler) resaveAsleep(ctx context.Context, entry *supervised, saver runtime.SaveRuntime) error {
	s.mu.Lock()
	cid, closed := entry.containerID, entry.retained
	entry.paused = true
	s.mu.Unlock()
	detachErr := s.detachForSave(ctx, entry)
	if closed {
		if err := s.cfg.Runtime.Pause(ctx, cid); err != nil {
			return errors.Join(fmt.Errorf("pause the closed session to save it again: %w", err), detachErr)
		}
	}
	if err := saver.Save(ctx, cid); err != nil {
		return errors.Join(fmt.Errorf("save the session again: %w", err), detachErr)
	}
	return errors.Join(detachErr, s.recordSaved(entry, true))
}

// saveClosed saves a closed session's VM as the close's pause left it, so no
// process writes between the close's commit and the save; the reopen's
// restore resumes it. A save that fails leaves it paused. Caller holds
// entry.lifecycleMu, set sleeping and marked the run asleep.
func (s *Scheduler) saveClosed(ctx context.Context, entry *supervised, saver runtime.SaveRuntime) error {
	s.mu.Lock()
	cid := entry.containerID
	s.mu.Unlock()
	if err := saver.Save(ctx, cid); err != nil {
		s.mu.Lock()
		entry.saveFailedAt = time.Now().UTC()
		s.mu.Unlock()
		return errors.Join(fmt.Errorf("save the closed session: %w", err), s.unmarkAsleep(entry))
	}
	recordErr := s.recordSaved(entry, true)
	s.endSleep(entry, true)
	return recordErr
}

// saveRecoveredAsleep finishes the save of a VM a server restart caught
// falling asleep. One that cannot be saved stays running, for the next wake
// to take up. Caller holds entry.lifecycleMu.
func (s *Scheduler) saveRecoveredAsleep(ctx context.Context, entry *supervised, saver runtime.SaveRuntime) error {
	s.mu.Lock()
	cid := entry.containerID
	s.mu.Unlock()
	if err := saver.Save(ctx, cid); err != nil {
		return fmt.Errorf("save the session the restart caught falling asleep: %w", err)
	}
	return s.recordSaved(entry, true)
}

// wakeBeforeEnd wakes a sleeping session VM before a kill or close takes its
// lifecycle lock, so its processes end, and its work is committed, in a
// running VM as in any running session. A wake that fails leaves the run to
// endAsleepVM, so the kill or close goes ahead.
func (s *Scheduler) wakeBeforeEnd(ctx context.Context, entry *supervised, actor domain.MemberID) {
	s.mu.Lock()
	asleep := !entry.asleepSince.IsZero() && !entry.retained && !entry.status.Terminal()
	cid := entry.containerID
	s.mu.Unlock()
	if _, saving := s.saver(cid); !saving || !asleep {
		return
	}
	if err := s.WakeRun(ctx, entry.runID, actor); err != nil {
		slog.Warn("scheduler: wake a sleeping session to end it; ending it asleep", "run", entry.runID, "error", err)
	}
}

// endAsleepVM ends a session VM a kill or close found still asleep: it
// discards the saved state, which ends every process the save kept, and
// records the terminals ended, so neither needs the VM running. Caller
// holds entry.lifecycleMu.
func (s *Scheduler) endAsleepVM(ctx context.Context, entry *supervised) error {
	s.mu.Lock()
	asleep := !entry.asleepSince.IsZero() && !entry.retained
	cid := entry.containerID
	s.mu.Unlock()
	if _, saving := s.saver(cid); !saving || !asleep {
		return nil
	}
	if err := s.cfg.Runtime.Stop(ctx, cid, s.cfg.StopGrace); err != nil && !errors.Is(err, runtime.ErrNotFound) {
		return fmt.Errorf("discard the saved session: %w", err)
	}
	return errors.Join(s.recordSaved(entry, false), s.MarkDevelopmentContainerEnded(ctx, entry.runID))
}
