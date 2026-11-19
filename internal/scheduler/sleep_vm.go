package scheduler

import (
	"cmp"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/coordtransport"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
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
	// closed is a closed session's VM or container, which a Relaunch
	// started.
	closed bool
	// newConversation is a Standard agent started from the VM's disks that
	// could not reopen its conversation.
	newConversation bool
	// unproven is an agent started from the disks that is not known to
	// reopen its conversation: an Enhanced one that may not restore it in a
	// new process, or a Standard one whose conversation may be gone.
	unproven bool
}

func (w woke) notice() string {
	kept := "the conversation, files and checkout are kept."
	switch {
	case w.newConversation:
		kept = "the files and checkout are kept. The agent could not reopen its conversation, so it starts a new one; the earlier one stays in the agent terminal's history."
	case w.unproven:
		kept = "the files and checkout are kept, and the agent restores its conversation if it can."
	}
	switch {
	case w.restored:
		return "Cloudfield had saved this session to disk and restored it. Its processes, terminals and conversation carry on where they were; connections to outside the session may have dropped while it slept."
	case w.restoreErr != nil:
		return "Cloudfield could not restore this session's saved state, so it started the session again from its disk. Processes and terminals that were running were stopped; " + kept + " The restore failed: " + w.restoreErr.Error()
	case w.closed:
		return "Cloudfield started this closed session again from its disk. Processes and terminals that were running when it closed are gone; " + kept
	}
	return "Cloudfield had stopped this idle session's container. Processes and terminals that were running were stopped; " + kept
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
	// The agent may have started a turn of its own since the sweep looked;
	// once detached, nothing here would see it.
	if s.agentWorking(entry) {
		return errors.Join(errStayAwake, s.unmarkAsleep(entry))
	}
	return s.saveMarked(ctx, entry, saver, false)
}

// agentWorking reports whether entry's agent works, as a save of its VM
// sees it.
func (s *Scheduler) agentWorking(entry *supervised) bool {
	s.mu.Lock()
	enhanced := entry.acp && entry.launchMode == domain.LaunchACP
	facts := s.liveFactsLocked(entry)
	s.mu.Unlock()
	return s.sessionFacts(entry.runID, enhanced, true, facts).busy != ""
}

// saveMarked is saveLive for a run whatever its agent is doing. A turn in
// flight carries on after the restore, as across a server restart. The
// guest of a frozen VM answers no detach, so its keeper is let go of as a
// server restart lets go of it, before the save ends its connection: the
// save goes ahead meanwhile and ends the wait. A VM that died instead of
// being saved is not left asleep: its run ends as that of any VM that died.
func (s *Scheduler) saveMarked(ctx context.Context, entry *supervised, saver runtime.SaveRuntime, frozen bool) error {
	s.mu.Lock()
	cid := entry.containerID
	// From here exec and terminal recovery treat the VM as frozen.
	entry.paused = true
	s.mu.Unlock()
	// A shutdown must not cut a save short: it would leave the VM half
	// detached.
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), wakeTimeout)
	defer cancel()
	var detachErr, saveErr error
	if frozen {
		agent := s.acp.letGo(entry.runID)
		detachCtx, saved := context.WithCancel(saveCtx)
		detached := make(chan struct{})
		go func() {
			defer close(detached)
			_ = s.detachForSave(detachCtx, entry)
		}()
		saveErr = saver.Save(saveCtx, cid)
		if saveErr != nil {
			// The detach, and the reattach below, need the guest to answer.
			s.endFrozenSave(entry, cid)
		}
		saved()
		<-detached
		if agent != nil {
			agent.release()
		}
	} else {
		detachErr = s.detachForSave(saveCtx, entry)
		saveErr = saver.Save(saveCtx, cid)
	}
	if saveErr == nil {
		recordErr := s.recordSaved(entry, true)
		s.endSleep(entry, true)
		return errors.Join(detachErr, recordErr)
	}
	saveErr = errors.Join(fmt.Errorf("save the idle session: %w", saveErr), detachErr)
	s.mu.Lock()
	entry.saveFailedAt = time.Now().UTC()
	entry.paused = false
	s.mu.Unlock()
	if info, err := s.cfg.Runtime.Inspect(context.WithoutCancel(ctx), cid); errors.Is(err, runtime.ErrNotFound) || err == nil && info.Exited {
		return errors.Join(saveErr, s.unmarkAsleep(entry))
	}
	if ctx.Err() != nil {
		s.endSleep(entry, true)
		return saveErr
	}
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

// restoreAsleep restores entry's saved VM under a fresh wait. A VM with no
// saved state, as a close, disk reclaim or a VM that stopped before its save
// leaves it, starts from its disks. So does one whose saved state cannot be
// restored, unless coldStartRefusal keeps that state: the wake then fails
// with the restore's error. Caller holds entry.lifecycleMu and has reserved
// capacity.
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
	// One whose state cannot be read is not started over, which would end
	// any process it runs.
	inspectCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	info, inspectErr := s.cfg.Runtime.Inspect(inspectCtx, cid)
	cancel()
	if inspectErr != nil {
		return woke{}, errors.Join(restoreErr, fmt.Errorf("inspect the session's VM: %w", inspectErr))
	}
	if !info.Exited {
		_ = s.cfg.Runtime.Resume(ctx, cid)
		s.superviseWoken(entry)
		return woke{restored: true}, nil
	}
	// A shutdown cut the restore short before the guest ran: its saved state
	// stays for the next server's wake.
	if s.superCtx.Err() != nil {
		return woke{}, restoreErr
	}
	how := woke{closed: closed}
	if !errors.Is(restoreErr, runtime.ErrNotSaved) {
		reason, err := s.coldStartRefusal(ctx, entry)
		if err != nil {
			return woke{}, fmt.Errorf("restore the saved session: %w; and whether it can start again from its disk is unknown: %w", restoreErr, err)
		}
		if reason != "" {
			return woke{}, fmt.Errorf("restore the saved session: %w; %s", restoreErr, reason)
		}
	}
	// A closed session has no saved state by design; an open one without it
	// is told why its processes are gone.
	if !closed || !errors.Is(restoreErr, runtime.ErrNotSaved) {
		slog.Warn("scheduler: restore a sleeping session; starting it from its disk", "run", entry.runID, "error", restoreErr)
		how.restoreErr = restoreErr
	}
	// Stop discards the saved state and a half-restored VM alike.
	if err := s.cfg.Runtime.Stop(ctx, cid, s.cfg.StopGrace); err != nil {
		return woke{}, errors.Join(how.restoreErr, fmt.Errorf("discard the saved state: %w", err))
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
	if err := s.setBootCommand(ctx, entry, saver, &how); err != nil {
		return woke{}, errors.Join(how.restoreErr, fmt.Errorf("set what the session starts from its disk: %w", err))
	}
	if err := s.cfg.Runtime.Start(ctx, cid); err != nil {
		return woke{}, errors.Join(how.restoreErr, err)
	}
	if how.restoreErr != nil {
		// The error carries the VM's log tail on the lines after its first.
		note, _, _ := strings.Cut(how.notice(), "\n")
		s.publishTimeline(context.WithoutCancel(ctx), entry.workspaceID, entry.runID, "", events.TimelineNote, note)
	}
	s.mu.Lock()
	// The agent and its keeper did not survive, so the open restores the
	// conversation rather than trying to reattach.
	entry.agentExec, entry.agentKeeper = nil, nil
	s.mu.Unlock()
	s.superviseWoken(entry)
	return how, nil
}

// setBootCommand replaces the command entry's VM runs when it next starts
// from its disk, which was its launch command with the task: a bare shell
// for an Enhanced session, whose agent the server starts, and for a Standard
// one the agent's terminal on the conversation it reported, or on a new one
// when it reported none, has no command that reopens one, or that command
// fails as it starts, as it does when the conversation is gone. It notes in
// how what becomes of the conversation.
func (s *Scheduler) setBootCommand(ctx context.Context, entry *supervised, saver runtime.SaveRuntime, how *woke) error {
	s.mu.Lock()
	cid, mode, session := entry.containerID, entry.launchMode, entry.agentSessionID
	s.mu.Unlock()
	run, err := s.cfg.Store.GetRun(ctx, entry.runID)
	if err != nil {
		return err
	}
	if mode != domain.LaunchTUI {
		how.unproven = !s.resumes(run.Harness)
		return saver.SetCommand(ctx, cid, wrapTUICommand(nil))
	}
	argv, profile, _, err := s.command(ctx, run.WorkspaceID, run.MemberID, run.AccountMember(), run.Harness, domain.LaunchTUI, "", run.Model, run.Effort)
	if err != nil {
		return err
	}
	c := s.coordinationSeam()
	files := map[string][]byte{}
	script := func(nonce string, argv []string) (out []byte, scriptErr error) {
		// As a launch without coordination, which adds nothing to the command.
		if c == nil {
			return nextCommand(nonce, argv, nil), nil
		}
		sw := modeSwitch{s: s, c: c, entry: entry, run: run, profile: profile, cid: cid}
		var launch map[string][]byte
		out, launch, _, scriptErr = sw.terminalScript(ctx, nonce, argv, run.Model)
		maps.Copy(files, launch)
		return out, scriptErr
	}
	wrap := wrapTUICommand
	session = cmp.Or(session, run.HarnessSessionID)
	how.newConversation = len(profile.ResumeArgs) == 0 || !domain.ValidAgentSessionID(session)
	if !how.newConversation {
		how.unproven = true
		if c != nil {
			var fresh []byte
			if fresh, err = script(rand.Text(), argv); err != nil {
				return err
			}
			files[coordtransport.NextCommandName], wrap = fresh, wrapResumeCommand
		}
		argv = profile.ResumeCommand(session, run.Model, run.Effort)
	}
	boot, err := script("", argv)
	if err != nil {
		return err
	}
	if c != nil {
		if err = c.svc.WriteFiles(entry.runID, files); err != nil {
			return fmt.Errorf("write the agent's launch files: %w", err)
		}
	}
	return saver.SetCommand(ctx, cid, wrap([]string{"/bin/sh", "-c", string(boot)}))
}

// coldStartRefusal is why an open session keeps the saved memory a restore
// could not load instead of starting again from its disk, empty when it
// starts: only an Enhanced session of an agent that restores its
// conversation in a new process does. Any other would come back with a new
// conversation while its saved memory may restore on a later try. A closed
// session starts from its disk whatever its agent. It fails when it cannot
// tell.
func (s *Scheduler) coldStartRefusal(ctx context.Context, entry *supervised) (string, error) {
	s.mu.Lock()
	enhanced, closed := entry.acp && entry.launchMode == domain.LaunchACP, entry.retained
	s.mu.Unlock()
	if closed {
		return "", nil
	}
	const keep = "its saved state and disk are kept: try waking it again, or close it and relaunch it, which starts it from its disk"
	if !enhanced {
		return "a Standard session may not find its conversation again in a new process; " + keep, nil
	}
	run, err := s.cfg.Store.GetRun(ctx, entry.runID)
	if err != nil {
		return "", fmt.Errorf("load the session: %w", err)
	}
	if !s.resumes(run.Harness) {
		return fmt.Sprintf("%s cannot restore its conversation in a new process; %s", run.Harness, keep), nil
	}
	return "", nil
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

// resaveAsleep saves an open session's VM again after a failed wake
// reattached part of it. A VM that cannot be saved stays up, for the next
// wake to take up. Caller holds entry.lifecycleMu.
func (s *Scheduler) resaveAsleep(ctx context.Context, entry *supervised, saver runtime.SaveRuntime) error {
	s.mu.Lock()
	cid := entry.containerID
	entry.paused = true
	s.mu.Unlock()
	defer s.markSaving(entry)()
	detachErr := s.detachForSave(ctx, entry)
	if err := saver.Save(ctx, cid); err != nil {
		return errors.Join(fmt.Errorf("save the session again: %w", err), detachErr)
	}
	return errors.Join(detachErr, s.recordSaved(entry, true))
}

// saveRecoveredAsleep finishes the save of the VM of a run recorded asleep
// that still runs, which this server holds nothing in: one a server restart
// caught falling asleep or left frozen, or whose save failed. One that
// cannot be saved stays running, for the next wake to take up. Caller holds
// entry.lifecycleMu.
func (s *Scheduler) saveRecoveredAsleep(ctx context.Context, entry *supervised, saver runtime.SaveRuntime) error {
	s.mu.Lock()
	cid := entry.containerID
	s.mu.Unlock()
	if err := saver.Save(ctx, cid); err != nil {
		s.mu.Lock()
		entry.saveFailedAt = time.Now().UTC()
		s.mu.Unlock()
		return fmt.Errorf("save the sleeping session: %w", err)
	}
	return s.recordSaved(entry, true)
}

// saveAsleepRunning is saveRecoveredAsleep for the memory guard: the VM is
// frozen first and saved as it stands. Caller holds entry.lifecycleMu.
func (s *Scheduler) saveAsleepRunning(ctx context.Context, entry *supervised) error {
	s.mu.Lock()
	cid := entry.containerID
	asleep := s.runs[entry.runID] == entry && !entry.asleepSince.IsZero() && !entry.asleepSaved && !entry.killRequested && !entry.finalizing && s.guestsNeeded[cid] == 0
	s.mu.Unlock()
	saver, ok := s.saver(cid)
	if !asleep || !ok {
		return nil
	}
	defer s.endFrozenSave(entry, cid)
	s.freezeToSave(entry, cid)
	return s.saveRecoveredAsleep(ctx, entry, saver)
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
