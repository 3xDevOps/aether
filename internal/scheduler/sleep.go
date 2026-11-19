package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/3xDevOps/Aether/internal/coordtransport"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/harness"
	"github.com/3xDevOps/Aether/internal/ptyhost"
	"github.com/3xDevOps/Aether/internal/runtime"
)

// DefaultRunSleepAfter is aether-server's --run-sleep-after default, and
// DefaultVMSleepAfter its default for session VMs.
const (
	DefaultRunSleepAfter = 30 * time.Minute
	DefaultVMSleepAfter  = 5 * time.Minute
)

// wakeTimeout bounds one wake: the container start, its setup gate and the
// agent's restore. The wake of a saved VM also gets the time to read its
// saved memory back at restoreRate, the slowest read the VM runtime allows
// a restore. A wake runs for every caller waiting on it, so no caller's
// context cancels it; a server shutdown does.
const (
	wakeTimeout = 3 * time.Minute
	restoreRate = 20 << 20
)

// ErrClosedAsleep refuses waking a closed session's stopped container
// outside Relaunch.
var ErrClosedAsleep = fmt.Errorf("%w: the session is closed and its container is asleep; reopen it", ErrInvalidTransition)

// SleepState is a live run's idle sleep: when its container was stopped or
// its VM saved, whether the saved VM keeps every process, whether a wake is
// starting it, and the last wake's failure.
type SleepState struct {
	AsleepSince *time.Time
	Saved       bool
	Waking      bool
	WakeError   string
}

// Sleep reports run's sleep state; a run that is awake, or still falling
// asleep, has none.
func (s *Scheduler) Sleep(run domain.RunID) SleepState {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.runs[run]
	if entry == nil || entry.asleepSince.IsZero() || entry.sleeping {
		return SleepState{}
	}
	since := entry.asleepSince
	state := SleepState{AsleepSince: &since, Saved: entry.asleepSaved, Waking: entry.waking != nil}
	if entry.wakeErr != nil {
		state.WakeError = entry.wakeErr.Error()
	}
	return state
}

// NoteTyping records a member typing in run's composer, which keeps it awake.
func (s *Scheduler) NoteTyping(run domain.RunID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if entry := s.runs[run]; entry != nil {
		entry.typedAt = time.Now().UTC()
	}
}

// WakeRun starts run's container again if it is asleep, and returns once
// the agent has its conversation back or the wake failed.
func (s *Scheduler) WakeRun(ctx context.Context, run domain.RunID, _ domain.MemberID) error {
	release, err := s.holdAwake(ctx, run)
	if err != nil {
		return err
	}
	release()
	return nil
}

// HoldRunAwake is holdAwake for a connection forwarded to run's container:
// its end counts as the run's last activity.
func (s *Scheduler) HoldRunAwake(ctx context.Context, run domain.RunID) (func(), error) {
	release, err := s.holdAwake(ctx, run)
	if err != nil {
		return nil, err
	}
	return sync.OnceFunc(func() {
		release()
		s.mu.Lock()
		if entry := s.runs[run]; entry != nil {
			entry.execAt = time.Now().UTC()
		}
		s.mu.Unlock()
	}), nil
}

// SettleSleep waits for a sleep of run that is under way to finish, for a
// caller that must then act on the run inside control admission, which a
// wait must not hold.
func (s *Scheduler) SettleSleep(ctx context.Context, run domain.RunID) error {
	s.mu.Lock()
	entry := s.runs[run]
	var done chan struct{}
	if entry != nil && entry.sleeping {
		done = entry.sleepDone
	}
	s.mu.Unlock()
	if done == nil {
		return nil
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("wait for the session to finish falling asleep: %w", ctx.Err())
	}
}

var (
	errAsleep        = fmt.Errorf("%w: the session is asleep; wake it first", ErrInvalidTransition)
	errFallingAsleep = fmt.Errorf("%w: the session is falling asleep; try again", ErrInvalidTransition)
)

// holdAwakeNow is holdAwake for a caller inside control admission: it never
// waits, and refuses a run that is asleep or falling asleep.
func (s *Scheduler) holdAwakeNow(run domain.RunID) (func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.runs[run]
	switch {
	case entry == nil:
		return func() {}, nil
	case entry.sleeping:
		return nil, errFallingAsleep
	case !entry.asleepSince.IsZero():
		return nil, errAsleep
	}
	entry.awake++
	return func() {
		s.mu.Lock()
		entry.awake--
		s.mu.Unlock()
	}, nil
}

// holdAwake wakes run if it is asleep and keeps it from falling asleep
// until release. A run with no live entry is left to the caller's own
// lookup to refuse.
func (s *Scheduler) holdAwake(ctx context.Context, run domain.RunID) (release func(), err error) {
	for {
		s.mu.Lock()
		entry := s.runs[run]
		if entry == nil {
			s.mu.Unlock()
			return func() {}, nil
		}
		if !entry.sleeping && entry.asleepSince.IsZero() {
			entry.awake++
			s.mu.Unlock()
			return func() {
				s.mu.Lock()
				entry.awake--
				s.mu.Unlock()
			}, nil
		}
		s.mu.Unlock()
		if err := s.wake(ctx, entry); err != nil {
			return nil, err
		}
	}
}

// wake starts entry's stopped container, once however many callers ask at
// the same time: the first runs the wake and the others wait for its result.
func (s *Scheduler) wake(ctx context.Context, entry *supervised) error {
	for {
		s.mu.Lock()
		switch {
		case s.runs[entry.runID] != entry:
			s.mu.Unlock()
			return fmt.Errorf("%w: the session ended while it was asleep", ErrNoLiveEnvironment)
		case entry.sleeping:
			done := entry.sleepDone
			s.mu.Unlock()
			select {
			case <-done:
			case <-ctx.Done():
				return fmt.Errorf("wait for the session to finish falling asleep: %w", ctx.Err())
			}
			continue
		case entry.asleepSince.IsZero():
			s.mu.Unlock()
			return nil
		case entry.waking != nil:
			done := entry.waking
			s.mu.Unlock()
			select {
			case <-done:
			case <-ctx.Done():
				return fmt.Errorf("wait for the session to wake: %w", ctx.Err())
			}
			s.mu.Lock()
			failed := entry.wakeErr
			asleep := !entry.asleepSince.IsZero()
			s.mu.Unlock()
			if asleep && failed != nil {
				return failed
			}
			continue
		case entry.retained:
			s.mu.Unlock()
			return ErrClosedAsleep
		}
		done := make(chan struct{})
		entry.waking = done
		cid := entry.containerID
		s.mu.Unlock()
		s.publishSleep(entry)

		limit := wakeTimeout
		if saver, ok := s.saver(cid); ok {
			saved, _ := saver.SavedMemory(cid)
			limit += time.Duration(saved/restoreRate) * time.Second
		}
		wakeCtx, cancel := context.WithTimeout(s.superCtx, limit)
		how, err := s.wakeLive(wakeCtx, entry)
		cancel()
		if err != nil {
			err = fmt.Errorf("wake the session: %w", err)
		}
		s.mu.Lock()
		entry.waking = nil
		entry.wakeErr = err
		// A wake a shutdown cut short has not failed: the next server wakes it.
		if s.superCtx.Err() != nil {
			entry.wakeErr = nil
		}
		if err != nil && s.runs[entry.runID] == entry && !entry.finalizing {
			if werr := s.writeSidecar(entry.sidecar()); werr != nil {
				err = errors.Join(err, fmt.Errorf("record the failed wake: %w", werr))
			}
		}
		close(done)
		s.mu.Unlock()
		s.publishSleep(entry)
		if err != nil {
			slog.Warn("scheduler: wake a sleeping session", "run", entry.runID, "error", err)
			return err
		}
		s.noteWake(ctx, entry.runID, how)
		return nil
	}
}

// noteWake tells the session's terminal and conversation how it woke.
func (s *Scheduler) noteWake(ctx context.Context, run domain.RunID, how woke) {
	text := how.notice()
	if err := s.cfg.PTY.Annotate(context.WithoutCancel(ctx), ptyhost.RunSession(run), "Cloudfield", "", text); err != nil {
		slog.Warn("scheduler: note the wake in the terminal", "run", run, "error", err)
	}
	if sess := s.acp.session(run); sess != nil {
		severity, title := "info", "Woke from sleep"
		if how.restoreErr != nil {
			severity = "warning"
		}
		if how.closed {
			title = "Relaunched"
		}
		if err := sess.Notice(severity, title, text); err != nil {
			slog.Warn("scheduler: note the wake in the session", "run", run, "error", err)
		}
	}
}

// wakeLive starts or restores an open session's container and takes up its
// agent again, as Relaunch reopens a closed one. A failure puts the run back
// to sleep, with nothing half started.
func (s *Scheduler) wakeLive(ctx context.Context, entry *supervised) (woke, error) {
	entry.lifecycleMu.Lock()
	defer entry.lifecycleMu.Unlock()
	s.mu.Lock()
	if s.runs[entry.runID] != entry || entry.asleepSince.IsZero() || entry.finalizing || entry.killRequested || entry.retained || entry.destroyPending {
		s.mu.Unlock()
		return woke{}, fmt.Errorf("%w: the session is ending", ErrNoLiveEnvironment)
	}
	cid := entry.containerID
	entry.admitting = true
	s.mu.Unlock()

	release, err := s.reserveWake(ctx, cid)
	if err == nil {
		defer release()
		defer s.needGuest(cid)()
	}
	s.mu.Lock()
	entry.admitting = false
	s.mu.Unlock()
	if err != nil {
		return woke{}, err
	}
	how, err := s.startAsleep(ctx, entry)
	if err != nil {
		if errors.Is(err, runtime.ErrNotFound) {
			// As for any container that disappears under a session.
			s.finalizeAsleep(entry, -1)
			return woke{}, fmt.Errorf("the session's container was removed while it slept, so the session ends; its checkout and branch are kept: %w", err)
		}
		return woke{}, err
	}
	up, stop := s.untilExit(ctx, cid)
	err = s.reattach(up, entry, how.restored)
	exited := context.Cause(up)
	stop()
	if err != nil {
		// The exit ended the reattach, not the caller: its error must not
		// read as a canceled context.
		if exited != nil && ctx.Err() == nil {
			err = fmt.Errorf("%w: %v", exited, err)
		}
		if how.restoreErr != nil {
			// The restore's error carries the VM's log tail on the lines after its first.
			restore, _, _ := strings.Cut(how.restoreErr.Error(), "\n")
			err = fmt.Errorf("%w; the session had started again from its disk without its processes, as its restore failed: %s", err, restore)
		}
		return woke{}, errors.Join(err, s.stopAsleep(context.WithoutCancel(ctx), entry))
	}
	s.mu.Lock()
	entry.asleepSince = time.Time{}
	entry.quietFrom = time.Now().UTC()
	memorySaved := entry.memorySaved
	entry.memorySaved = 0
	err = s.writeSidecar(entry.sidecar())
	if err != nil {
		entry.asleepSince, entry.memorySaved = entry.quietFrom, memorySaved
	}
	s.mu.Unlock()
	if err != nil {
		return woke{}, errors.Join(fmt.Errorf("record the wake: %w", err), s.stopAsleep(context.WithoutCancel(ctx), entry))
	}
	return how, nil
}

// untilExit is ctx ending once cid's container exits, with the exit as its
// cause: nothing a wake still waits on in that container will answer.
func (s *Scheduler) untilExit(ctx context.Context, cid runtime.ID) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancelCause(ctx)
	go func() {
		if st, err := s.cfg.Runtime.Wait(ctx, cid); err == nil {
			cancel(fmt.Errorf("the session's container exited with status %d while the session woke", st.Code))
		}
	}()
	return ctx, func() { cancel(nil) }
}

// startAsleep starts entry's stopped container, or restores its saved VM,
// under a fresh wait. Caller holds entry.lifecycleMu and has reserved
// capacity.
func (s *Scheduler) startAsleep(ctx context.Context, entry *supervised) (woke, error) {
	s.mu.Lock()
	cid, closed := entry.containerID, entry.retained
	s.mu.Unlock()
	if saver, ok := s.saver(cid); ok {
		return s.restoreAsleep(ctx, entry, saver)
	}
	how := woke{closed: closed}
	if closed {
		run, err := s.cfg.Store.GetRun(ctx, entry.runID)
		if err != nil {
			return woke{}, err
		}
		how.unproven = !s.resumes(run.Harness)
	}
	info, err := s.cfg.Runtime.Inspect(ctx, cid)
	if err != nil {
		return woke{}, err
	}
	// A sleep a crash interrupted left the container frozen or running,
	// which docker start refuses, and its terminals unrecorded.
	if !info.Exited {
		if err = s.stopLeftoverAsleep(ctx, entry); err != nil {
			return woke{}, err
		}
	}
	if err = s.MarkDevelopmentContainerEnded(ctx, entry.runID); err != nil {
		return woke{}, fmt.Errorf("record the terminals the sleep ended: %w", err)
	}
	if err := s.cfg.Runtime.Start(ctx, cid); err != nil {
		return woke{}, err
	}
	s.superviseWoken(entry)
	return how, nil
}

// stopAsleep puts entry back to sleep after a failed wake: it stops the
// container or a closed session's VM, or saves an open session's VM again.
// A server shutting down leaves it as it is, recorded asleep, for the next
// server's recovery to take up. Caller holds entry.lifecycleMu.
func (s *Scheduler) stopAsleep(ctx context.Context, entry *supervised) error {
	if s.superCtx.Err() != nil {
		return nil
	}
	s.mu.Lock()
	cid, closed := entry.containerID, entry.retained
	s.mu.Unlock()
	if saver, ok := s.saver(cid); ok && !closed {
		return s.resaveAsleep(ctx, entry, saver)
	}
	_ = s.entryDriver(entry).Stop(ctx, entry.runID)
	s.cfg.Git.StopDiffWatch(entry.runID)
	s.mu.Lock()
	entry.paused = true
	entry.sleepGen++
	entry.agentExec, entry.agentKeeper = nil, nil
	s.mu.Unlock()
	return s.cfg.Runtime.Stop(ctx, cid, s.cfg.StopGrace)
}

func (s *Scheduler) publishSleep(entry *supervised) {
	state := s.Sleep(entry.runID)
	s.publish(context.Background(), events.Event{
		WorkspaceID: entry.workspaceID,
		RunID:       entry.runID,
		Payload:     events.RunSleepPayload{AsleepSince: state.AsleepSince, AsleepSaved: state.Saved, Waking: state.Waking, WakeError: state.WakeError},
	})
}

// idleFacts are what the sleep sweep knows of one run.
type idleFacts struct {
	// busy names what keeps the run awake however long it has been quiet,
	// empty when nothing does.
	busy string
	// active are the times something last happened in the run.
	active []time.Time
}

// idleSince is when the run last saw activity; ok is false while something
// keeps it awake.
func idleSince(f idleFacts) (time.Time, bool) {
	if f.busy != "" {
		return time.Time{}, false
	}
	var last time.Time
	for _, t := range f.active {
		if t.After(last) {
			last = t
		}
	}
	return last, true
}

// resumes reports whether agent's conversation is known to survive a
// container restart, and no administrator definition replaces the command
// that showed it.
func (s *Scheduler) resumes(agent string) bool {
	if s.cfg.sleepAnyAgent {
		return true
	}
	profile, ok := harness.Lookup(agent)
	_, overridden := s.harnesses[agent]
	return ok && !overridden && profile.Capabilities.Sleep
}

// liveFactsLocked reads an open Enhanced run's state. Caller holds s.mu.
func (s *Scheduler) liveFactsLocked(e *supervised) idleFacts {
	busy := heldAwakeLocked(e)
	switch {
	case busy != "":
	case e.activity == domain.ActivityWorking || e.activity == domain.ActivityWaitingTools:
		busy = "working"
	case e.betweenTurns:
		busy = "background tasks run"
	}
	return idleFacts{busy: busy, active: []time.Time{
		e.startedAt, e.quietFrom, e.lastWorking, e.turnStartedAt, e.typedAt, e.browsedAt, e.execAt,
	}}
}

// heldAwakeLocked names what keeps an open run awake however long it is
// quiet and whatever the host is short of, empty when nothing does: a sleep
// would break a transition, a request or an operation in flight. Caller
// holds s.mu.
func heldAwakeLocked(e *supervised) string {
	switch {
	case e.status != domain.RunRunning && e.status != domain.RunNeedsAttention:
		return "not running"
	case e.paused || e.retained || e.destroyPending || e.finalizing || e.killRequested || e.exitObserved:
		return "paused or ending"
	case e.switching != "" || e.switchIntent != nil || e.moving || e.accountSwap != nil || e.usageLimit != "":
		return "changing mode or account"
	case len(e.pendingInputs) > 0 || len(e.sudo) > 0:
		return "a request waits for an answer"
	case e.awake > 0:
		return "a delivery, exec or browser operation is in flight"
	}
	return ""
}

// sessionFacts adds what the agent session of an Enhanced run, the terminals
// and the checkout recorded to f, every activity time even when it finds the
// run busy. A background shell nothing waits for, such
// as a dev server, keeps only a container awake, since a VM's save keeps it.
// Work the agent waits for keeps a VM awake too, as nothing would wake the
// VM when it ends. So does a wakeup the agent set: its timer counts only time
// the VM runs, so a save would make it fire late.
func (s *Scheduler) sessionFacts(run domain.RunID, enhanced, saving bool, f idleFacts) idleFacts {
	if enhanced {
		if sess := s.acp.session(run); sess == nil {
			f.busy = "the agent session is not open"
		} else {
			st := sess.State()
			switch {
			case st.TurnInFlight || st.Background || !saving && len(st.Tasks) > 0 || st.Queued > 0 || len(st.Pending) > 0 || st.ToolsOpen || st.Compacting:
				f.busy = "the agent is working"
			case saving && st.WakeupPending:
				f.busy = "a wakeup the agent set is pending"
			}
			f.active = append(f.active, st.LastProgress, st.TurnStartedAt, st.OpenedAt)
		}
	}
	keys := append(s.cfg.PTY.ActiveSessions(string(ptyhost.RunShellSession(run, ""))), ptyhost.RunSession(run))
	for _, key := range keys {
		if t, ok := s.cfg.PTY.LastOutput(key); ok {
			f.active = append(f.active, t)
		}
		if t, ok := s.cfg.PTY.LastInput(key); ok {
			f.active = append(f.active, t)
		}
	}
	if t, ok := s.cfg.Git.LastFileChange(run); ok {
		f.active = append(f.active, t)
	}
	return f
}

// sleepIdle puts to sleep every open run quiet for its sleep delay and
// every closed one retained that long, each in its own goroutine: Enhanced
// runs in containers, and Standard and Enhanced runs in session VMs.
func (s *Scheduler) sleepIdle(ctx context.Context) {
	if s.cfg.RunSleepAfter <= 0 && s.cfg.VMSleepAfter <= 0 {
		return
	}
	type candidate struct {
		e        *supervised
		after    time.Duration
		saving   bool
		enhanced bool
	}
	now := time.Now().UTC()
	s.mu.Lock()
	var live, closed []candidate
	for _, e := range s.runs {
		_, saving := s.saver(e.containerID)
		enhanced := e.acp && e.launchMode == domain.LaunchACP
		after := s.cfg.RunSleepAfter
		if saving {
			after = s.cfg.VMSleepAfter
		}
		sleeps := enhanced || saving && e.launchMode == domain.LaunchTUI
		if after <= 0 || !sleeps || e.sleeping || !e.asleepSince.IsZero() || e.containerID == "" || now.Sub(e.saveFailedAt) < after {
			continue
		}
		c := candidate{e: e, after: after, saving: saving, enhanced: enhanced}
		if e.retained {
			if e.paused && !e.destroyPending && !e.evidencePending && e.retainedUntil != nil {
				closed = append(closed, c)
			}
			continue
		}
		if last, ok := idleSince(s.liveFactsLocked(e)); ok && now.Sub(last) >= after {
			live = append(live, c)
		}
	}
	s.mu.Unlock()
	for _, c := range live {
		if !s.mayRunSleep(ctx, c.e.runID, now, c.after, c.saving, false) || !c.saving && !s.launchedEnhanced(ctx, c.e) {
			continue
		}
		s.mu.Lock()
		facts := s.liveFactsLocked(c.e)
		s.mu.Unlock()
		if last, ok := idleSince(s.sessionFacts(c.e.runID, c.enhanced, c.saving, facts)); !ok || now.Sub(last) < c.after {
			continue
		}
		s.goSleep(c.e, s.sleepLive)
	}
	for _, c := range closed {
		if s.mayRunSleep(ctx, c.e.runID, now, c.after, c.saving, true) && (c.saving || s.launchedEnhanced(ctx, c.e)) {
			s.goSleep(c.e, s.sleepClosed)
		}
	}
}

// mayRunSleep checks the run row: the agent, which any VM keeps through a
// save and a closed session's Relaunch starts in a new conversation when it
// cannot restore its own, and for a closed run how long ago it closed.
func (s *Scheduler) mayRunSleep(ctx context.Context, id domain.RunID, now time.Time, after time.Duration, saving, closed bool) bool {
	run, err := s.cfg.Store.GetRun(ctx, id)
	if err != nil {
		return false
	}
	if !saving && !closed && !s.resumes(run.Harness) {
		return false
	}
	return !closed || (run.FinishedAt != nil && now.Sub(*run.FinishedAt) >= after)
}

// launchedEnhanced reports a container created for an Enhanced launch, whose
// main process is a bare shell. One created for Standard and switched to
// Enhanced since would run its launch command, with the task, again on start.
func (s *Scheduler) launchedEnhanced(ctx context.Context, e *supervised) bool {
	s.mu.Lock()
	cid := e.containerID
	s.mu.Unlock()
	info, err := s.cfg.Runtime.Inspect(ctx, cid)
	return err == nil && slices.Contains(info.Env, coordtransport.EnhancedEnv+"=1")
}

// foregroundJobScript prints the command of a process that holds a
// terminal's foreground, such as a dev server, an editor or a REPL started
// in a shell tab. A shell waiting at its prompt holds its own terminal and
// is not one. Fields after the command in /proc/<pid>/stat: state, ppid,
// pgrp, session, tty_nr, tpgid.
const foregroundJobScript = `for stat in /proc/[0-9]*/stat; do
	read -r line 2>/dev/null <"$stat" || continue
	comm=${line#*(}
	comm=${comm%)*}
	set -- ${line##*) }
	[ "$6" -gt 0 ] 2>/dev/null && [ "$3" = "$6" ] || continue
	case $comm in
	bash | sh | dash | zsh | fish | ash | ksh | mksh) ;;
	*) printf '%s\n' "$comm"; exit 0 ;;
	esac
done`

// foregroundJob reports a command running in the foreground of one of the
// container's terminals, which keeps the session awake.
func (s *Scheduler) foregroundJob(ctx context.Context, cid runtime.ID) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	code, stdout, stderr, err := s.cfg.Runtime.Exec(ctx, cid, []string{"/bin/sh", "-c", foregroundJobScript}, "")
	if err == nil && code != 0 {
		err = fmt.Errorf("exit %d: %s", code, strings.TrimSpace(stderr))
	}
	if err != nil {
		return false, fmt.Errorf("look for a terminal's foreground command: %w", err)
	}
	return strings.TrimSpace(stdout) != "", nil
}

// guestCPUScript prints the guest's CPU use over one second in percent of
// one core, so 200 is two busy cores. /proc/stat's first line: cpu user
// nice system idle iowait irq softirq steal; one cpuN line follows per core.
const guestCPUScript = `read -r _ u1 n1 s1 i1 w1 q1 f1 _ </proc/stat
sleep 1
read -r _ u2 n2 s2 i2 w2 q2 f2 _ </proc/stat
cpus=0
while read -r name _; do case $name in cpu[0-9]*) cpus=$((cpus + 1)) ;; esac; done </proc/stat
busy=$(( u2 + n2 + s2 + q2 + f2 - u1 - n1 - s1 - q1 - f1 ))
idle=$(( i2 + w2 - i1 - w1 ))
echo $(( busy * 100 * cpus / (busy + idle + 1) ))`

// guestBusyPercent is the CPU use, in percent of one core, above which a VM
// counts as working: an idle agent, shell or dev server stays well below
// half a core, a build or a test run, even a single-threaded one, above.
const guestBusyPercent = 50

// guestBusy reports whether the VM cid still works on something, such as a
// build or a test run the agent or a terminal left running without output.
func (s *Scheduler) guestBusy(ctx context.Context, cid runtime.ID) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	code, stdout, stderr, err := s.cfg.Runtime.Exec(ctx, cid, []string{"/bin/sh", "-c", guestCPUScript}, "")
	if err == nil && code != 0 {
		err = fmt.Errorf("exit %d: %s", code, strings.TrimSpace(stderr))
	}
	if err != nil {
		return false, fmt.Errorf("read the session's CPU use: %w", err)
	}
	percent, err := strconv.Atoi(strings.TrimSpace(stdout))
	if err != nil {
		return false, fmt.Errorf("read the session's CPU use: %q: %w", stdout, err)
	}
	return percent > guestBusyPercent, nil
}

func (s *Scheduler) goSleep(entry *supervised, sleep func(context.Context, *supervised) error) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ctx, cancel := context.WithTimeout(s.superCtx, wakeTimeout)
		defer cancel()
		if err := sleep(ctx, entry); err != nil && !errors.Is(err, errStayAwake) {
			slog.Warn("scheduler: put an idle session to sleep", "run", entry.runID, "error", err)
		}
	}()
}

var errStayAwake = errors.New("scheduler: the session is no longer idle")

// sleepLive stops an idle open session's agent, terminals and container.
// Once sleeping is set, every delivery, exec or attach that holds the run
// awake waits for the sleep to finish and then wakes it.
func (s *Scheduler) sleepLive(ctx context.Context, entry *supervised) error {
	entry.lifecycleMu.Lock()
	defer entry.lifecycleMu.Unlock()
	run := entry.runID
	s.mu.Lock()
	facts := s.liveFactsLocked(entry)
	if s.runs[run] != entry || entry.sleeping || !entry.asleepSince.IsZero() || facts.busy != "" {
		s.mu.Unlock()
		return errStayAwake
	}
	entry.sleeping = true
	entry.sleepDone = make(chan struct{})
	cid := entry.containerID
	enhanced := entry.acp && entry.launchMode == domain.LaunchACP
	s.mu.Unlock()
	saver, saving := s.saver(cid)
	after := s.cfg.RunSleepAfter
	if saving {
		after = s.cfg.VMSleepAfter
	}
	// Activity since the sweep read the run, an autonomous turn included,
	// keeps it awake.
	if last, ok := idleSince(s.sessionFacts(run, enhanced, saving, facts)); !ok || time.Since(last) < after {
		s.endSleep(entry, false)
		return errStayAwake
	}
	// Work that still runs keeps the session awake: in a container a command
	// in a terminal's foreground, which the stop would end, and in a VM, whose
	// save keeps every process, anything that uses the CPU.
	probe := s.foregroundJob
	if saving {
		probe = s.guestBusy
	}
	if busy, err := probe(ctx, cid); err != nil || busy {
		s.endSleep(entry, false)
		if err != nil {
			slog.Warn("scheduler: keep a session awake whose work could not be read", "run", run, "error", err)
		}
		return errStayAwake
	}
	if err := s.markAsleep(entry); err != nil {
		s.endSleep(entry, false)
		return err
	}
	if saving {
		return s.saveLive(ctx, entry, saver)
	}
	undo := func(cause error) error {
		undoCtx := context.WithoutCancel(ctx)
		return errors.Join(cause, s.reopenDevelopment(undoCtx, run), s.resumeDevelopmentBrowser(undoCtx, run, cid), s.unmarkAsleep(entry))
	}
	if err := s.prepareDevelopmentClose(ctx, run); err != nil {
		return undo(err)
	}
	// The agent may have started a turn of its own while the terminals
	// stopped; the container stop below would end it.
	s.mu.Lock()
	facts = s.liveFactsLocked(entry)
	s.mu.Unlock()
	if s.sessionFacts(run, enhanced, saving, facts).busy != "" {
		return undo(errStayAwake)
	}
	// A companion left paused would come back in the network namespace of
	// the stopped container, where nothing answers.
	if err := s.removeDevelopmentBrowser(ctx, run); err != nil {
		return undo(fmt.Errorf("remove the browser companion: %w", err))
	}
	s.cfg.Git.StopDiffWatch(run)
	// The container stop below ends an agent this could not stop.
	if err := s.entryDriver(entry).Stop(ctx, run); err != nil {
		slog.Warn("scheduler: stop the agent of a session falling asleep", "run", run, "error", err)
	}
	s.cfg.PTY.StopSessionsWithPrefix(ctx, string(ptyhost.RunShellSession(run, "")))
	return s.stopSleeping(ctx, entry)
}

// sleepClosed stops a closed session's frozen container or VM, which keeps
// its disks and no memory. Its agent already stopped at the close.
func (s *Scheduler) sleepClosed(ctx context.Context, entry *supervised) error {
	entry.lifecycleMu.Lock()
	defer entry.lifecycleMu.Unlock()
	return s.sleepClosedLocked(ctx, entry)
}

// sleepClosedLocked is sleepClosed for a caller holding entry.lifecycleMu.
func (s *Scheduler) sleepClosedLocked(ctx context.Context, entry *supervised) error {
	s.mu.Lock()
	if s.runs[entry.runID] != entry || !entry.retained || !entry.paused || entry.destroyPending || entry.evidencePending || !entry.asleepSince.IsZero() {
		s.mu.Unlock()
		return errStayAwake
	}
	entry.sleeping = true
	entry.sleepDone = make(chan struct{})
	s.mu.Unlock()
	if err := s.markAsleep(entry); err != nil {
		s.endSleep(entry, false)
		return err
	}
	if err := s.removeDevelopmentBrowser(ctx, entry.runID); err != nil {
		return errors.Join(fmt.Errorf("remove the browser companion: %w", err), s.unmarkAsleep(entry))
	}
	s.mu.Lock()
	cid := entry.containerID
	s.mu.Unlock()
	// The stop outlives a caller that gives up, which would leave Docker
	// stopping the container unseen. One that fails can have unpaused the
	// sandbox, as Docker's does: one still up is frozen again, an already
	// frozen one refusing that, and not left asleep, so the sleep sweep and
	// the memory guard try again.
	stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.cfg.StopGrace+time.Minute)
	defer cancel()
	if err := s.cfg.Runtime.Stop(stopCtx, cid, s.cfg.StopGrace); err != nil {
		checkCtx, cancelCheck := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		defer cancelCheck()
		if info, inspectErr := s.cfg.Runtime.Inspect(checkCtx, cid); inspectErr != nil || !info.Exited {
			s.mu.Lock()
			entry.saveFailedAt = time.Now().UTC()
			s.mu.Unlock()
			return errors.Join(fmt.Errorf("stop the closed session: %w", err), s.cfg.Runtime.Pause(checkCtx, cid), s.unmarkAsleep(entry))
		}
	}
	s.mu.Lock()
	entry.agentExec, entry.agentKeeper = nil, nil
	s.mu.Unlock()
	s.endSleep(entry, true)
	return nil
}

// markAsleep records the sleep durably before anything stops, so a server
// that restarts from here on recovers the run asleep rather than finishing
// it on the container's exit.
func (s *Scheduler) markAsleep(entry *supervised) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry.asleepSince = time.Now().UTC()
	entry.wakeErr = nil
	entry.asleepSaved = false
	entry.memorySaved = 0
	if err := s.writeSidecar(entry.sidecar()); err != nil {
		entry.asleepSince = time.Time{}
		return fmt.Errorf("record the sleep: %w", err)
	}
	return nil
}

// unmarkAsleep takes back a sleep that stopped nothing.
func (s *Scheduler) unmarkAsleep(entry *supervised) error {
	s.mu.Lock()
	entry.asleepSince = time.Time{}
	entry.memorySaved = 0
	err := s.writeSidecar(entry.sidecar())
	s.mu.Unlock()
	s.endSleep(entry, false)
	if err != nil {
		return fmt.Errorf("take back the sleep: %w", err)
	}
	return nil
}

// stopSleeping stops the container of a run marked asleep. A container the
// stop could not reach stays marked asleep: its agent is gone, and a wake
// starts it the same way.
func (s *Scheduler) stopSleeping(ctx context.Context, entry *supervised) error {
	s.mu.Lock()
	cid := entry.containerID
	entry.sleepGen++
	s.mu.Unlock()
	stopErr := s.cfg.Runtime.Stop(ctx, cid, s.cfg.StopGrace)
	s.mu.Lock()
	entry.agentExec, entry.agentKeeper = nil, nil
	s.mu.Unlock()
	s.endSleep(entry, true)
	if stopErr != nil {
		return fmt.Errorf("stop the sleeping session's container: %w", stopErr)
	}
	return nil
}

// endSleep settles a sleep: asleep once the container stopped, else awake
// as before. markAsleep already recorded the sleep; recovery adopts a
// sleeping run paused.
func (s *Scheduler) endSleep(entry *supervised, asleep bool) {
	s.mu.Lock()
	entry.sleeping = false
	if asleep {
		entry.paused = true
	}
	close(entry.sleepDone)
	s.mu.Unlock()
	if asleep {
		s.publishSleep(entry)
		return
	}
	// A container that really exited while the sleep was under way ended
	// the wait, which left the exit to the sleep.
	s.startSupervision(entry)
}

// recoverAsleep adopts an open run whose sidecar says it is asleep, and
// reports false when its container is gone, which the caller handles as
// for any run.
func (s *Scheduler) recoverAsleep(ctx context.Context, r *domain.Run, sc sidecar) bool {
	info, err := s.cfg.Runtime.Inspect(ctx, runtime.ID(sc.ContainerID))
	if errors.Is(err, runtime.ErrNotFound) {
		return false
	}
	// Installed without waiting on or attaching to the container.
	var entry *supervised
	s.mu.Lock()
	if s.runs[r.ID] == nil && s.pending[r.ID] == nil {
		entry = s.entryFromSidecar(r, sc)
		entry.paused = true
		s.runs[r.ID] = entry
		s.syncRunUserReservationsLocked()
	}
	s.mu.Unlock()
	// Started by hand while the server was down, or a VM whose save the
	// restart interrupted: this process holds none of its streams. A VM the
	// memory guard marked to wake, one it was saving or the brake left
	// frozen, is the guard's to save frozen or wake: a save here would let
	// it run first.
	if entry != nil && err == nil && !info.Exited && sc.MemorySaved == 0 {
		s.stopRecoveredAsleep(ctx, entry)
	}
	return true
}

// stopRecoveredAsleep stops the running container of a run recovery adopted
// asleep, or saves an open session's VM, as it stands if a server left it
// frozen: the brake thaws a VM whose lifecycle lock is held. A wake or
// Relaunch that won the lifecycle lock first and started the container on
// purpose keeps it running.
func (s *Scheduler) stopRecoveredAsleep(ctx context.Context, entry *supervised) {
	s.mu.Lock()
	cid := entry.containerID
	s.mu.Unlock()
	frozen := s.vmFrozen(cid)
	entry.lifecycleMu.Lock()
	defer entry.lifecycleMu.Unlock()
	s.mu.Lock()
	asleep, closed := s.runs[entry.runID] == entry && !entry.asleepSince.IsZero(), entry.retained
	s.mu.Unlock()
	if !asleep {
		return
	}
	if frozen && !closed {
		if err := s.saveAsleepRunning(ctx, entry); err != nil {
			slog.Warn("scheduler: save a sleeping session's VM", "run", entry.runID, "error", err)
		}
		return
	}
	if saver, ok := s.entrySaver(entry); ok && !closed {
		if err := s.saveRecoveredAsleep(ctx, entry, saver); err != nil {
			slog.Warn("scheduler: save a sleeping session's VM", "run", entry.runID, "error", err)
		}
		return
	}
	if err := s.stopLeftoverAsleep(ctx, entry); err != nil {
		slog.Warn("scheduler: stop a sleeping session's container", "run", entry.runID, "error", err)
	}
}

// stopLeftoverAsleep finishes a sleep a crash interrupted: it removes the
// browser companion and stops the container. Caller holds
// entry.lifecycleMu.
func (s *Scheduler) stopLeftoverAsleep(ctx context.Context, entry *supervised) error {
	s.mu.Lock()
	cid := entry.containerID
	entry.sleepGen++
	s.mu.Unlock()
	// A companion left behind holds no agent: the container still stops,
	// and the next Browser call removes the companion or reports why not.
	if err := s.removeDevelopmentBrowser(ctx, entry.runID); err != nil {
		s.mu.Lock()
		entry.browserLeft = true
		s.mu.Unlock()
	}
	return s.cfg.Runtime.Stop(ctx, cid, s.cfg.StopGrace)
}

// finalizeAsleep finishes, with exit code, a run whose container a sleep had
// already stopped, which no Wait is left to see exit. Caller holds
// entry.lifecycleMu, so the finish runs after it returns.
func (s *Scheduler) finalizeAsleep(entry *supervised, code int) {
	s.mu.Lock()
	claimed := s.runs[entry.runID] == entry && !entry.asleepSince.IsZero() && !entry.finalizing
	if claimed {
		entry.finalizing = true
		entry.asleepSince = time.Time{}
		s.wg.Add(1)
	}
	s.mu.Unlock()
	if !claimed {
		return
	}
	s.publishSleep(entry)
	go func() {
		defer s.wg.Done()
		s.recordExitObserved(entry, code)
		s.finalize(entry, code)
	}()
}

// awakeExec keeps its run awake until the exec is seen to end.
type awakeExec struct {
	runtime.ManagedExec
	done func()
}

func (s *Scheduler) keepAwake(run domain.RunID, exec runtime.ManagedExec) runtime.ManagedExec {
	s.mu.Lock()
	entry := s.runs[run]
	if entry == nil {
		s.mu.Unlock()
		return exec
	}
	entry.awake++
	entry.execAt = time.Now().UTC()
	s.mu.Unlock()
	return &awakeExec{ManagedExec: exec, done: sync.OnceFunc(func() {
		s.mu.Lock()
		entry.awake--
		entry.execAt = time.Now().UTC()
		s.mu.Unlock()
	})}
}

func (e *awakeExec) Wait(ctx context.Context) (runtime.ExitStatus, error) {
	st, err := e.ManagedExec.Wait(ctx)
	if err == nil || errors.Is(err, runtime.ErrNotFound) {
		e.done()
	}
	return st, err
}

func (e *awakeExec) Stop(ctx context.Context, grace time.Duration) (runtime.ExitStatus, error) {
	st, err := e.ManagedExec.Stop(ctx, grace)
	if err == nil {
		e.done()
	}
	return st, err
}

func (e *awakeExec) Detach() error {
	e.done()
	return e.ManagedExec.Detach()
}
