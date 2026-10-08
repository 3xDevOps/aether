package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/agentstatus"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/harness"
	"github.com/3xDevOps/Aether/internal/ptyhost"
	"github.com/3xDevOps/Aether/internal/runtime"
	"github.com/3xDevOps/Aether/internal/store"
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

// finalizeTimeout bounds post-exit work, which runs on a fresh context so
// shutdown cannot orphan half-finalized runs.
const finalizeTimeout = time.Minute

// superviseWait finalizes the run when its container exits. Cancelling the
// supervision context ends supervision without touching the container or run.
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
			// Guards against a transport error ever becoming a bogus exit.
			slog.Warn("scheduler: container wait inconclusive; retaining supervision", "run", entry.runID, "error", err)
			return
		}
	}

	// Claim the exit transition while holding the lifecycle mutex, then let it
	// go before any potentially blocking finalization work. Kill deliberately
	// remains able to acquire the mutex and record cancellation while commit or
	// publish is in flight.
	entry.lifecycleMu.Lock()
	s.mu.Lock()
	live, retained := s.runs[entry.runID] == entry, entry.retained
	alreadyFinalizing := entry.finalizing
	if live && !retained && !alreadyFinalizing {
		entry.finalizing = true
	}
	s.mu.Unlock()
	if !live || alreadyFinalizing {
		entry.lifecycleMu.Unlock()
		return
	}
	if retained {
		s.mu.Lock()
		keep := entry.missionAssigned && !entry.destroyPending && s.cfg.RunContainerTTL >= 0 &&
			entry.retainedUntil != nil && time.Now().UTC().Before(*entry.retainedUntil) && err == nil
		if keep {
			entry.exitObserved = true
			entry.exitCode = st.Code
			entry.paused = false
			if persistErr := s.persistRetainedSidecar(entry.sidecar()); persistErr != nil {
				entry.evidencePending = true
				s.recordCleanupErrorLocked(entry, cleanupStateError)
				slog.Warn("scheduler: persist retained worker exit", "run", entry.runID, "error", persistErr)
			}
		}
		s.mu.Unlock()
		if keep {
			entry.lifecycleMu.Unlock()
			return
		}
		if err := s.expireRetainedLocked(context.Background(), entry); err != nil {
			slog.Warn("scheduler: expire retained container after exit", "run", entry.runID, "error", err)
		}
		entry.lifecycleMu.Unlock()
		return
	}
	entry.lifecycleMu.Unlock()

	s.recordExitObserved(entry, st.Code)
	s.finalize(entry, st.Code)
}

// recordExitObserved durably persists the Wait result before finalize so a
// crash between Wait and status/commit resumes the original exit.
func (s *Scheduler) recordExitObserved(entry *supervised, code int) {
	s.mu.Lock()
	entry.exitObserved = true
	entry.exitCode = code
	live := s.runs[entry.runID] == entry
	s.mu.Unlock()
	if !live {
		return
	}
	// Re-snapshot under the scheduler lock immediately before writing. Kill
	// may have set killRequested after the first snapshot; writing that stale
	// snapshot would erase the durable cancellation request.
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.runs[entry.runID] != entry {
		return
	}
	if err := s.writeSidecar(entry.sidecar()); err != nil {
		slog.Warn("scheduler: persist exit_observed", "run", entry.runID, "error", err)
	}
}

// finalize commits "aether:" on a clean exit or reported success and "wip:"
// otherwise. The caller has already released entry.lifecycleMu; the
// finalizing flag keeps other destructive operations from racing this work.
func (s *Scheduler) finalize(entry *supervised, code int) {
	ctx, cancel := context.WithTimeout(context.Background(), finalizeTimeout)
	defer cancel()

	s.cfg.Git.StopDiffWatch(entry.runID)

	s.mu.Lock()
	killed, killActor := entry.killRequested, entry.killActor
	// The agent's terminal report outranks its exit code. A run a human
	// already closed keeps that close.
	reported := entry.reported
	if entry.status.Terminal() && entry.status != reported {
		reported = ""
	}
	s.mu.Unlock()

	var detail string
	if !killed && reported == "" && code != 0 {
		detail = s.exitDetail(ctx, entry)
	}

	msg := "wip: "
	if !killed && (reported == domain.RunCompleted || (reported == "" && code == 0)) {
		msg = "aether: "
	}
	committed, commitErr := s.commitAll(ctx, entry.runID, msg+taskLine(entry.task))
	if commitErr != nil {
		slog.Warn("scheduler: commit results", "run", entry.runID, "error", commitErr)
	}
	published, publishErr := s.cfg.Git.PublishRunBranch(ctx, entry.runID)
	if publishErr != nil {
		slog.Warn("scheduler: publish run branch", "run", entry.runID, "error", publishErr)
	}

	// Serialize the terminal ownership handoff with Kill. Retention keeps
	// this admission through settlement; destructive finalization releases it
	// after the handoff so Close can relabel while runtime cleanup is pending.
	entry.lifecycleMu.Lock()
	var (
		to       domain.RunStatus
		reason   string
		actor    domain.MemberID
		byReport bool
	)
	switch {
	case killed:
		to, reason, actor = domain.RunAbandoned, "killed", killActor
	case reported != "":
		to, reason, byReport = reported, reportedClose(reported).reason, true
	case code == 0:
		to, reason = domain.RunCompleted, exitedCompletedReason
	default:
		to, reason = domain.RunFailed, fmt.Sprintf(exitedFailedReasonPrefix+"%d", code)
		if detail != "" {
			reason += ": " + detail
		}
	}
	s.mu.Lock()
	// A Kill accepted after the snapshot above still owns the outcome: the
	// caller was told the kill succeeded. A report armed while the results
	// were committing still outranks the exit code.
	switch {
	case entry.killRequested:
		to, reason, actor, byReport = domain.RunAbandoned, "killed", entry.killActor, false
	case reported == "" && entry.reported != "" && !entry.status.Terminal():
		to, reason, byReport = entry.reported, reportedClose(entry.reported).reason, true
	}
	retain := entry.missionAssigned && !entry.killRequested && s.cfg.RunContainerTTL >= 0
	if retain {
		deadline := time.Now().UTC().Add(s.cfg.RunContainerTTL)
		entry.evidenceIdentity = finishCaptureIdentity(entry.evidenceIdentity, published, committed, code)
		sc := entry.sidecar()
		sc.Retained = true
		sc.RetainedUntil = &deadline
		sc.EvidencePending = true
		if persistErr := s.persistRetainedSidecar(sc); persistErr != nil {
			entry.finalizing = false
			s.recordCleanupErrorLocked(entry, cleanupStateError)
			s.mu.Unlock()
			slog.Warn("scheduler: persist completed worker retention", "run", entry.runID, "error", persistErr)
			entry.lifecycleMu.Unlock()
			return
		}
		entry.retained = true
		entry.retainedUntil = &deadline
		entry.evidencePending = true
		reason = retainedCompletionReason
	}
	err := s.transitionOutcomeLocked(ctx, entry.runID, entry.workspaceID, entry.status, to, reason, actor, byReport)
	s.mu.Unlock()
	if err != nil && !errors.Is(err, ErrInvalidTransition) {
		slog.Warn("scheduler: record exit status", "run", entry.runID, "error", err)
		// Without a durable terminal row, destruction would orphan the
		// recoverable checkout and transcript.
		s.retainAfterEvidenceFailure(entry)
		s.recordCleanupError(entry, cleanupStateError)
		entry.lifecycleMu.Unlock()
		return
	}

	identity := ""
	s.mu.Lock()
	// The status the row ended with: a report handed off since the
	// transition may have overridden the exit's (see overrideExitLocked).
	to = entry.status
	if s.runs[entry.runID] == entry {
		identity = finishCaptureIdentity(entry.evidenceIdentity, published, committed, code)
		entry.evidenceIdentity = identity
		entry.evidencePending = retain
		if sidecarErr := s.writeSidecar(entry.sidecar()); sidecarErr != nil {
			slog.Warn("scheduler: persist evidence identity", "run", entry.runID, "error", sidecarErr)
		}
	}
	s.mu.Unlock()
	if identity == "" {
		identity = finishCaptureIdentity("", published, committed, code)
	}
	if retain {
		defer entry.lifecycleMu.Unlock()
		if settleErr := s.settleRetainedCompletion(ctx, entry); settleErr != nil {
			slog.Warn("scheduler: settle completed worker retention", "run", entry.runID, "error", settleErr)
		}
		_ = s.entryDriver(entry).Stop(ctx, entry.runID)
		s.cfg.PTY.StopSessionsWithPrefix(ctx, string(ptyhost.RunShellSession(entry.runID, "")))
		return
	}
	entry.lifecycleMu.Unlock()
	if captureErr := s.captureFinishEvidence(ctx, entry.runID, to, identity); captureErr != nil {
		logEvidenceFailure(entry.runID, captureErr)
		s.retainAfterEvidenceFailure(entry)
		return
	}
	if err := s.MarkDevelopmentContainerEnded(ctx, entry.runID); err != nil {
		slog.Warn("scheduler: record development container exit", "run", entry.runID, "error", err)
		s.retainAfterEvidenceFailure(entry)
		s.recordCleanupError(entry, cleanupStateError)
		return
	}
	if err := s.StopDevelopmentRun(ctx, entry.runID); err != nil {
		slog.Warn("scheduler: stop development resources", "run", entry.runID, "error", err)
		s.retainAfterEvidenceFailure(entry)
		s.recordCleanupError(entry, cleanupRuntimeError)
		return
	}

	// Keep the recording available through the capture above. A process that
	// exits has already stopped producing PTY bytes, so stopping the session
	// now cannot change the captured transcript.
	if err := s.entryDriver(entry).Stop(ctx, entry.runID); err != nil {
		slog.Warn("scheduler: stop pty session", "run", entry.runID, "error", err)
	}
	s.cfg.PTY.StopSessionsWithPrefix(ctx, string(ptyhost.RunShellSession(entry.runID, "")))

	// ErrInvalidTransition means the run already reached a terminal state
	// (e.g. CloseRun raced the exit); the cleanup below still applies.
	if destroyErr := s.cfg.Runtime.Destroy(ctx, entry.containerID); destroyErr != nil &&
		!errors.Is(destroyErr, runtime.ErrNotFound) {
		// Keep every ownership reference when destruction is uncertain. The
		// expiry sweep will retry without allowing checkout/home/coordination
		// reclamation to race a still-live container.
		s.mu.Lock()
		if s.runs[entry.runID] == entry {
			now := time.Now().UTC()
			entry.retained = true
			entry.retainedUntil = &now
			entry.finalizing = false
			entry.destroyPending = true
			entry.cleanupError = cleanupRuntimeError
			if sidecarErr := s.writeSidecar(entry.sidecar()); sidecarErr != nil {
				slog.Warn("scheduler: persist retained sidecar after destroy failure",
					"run", entry.runID, "error", sidecarErr)
			}
			s.publishRetentionLocked(entry.runID)
		}
		s.mu.Unlock()
		slog.Warn("scheduler: destroy container", "run", entry.runID, "error", destroyErr)
		return
	}
	// Publish release activity while the lifecycle owner still protects cache
	// cleanup, never after removing its last ownership reference.
	s.touchRunCache(entry.memberID)
	s.removeSidecar(entry.runID)
	s.closeDone(entry)
	s.mu.Lock()
	if s.runs[entry.runID] == entry {
		entry.finalizing = false
		delete(s.runs, entry.runID)
		s.publishRetentionLocked(entry.runID)
	}
	s.mu.Unlock()
}

// sweepRetained deliberately starts no per-run goroutines.
func (s *Scheduler) sweepRetained(ctx context.Context) {
	s.mu.Lock()
	entries := make([]*supervised, 0)
	for _, entry := range s.runs {
		if entry.destroyPending || (entry.retained && entry.retainedUntil != nil) || (entry.exitObserved && !entry.finalizing) {
			entries = append(entries, entry)
		}
	}
	s.mu.Unlock()
	now := time.Now().UTC()
	for _, entry := range entries {
		s.mu.Lock()
		pending := s.runs[entry.runID] == entry && entry.destroyPending
		due := s.runs[entry.runID] == entry && entry.retainedUntil != nil &&
			!now.Before(*entry.retainedUntil)
		settle := s.runs[entry.runID] == entry && entry.retained && entry.status.Terminal() &&
			entry.evidencePending
		finalize := s.runs[entry.runID] == entry && entry.exitObserved && !entry.status.Terminal() && !entry.finalizing
		if finalize {
			entry.finalizing = true
		}
		s.mu.Unlock()
		if pending {
			s.retryDestroyPending(ctx, entry)
			continue
		}
		if finalize {
			s.finalize(entry, entry.exitCode)
			continue
		}
		if settle && !due {
			entry.lifecycleMu.Lock()
			if err := s.settleRetainedCompletion(ctx, entry); err != nil {
				slog.Warn("scheduler: settle retained run", "run", entry.runID, "error", err)
			}
			entry.lifecycleMu.Unlock()
		}
		if due {
			if err := s.expireRetained(ctx, entry); err != nil {
				slog.Warn("scheduler: retained expiry", "run", entry.runID, "error", err)
			}
		}
	}
}

// retryDestroyPending retries a previously uncertain destruction while
// retaining the lifecycle owner. It terminalizes an active row only after
// the runtime confirms the container is gone.
func (s *Scheduler) retryDestroyPending(ctx context.Context, entry *supervised) {
	if entry == nil {
		return
	}
	entry.lifecycleMu.Lock()
	defer entry.lifecycleMu.Unlock()
	if err := s.retryDestroyPendingLocked(ctx, entry); err != nil {
		slog.Warn("scheduler: retry destroy-pending cleanup", "run", entry.runID, "error", err)
	}
}

// retryDestroyPendingLocked is the same retry for a caller that already owns
// entry.lifecycleMu, such as Kill.
func (s *Scheduler) retryDestroyPendingLocked(ctx context.Context, entry *supervised) (resultErr error) {
	if entry == nil {
		return nil
	}
	s.mu.Lock()
	if s.runs[entry.runID] != entry || !entry.destroyPending {
		s.mu.Unlock()
		return nil
	}
	cid := entry.containerID
	s.mu.Unlock()
	cause := cleanupLookupError
	defer func() {
		if resultErr != nil {
			s.recordCleanupError(entry, cause)
		}
	}()
	if cid == "" {
		found, err := s.cfg.Runtime.FindByCreationKey(ctx, string(entry.runID))
		if err != nil {
			if errors.Is(err, runtime.ErrNotFound) {
				cause = cleanupEvidenceError
				if preserveErr := s.preserveRecoveryWork(ctx, entry.runID, entry.task); preserveErr != nil {
					return preserveErr
				}
				cause = cleanupStateError
				return s.finishDestroyPending(ctx, entry)
			}
			return fmt.Errorf("find destroy-pending container: %w", err)
		}
		cid = found
		s.mu.Lock()
		if s.runs[entry.runID] == entry && entry.destroyPending {
			entry.containerID = cid
			if err := s.writeSidecar(entry.sidecar()); err != nil {
				slog.Warn("scheduler: persist resolved destroy-pending container", "run", entry.runID, "error", err)
			}
		}
		s.mu.Unlock()
	}
	// Required evidence must succeed before runtime deletion on retries too.
	// The failed attempt may never have captured it.
	cause = cleanupEvidenceError
	if preserveErr := s.preserveRecoveryWork(ctx, entry.runID, entry.task); preserveErr != nil {
		return preserveErr
	}
	cause = cleanupRuntimeError
	if err := s.destroyDevelopmentContainer(ctx, entry.runID, cid); err != nil && !errors.Is(err, runtime.ErrNotFound) {
		return fmt.Errorf("destroy-pending container: %w", err)
	}
	cause = cleanupStateError
	return s.finishDestroyPending(ctx, entry)
}

// expireRetained serializes expiry against every other operation on a
// retained run.
func (s *Scheduler) expireRetained(ctx context.Context, entry *supervised) error {
	if entry == nil {
		return nil
	}
	entry.lifecycleMu.Lock()
	defer entry.lifecycleMu.Unlock()
	err := s.expireRetainedLocked(ctx, entry)
	if err == nil {
		return nil
	}
	s.mu.Lock()
	retry := s.runs[entry.runID] == entry && entry.retained && entry.destroyPending
	s.mu.Unlock()
	if retry {
		s.startRetainedWaitAfterDestroyFailure(ctx, entry)
	}
	return err
}

// expireRetainedLocked is called with entry.lifecycleMu held.
func (s *Scheduler) expireRetainedLocked(ctx context.Context, entry *supervised) error {
	s.mu.Lock()
	if s.runs[entry.runID] != entry || !entry.retained {
		s.mu.Unlock()
		return nil
	}
	deadline := entry.retainedUntil
	cid := entry.containerID
	reason := retainedExpiredReason
	if s.cfg.RunContainerTTL >= 0 && deadline != nil && time.Now().UTC().Before(*deadline) {
		reason = retainedUnavailableReason
	}
	identity := entry.evidenceIdentity
	s.mu.Unlock()

	run, err := s.cfg.Store.GetRun(ctx, entry.runID)
	if err != nil {
		s.recordCleanupError(entry, cleanupLookupError)
		return fmt.Errorf("scheduler: load retained run: %w", err)
	}
	if !run.Status.Terminal() {
		// A reopened row, including one ahead of a stale retained sidecar,
		// is not eligible for terminal cleanup.
		return retainedTransitionError()
	}
	if identity == "" {
		identity = evidenceCommitIdentity(run.LastCommit, "", -1)
	}
	if err := s.captureFinishEvidence(ctx, entry.runID, run.Status, identity); err != nil {
		logEvidenceFailure(entry.runID, err)
		s.retainAfterEvidenceFailure(entry)
		return err
	}

	if err := s.destroyDevelopmentContainer(ctx, entry.runID, cid); err != nil && !errors.Is(err, runtime.ErrNotFound) {
		// Keep every ownership reference and make this owner due now. The
		// bounded sweep will retry without allowing cleanup to race a live
		// container.
		s.mu.Lock()
		entry.cleanupError = cleanupRuntimeError
		s.markRetainedDestroyDueLocked(entry)
		s.mu.Unlock()
		return fmt.Errorf("scheduler: destroy retained container: %w", err)
	}
	s.touchRunCache(entry.memberID)

	var transitionErr error
	s.mu.Lock()
	if s.runs[entry.runID] == entry && entry.retained {
		if released, ok := releasedReason(run.Status, run.Reason, reason); ok {
			transitionErr = s.relabelLocked(ctx, entry.runID, run.WorkspaceID, run.Status, released, "")
		}
		entry.retained = false
		entry.retainedUntil = nil
		entry.destroyPending = false
		entry.evidencePending = false
		entry.cleanupError = ""
		if entry.userReservation != nil {
			delete(s.credentialUsers, entry.userReservation)
			entry.userReservation = nil
		}
	}
	s.mu.Unlock()
	// Remove durable ownership before publishing completion. Keep the exact
	// entry installed until its completion signal is closed, so consumers that
	// observe owner removal cannot race an open done channel.
	s.removeSidecar(entry.runID)
	s.mu.Lock()
	if s.runs[entry.runID] == entry {
		s.closeDone(entry)
		delete(s.runs, entry.runID)
		s.publishRetentionLocked(entry.runID)
	}
	s.mu.Unlock()
	return transitionErr
}

// checkStalls counts only the agent's own PTY output as activity: a steer's
// banner and the echo of its input are the server's. Where the harness
// reports both ends of a turn, terminal activity never un-parks a run - a TUI
// repainting while the member types is not work. Un-parking needs observed
// activity, or after a restart every parked run would resume on the first
// poll.
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
		paused, status, started, acp := e.paused, e.status, e.startedAt, e.acp
		s.mu.Unlock()
		if !live || paused || (status != domain.RunRunning && status != domain.RunNeedsAttention) {
			continue
		}
		activity, observed := started, false
		if t := s.driver(acp).LastActivity(e.runID); t.After(activity) {
			activity, observed = t, true
		}
		if t, ok := s.cfg.Git.LastFileChange(e.runID); ok && t.After(activity) {
			activity, observed = t, true
		}

		s.mu.Lock()
		if s.runs[e.runID] == e && !e.paused {
			// A working report leaves no other trace, so it counts as activity.
			if e.lastWorking.After(activity) {
				activity, observed = e.lastWorking, true
			}
			idle := now.Sub(activity)
			// A run whose harness reports both ends of a turn and has said
			// it is waiting is released by the agent's own next report, not
			// by anything on the terminal.
			heldForTheMember := e.agentReport.State == agentstatus.Idle &&
				e.reporter == harness.ReporterFull
			released := e.status == domain.RunNeedsAttention && observed &&
				idle <= s.cfg.StallThreshold && !heldForTheMember
			if released && e.agentReport.State == agentstatus.Idle {
				released = e.unparks(activity, s.cfg.turnTail)
			}
			var err error
			switch {
			case e.status == domain.RunRunning && idle > s.cfg.StallThreshold:
				reason := fmt.Sprintf("stalled: no output or file changes for %s", idle.Truncate(time.Second))
				// The stall is the turn end only on a harness that cannot
				// report one; elsewhere the agent is quiet mid-turn and the
				// blocked reason waits for its turn-end report.
				showsBlocked := e.blockedReason != "" && e.reporter == harness.ReporterNone
				if showsBlocked {
					reason = e.blockedReason
				}
				err = s.transitionLocked(ctx, e.runID, e.workspaceID, e.status, domain.RunNeedsAttention, reason, "")
				if err == nil && showsBlocked && !e.blockedShown {
					e.blockedShown = true
					if serr := s.writeSidecar(e.sidecar()); serr != nil {
						slog.Warn("scheduler: persist shown blocked reason", "run", e.runID, "error", serr)
					}
				}
			case released:
				// The run goes back to being judged on silence alone, so
				// the next quiet threshold parks it as a stall again - and
				// a restart must not resurrect the report this clears.
				e.agentReport = agentstatus.Report{}
				e.parkedAt, e.postParkActivity = time.Time{}, time.Time{}
				if e.blockedShown {
					e.blockedReason, e.blockedShown = "", false
				}
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

// defaultTurnTail bounds a TUI's trailing frames after a waiting report. It is
// not a measured vendor number, so it is generous.
const defaultTurnTail = 3 * time.Second

// unparks reports whether terminal activity on a self-parked run is the next
// turn rather than the tail of the one that ended. A turn-end-only reporter
// fires while its turn is still being drawn, so the tail is measured against
// the clock, not polls, and past it activity must still move on a later poll.
// Caller must hold s.mu.
func (e *supervised) unparks(activity time.Time, tail time.Duration) bool {
	if !activity.After(e.parkedAt.Add(tail)) {
		return false
	}
	if e.postParkActivity.IsZero() {
		e.postParkActivity = activity
		return false
	}
	return activity.After(e.postParkActivity)
}

// sweepCheckouts never removes the branch or transcript.
func (s *Scheduler) sweepCheckouts(ctx context.Context) {
	workspaces, err := s.cfg.Store.ListWorkspaces(ctx)
	if err != nil {
		slog.Warn("scheduler: checkout gc: list workspaces", "error", err)
		return
	}
	// Checkouts are normally per-run-ID. Relaunch reuses the retained run's
	// exact checkout; never trust a stale terminal snapshot when that checkout
	// has since been reopened or adopted.
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

			var lifecycle *supervised
			s.mu.Lock()
			lifecycle = s.runs[r.ID]
			s.mu.Unlock()
			if lifecycle == nil && s.RetainsContainer(ctx, r.ID) {
				continue
			}
			if lifecycle != nil {
				lifecycle.lifecycleMu.Lock()
				s.mu.Lock()
				same := s.runs[r.ID] == lifecycle
				retained := same && lifecycle.retained
				finalizing := same && lifecycle.finalizing
				running := same && !lifecycle.status.Terminal()
				s.mu.Unlock()
				if !same || retained || finalizing || running {
					lifecycle.lifecycleMu.Unlock()
					continue
				}
			}

			// Lifecycle ownership is held before this durable re-read. The
			// row listed above may have been reopened or otherwise changed
			// while the sweep was enumerating it.
			fresh, err := s.cfg.Store.GetRun(ctx, r.ID)
			if err != nil {
				if lifecycle != nil {
					lifecycle.lifecycleMu.Unlock()
				}
				slog.Warn("scheduler: checkout gc: reread run", "run", r.ID, "error", err)
				continue
			}
			if !fresh.Status.Terminal() || fresh.Worktree == "" ||
				fresh.Worktree != r.Worktree || fresh.FinishedAt == nil ||
				fresh.FinishedAt.After(cutoff) {
				if lifecycle != nil {
					lifecycle.lifecycleMu.Unlock()
				}
				continue
			}
			if lifecycle == nil && s.RetainsContainer(ctx, r.ID) {
				continue
			}

			// Evidence owns a per-run capture/cleanup lock. Capture both
			// sources before either is removed; a failed required capture
			// leaves the checkout and transcript recoverable for retry.
			identity := fresh.LastCommit
			if lifecycle != nil {
				s.mu.Lock()
				if lifecycle.evidenceIdentity != "" {
					identity = lifecycle.evidenceIdentity
				}
				s.mu.Unlock()
			}
			if identity == "" {
				identity = "none"
			}
			cleanup := func(cleanupCtx context.Context) error {
				if err := s.deleteDevelopment(cleanupCtx, r.ID); err != nil {
					return err
				}
				if err := s.cfg.PTY.RemoveRunTranscripts(cleanupCtx, r.ID); err != nil {
					return fmt.Errorf("scheduler: checkout gc: remove run transcripts: %w", err)
				}
				if err := s.cfg.Git.RemoveRunCheckout(cleanupCtx, r.ID); err != nil {
					return fmt.Errorf("scheduler: checkout gc: remove run checkout: %w", err)
				}
				return nil
			}
			if err := s.captureBeforeCleanupEvidence(ctx, r.ID, identity, cleanup); err != nil {
				if lifecycle != nil {
					lifecycle.lifecycleMu.Unlock()
				}
				slog.Warn("scheduler: checkout gc: capture before cleanup", "run", r.ID, "error", err)
				continue
			}
			var clearErr error
			if clearer, ok := s.cfg.Store.(store.RunWorktreeStore); ok {
				clearErr = clearer.ClearRunWorktree(ctx, fresh.ID, fresh.Worktree, fresh.Status)
			} else {
				fresh.Worktree = ""
				clearErr = s.cfg.Store.UpdateRun(ctx, fresh)
			}
			if clearErr != nil {
				if lifecycle != nil {
					lifecycle.lifecycleMu.Unlock()
				}
				slog.Warn("scheduler: checkout gc: clear worktree", "run", r.ID, "error", clearErr)
				continue
			}
			s.removeSidecar(r.ID)
			if lifecycle != nil {
				lifecycle.lifecycleMu.Unlock()
			}
		}
	}
}

// sweepArchived keeps no state between ticks: every candidate is re-validated,
// so a restore or publish failure only costs a retry.
func (s *Scheduler) sweepArchived(ctx context.Context) {
	cutoff := time.Now().UTC().Add(-domain.ArchiveRetention)
	candidates, err := s.cfg.Store.ListRunsArchivedBefore(ctx, cutoff)
	if err != nil {
		slog.Warn("scheduler: archive sweep: list archived runs", "error", err)
		return
	}
	for _, candidate := range candidates {
		if err := s.sweepArchivedRun(ctx, candidate.ID, cutoff); err != nil {
			slog.Warn("scheduler: archive sweep", "run", candidate.ID, "error", err)
		}
	}
}

// sweepArchivedRun holds archiveMu across the re-read and the delete so a
// restore cannot race it. Runs with durable container ownership are skipped:
// DeleteRun would take lifecycleMu, which Relaunch takes before archiveMu.
func (s *Scheduler) sweepArchivedRun(ctx context.Context, id domain.RunID, cutoff time.Time) error {
	s.archiveMu.Lock()
	defer s.archiveMu.Unlock()

	fresh, err := s.cfg.Store.GetRun(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("reread run: %w", err)
	}
	if fresh.ArchivedAt == nil || fresh.ArchivedAt.After(cutoff) || !fresh.Status.Final() ||
		s.RetainsContainer(ctx, id) {
		return nil
	}

	// Never destroy the only copy of unpublished work unattended: a
	// checkout that never got a container back after a crash can still
	// carry commits the branch does not have.
	if fresh.Worktree != "" {
		if _, err := s.cfg.Git.PublishRunBranch(ctx, id); err != nil {
			return fmt.Errorf("publish run branch: %w", err)
		}
	}

	if err := s.DeleteRun(ctx, id, ""); err != nil {
		return fmt.Errorf("delete run: %w", err)
	}
	s.publishTimeline(ctx, fresh.WorkspaceID, id, "", events.TimelineNote,
		"archived run deleted after the retention period")
	return nil
}

const (
	exitDrainWait = 2 * time.Second
	maxExitDetail = 200
)

// exitDetail is the last non-empty line the failed agent printed.
func (s *Scheduler) exitDetail(ctx context.Context, entry *supervised) string {
	s.mu.Lock()
	detail, acp := entry.exitDetail, entry.acp
	s.mu.Unlock()
	if detail == "" && !acp {
		line, err := s.cfg.PTY.LastLine(ctx, entry.runID, exitDrainWait)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			slog.Warn("scheduler: read the agent's last output", "run", entry.runID, "error", err)
		}
		detail = line
	}
	lines := strings.FieldsFunc(detail, func(r rune) bool { return r == '\n' || r == '\r' })
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); line != "" {
			if runes := []rune(line); len(runes) > maxExitDetail {
				line = string(runes[:maxExitDetail-3]) + "..."
			}
			return line
		}
	}
	return ""
}
