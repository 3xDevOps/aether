package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/3xDevOps/Aether/internal/agentstatus"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/harness"
	"github.com/3xDevOps/Aether/internal/ptyhost"
	"github.com/3xDevOps/Aether/internal/runtime"
)

// waitRetryInitial and waitRetryMax bound the delay between inconclusive
// Runtime.Wait calls. A daemon transport error is not evidence that the
// container exited, but retrying without a delay would busy-loop the daemon.
const (
	waitRetryInitial = 50 * time.Millisecond
	waitRetryMax     = time.Second
)

func waitForExit(ctx context.Context, wait func(context.Context) (runtime.ExitStatus, error)) (runtime.ExitStatus, error) {
	delay := waitRetryInitial
	for {
		status, err := wait(ctx)
		if err == nil || errors.Is(err, runtime.ErrNotFound) || ctx.Err() != nil {
			return status, err
		}
		if delay == waitRetryInitial {
			slog.Warn("scheduler: container wait failed; retrying without stopping container", "error", err)
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return runtime.ExitStatus{}, ctx.Err()
		case <-timer.C:
		}
		if delay < waitRetryMax {
			delay *= 2
			if delay > waitRetryMax {
				delay = waitRetryMax
			}
		}
	}
}

// finalizeTimeout bounds the post-exit work (commit, publish, destroy),
// which runs on a fresh context so shutdown cannot orphan half-finalized
// runs.
const finalizeTimeout = time.Minute

// superviseWait blocks on the container's main process and finalizes the
// run when it exits. Supervision-context cancellation (Close / server
// shutdown) ends supervision without touching the container or the run.
func (s *Scheduler) superviseWait(entry *supervised) {
	defer s.wg.Done()
	st, err := waitForExit(s.superCtx, func(ctx context.Context) (runtime.ExitStatus, error) {
		return s.cfg.Runtime.Wait(ctx, entry.containerID)
	})
	if err != nil {
		if s.superCtx.Err() != nil {
			return
		}
		if errors.Is(err, runtime.ErrNotFound) {
			// A missing container is the one non-success result that proves
			// this run can no longer be supervised.
			slog.Warn("scheduler: container disappeared while waiting", "run", entry.runID, "error", err)
			st = runtime.ExitStatus{Code: -1}
		} else {
			// waitForExit only returns another error when its context was
			// cancelled; keep this guard so a future implementation cannot
			// turn a transport error into a bogus exit.
			slog.Warn("scheduler: container wait inconclusive; retaining supervision", "run", entry.runID, "error", err)
			return
		}
	}
	s.recordExitObserved(entry, st.Code)
	s.finalize(entry, st.Code)
}

// recordExitObserved durably persists the Wait result before finalize so a
// crash between Wait and status/commit resumes the original exit.
func (s *Scheduler) recordExitObserved(entry *supervised, code int) {
	s.mu.Lock()
	entry.exitObserved = true
	entry.exitCode = code
	sc := entry.sidecar()
	live := s.runs[entry.runID] == entry
	s.mu.Unlock()
	if !live {
		return
	}
	if err := s.writeSidecar(sc); err != nil {
		slog.Warn("scheduler: persist exit_observed", "run", entry.runID, "error", err)
	}
}

// finalize implements the pinned exit handling (§6.6): stop the watches,
// commit results ("aether:" on clean exit, "wip:" otherwise), publish the
// run branch, record the completed or final status, destroy the container,
// and drop the sidecar. Checkout and transcript are always preserved.
func (s *Scheduler) finalize(entry *supervised, code int) {
	ctx, cancel := context.WithTimeout(context.Background(), finalizeTimeout)
	defer cancel()

	s.cfg.Git.StopDiffWatch(entry.runID)
	if err := s.cfg.PTY.StopSession(ctx, ptyhost.RunSession(entry.runID)); err != nil {
		slog.Warn("scheduler: stop pty session", "run", entry.runID, "error", err)
	}
	s.cfg.PTY.StopSessionsWithPrefix(ctx, string(ptyhost.RunShellSession(entry.runID, "")))

	s.mu.Lock()
	killed, killActor := entry.killRequested, entry.killActor
	s.mu.Unlock()

	msg := "wip: "
	if code == 0 && !killed {
		msg = "aether: "
	}
	if _, err := s.commitAll(ctx, entry.runID, msg+taskLine(entry.task)); err != nil {
		slog.Warn("scheduler: commit results", "run", entry.runID, "error", err)
	}
	if _, err := s.cfg.Git.PublishRunBranch(ctx, entry.runID); err != nil {
		slog.Warn("scheduler: publish run branch", "run", entry.runID, "error", err)
	}

	var (
		to     domain.RunStatus
		reason string
		actor  domain.MemberID
	)
	switch {
	case killed:
		to, reason, actor = domain.RunAbandoned, "killed", killActor
	case code == 0:
		to, reason = domain.RunCompleted, "agent exited; results committed"
	default:
		to, reason = domain.RunFailed, fmt.Sprintf("agent exited %d", code)
	}
	s.mu.Lock()
	// A Kill accepted after the snapshot above still owns the outcome: the
	// caller was told the kill succeeded.
	if entry.killRequested {
		to, reason, actor = domain.RunAbandoned, "killed", entry.killActor
	}
	err := s.transitionLocked(ctx, entry.runID, entry.workspaceID, entry.status, to, reason, actor)
	s.mu.Unlock()
	// ErrInvalidTransition means the run already reached a terminal state
	// (e.g. CloseRun raced the exit); the cleanup below still applies.
	if err != nil && !errors.Is(err, ErrInvalidTransition) {
		slog.Warn("scheduler: record exit status", "run", entry.runID, "error", err)
	}

	if err := s.cfg.Runtime.Destroy(ctx, entry.containerID); err != nil {
		slog.Warn("scheduler: destroy container", "run", entry.runID, "error", err)
	}
	s.removeSidecar(entry.runID)
	if entry.done != nil {
		close(entry.done)
	}
	s.mu.Lock()
	if s.runs[entry.runID] == entry {
		delete(s.runs, entry.runID)
	}
	s.mu.Unlock()

}

// checkStalls implements §6.7: a running, non-paused run with no PTY
// output and no file changes past StallThreshold parks at needs-attention;
// a stalled-but-alive run whose activity refreshes returns to running.
// PTY output is what the agent wrote: a steer's banner, and the terminal's
// echo of anything written to the agent's input, are the server's, so only
// the agent's own answer clears a stall.
//
// Silence is now the hang detector and the fallback for harnesses that
// cannot report (internal/agentstatus): an agent that says it is waiting
// parks its own run, with a reason that says what for, the moment it stops.
// Two rules follow. Where the harness reports both ends of a turn, activity
// must not un-park it: a TUI that repaints while the member types is
// producing output, not work, and only the agent's own "working" means the
// turn resumed - which is activity in its own right, because a hook writes
// nothing to the terminal and touches no files. And un-parking takes
// activity that was actually observed, not a run that merely started
// recently - after a restart nothing has been observed yet, and every run
// parked for its member would otherwise be declared working again on the
// first poll.
func (s *Scheduler) checkStalls(ctx context.Context) {
	s.mu.Lock()
	entries := make([]*supervised, 0, len(s.runs))
	for _, e := range s.runs {
		entries = append(entries, e)
	}
	s.mu.Unlock()

	now := time.Now().UTC()
	for _, e := range entries {
		s.mu.Lock()
		live := s.runs[e.runID] == e
		paused, status, started := e.paused, e.status, e.startedAt
		s.mu.Unlock()
		if !live || paused || (status != domain.RunRunning && status != domain.RunNeedsAttention) {
			continue
		}
		activity, observed := started, false
		if t, ok := s.cfg.PTY.LastOutput(ptyhost.RunSession(e.runID)); ok && t.After(activity) {
			activity, observed = t, true
		}
		if t, ok := s.cfg.Git.LastFileChange(e.runID); ok && t.After(activity) {
			activity, observed = t, true
		}

		s.mu.Lock()
		if s.runs[e.runID] == e && !e.paused {
			// An agent that says it is working leaves no other trace, so
			// the report is read here, under the lock, and counts as the
			// activity it is - including one that lands mid-poll.
			if e.lastWorking.After(activity) {
				activity, observed = e.lastWorking, true
			}
			idle := now.Sub(activity)
			// A run whose harness reports both ends of a turn and has said
			// it is waiting is released by the agent's own next report, not
			// by anything on the terminal.
			heldForTheMember := e.agentReport.State == agentstatus.Waiting &&
				e.reporter == harness.ReporterFull
			released := e.status == domain.RunNeedsAttention && observed &&
				idle <= s.cfg.StallThreshold && !heldForTheMember
			if released && e.agentReport.State == agentstatus.Waiting {
				released = e.unparks(activity)
			}
			var err error
			switch {
			case e.status == domain.RunRunning && idle > s.cfg.StallThreshold:
				err = s.transitionLocked(ctx, e.runID, e.workspaceID, e.status, domain.RunNeedsAttention,
					fmt.Sprintf("stalled: no output or file changes for %s", idle.Truncate(time.Second)), "")
			case released:
				// The run goes back to being judged on silence alone, so
				// the next quiet threshold parks it as a stall again - and
				// a restart must not resurrect the report this clears.
				e.agentReport = agentstatus.Report{}
				e.parkedAt, e.postParkActivity = time.Time{}, time.Time{}
				if serr := s.writeSidecar(e.sidecar()); serr != nil {
					slog.Warn("scheduler: persist cleared agent report", "run", e.runID, "error", serr)
				}
				err = s.transitionLocked(ctx, e.runID, e.workspaceID, e.status, domain.RunRunning,
					"activity resumed", "")
			}
			if err != nil {
				slog.Warn("scheduler: stall transition", "run", e.runID, "error", err)
			}
		}
		s.mu.Unlock()
	}
}

// turnTail is how long after a waiting report the terminal is still taken
// to be painting the turn that ended: the answer it wrote, the prompt
// being restored, a spinner winding down. It is a bound on a TUI's own
// trailing frames, not a measured vendor number, so it is generous.
const turnTail = 3 * time.Second

// unparks reports whether terminal activity on a run the agent parked
// itself is the next turn rather than the tail of the one that ended.
//
// A harness that reports only the end of a turn (harness.ReporterTurnEnd)
// never says the next one started, so activity is the only thing that can
// release its run - but the report fires while the finished turn is still
// being drawn. Counting those frames would hand the run straight back to
// an agent that is waiting, which is the failure this whole mechanism
// exists to fix. So the frames are measured against the clock rather than
// against polls: a poll landing inside the trailing burst would otherwise
// see it advance twice and release the run, whatever --poll-interval is
// set to. Past the tail, activity still has to move on a later poll, so a
// single late frame is not a turn either.
//
// Caller must hold s.mu.
func (e *supervised) unparks(activity time.Time) bool {
	if !activity.After(e.parkedAt.Add(turnTail)) {
		return false
	}
	if e.postParkActivity.IsZero() {
		e.postParkActivity = activity
		return false
	}
	return activity.After(e.postParkActivity)
}

// sweepCheckouts applies the checkout TTL (§6.8): terminal runs whose
// checkout outlived CheckoutTTL lose the checkout directory - the branch
// and transcript are the artifacts and are never GC'd.
func (s *Scheduler) sweepCheckouts(ctx context.Context) {
	workspaces, err := s.cfg.Store.ListWorkspaces(ctx)
	if err != nil {
		slog.Warn("scheduler: checkout gc: list workspaces", "error", err)
		return
	}
	// Checkouts are per-run-ID (relaunch clones a new tree from the old
	// published branch), but skip reclaiming a path an active run still
	// names in case of a leftover shared tree.
	active, err := s.cfg.Store.ListActiveRuns(ctx)
	if err != nil {
		slog.Warn("scheduler: checkout gc: list active runs", "error", err)
		return
	}
	inUse := make(map[string]bool, len(active))
	for _, a := range active {
		if a.Worktree != "" {
			inUse[a.Worktree] = true
		}
	}
	cutoff := time.Now().UTC().Add(-s.cfg.CheckoutTTL)
	for _, ws := range workspaces {
		runs, err := s.cfg.Store.ListRunsByWorkspace(ctx, ws.ID)
		if err != nil {
			slog.Warn("scheduler: checkout gc: list runs", "workspace", ws.ID, "error", err)
			continue
		}
		for _, r := range runs {
			if !r.Status.Terminal() || r.Worktree == "" || r.FinishedAt == nil || r.FinishedAt.After(cutoff) {
				continue
			}
			if inUse[r.Worktree] {
				continue
			}
			if err := s.cfg.Git.RemoveRunCheckout(ctx, r.ID); err != nil {
				slog.Warn("scheduler: checkout gc: remove checkout", "run", r.ID, "error", err)
				continue
			}
			// Re-read before the full-row write: UpdateRun replaces every
			// mutable column, and the row listed above may be stale (e.g. a
			// concurrent handoff changed member_id).
			fresh, err := s.cfg.Store.GetRun(ctx, r.ID)
			if err != nil {
				slog.Warn("scheduler: checkout gc: reread run", "run", r.ID, "error", err)
				continue
			}
			fresh.Worktree = ""
			if err := s.cfg.Store.UpdateRun(ctx, fresh); err != nil {
				slog.Warn("scheduler: checkout gc: clear worktree", "run", r.ID, "error", err)
				continue
			}
			s.removeSidecar(r.ID)
		}
	}
}
