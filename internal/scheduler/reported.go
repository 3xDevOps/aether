package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/harness"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/store"
)

// reportFinishDeadline is how long an armed run on a harness with no status
// reporter waits before the poll loop finishes it: that harness never says
// its turn ended.
const reportFinishDeadline = 2 * time.Minute

const (
	reportedSuccessReason         = "agent reported success"
	reportedFailureReason         = "agent reported failure"
	reportedSuccessRetainedReason = reportedSuccessReason + "; retained container"
	reportedFailureRetainedReason = reportedFailureReason + "; retained container"
	blockedReasonPrefix           = "blocked: "
	exitedCompletedReason         = "agent exited; results committed"
	exitedFailedReasonPrefix      = "agent exited "
)

// closeSpec is what ending a live run records: a human's merged or
// abandoned close, or the completed or failed outcome its agent reported.
type closeSpec struct {
	outcome domain.RunStatus
	actor   domain.MemberID
	// reason is used when no container is retained, retained when one is.
	reason   string
	retained string
	commit   string
	// reported marks the close an agent's report causes, which leaves the
	// outcome unseen by the run's owner.
	reported bool
}

func humanClose(outcome domain.RunStatus, actor domain.MemberID) closeSpec {
	commit := "wip: "
	if outcome == domain.RunMerged {
		commit = "aether: "
	}
	return closeSpec{outcome: outcome, actor: actor, reason: "closed", retained: retainedCloseReason, commit: commit}
}

func reportedClose(outcome domain.RunStatus) closeSpec {
	if outcome == domain.RunCompleted {
		return closeSpec{outcome: outcome, reason: reportedSuccessReason, retained: reportedSuccessRetainedReason, commit: "aether: ", reported: true}
	}
	return closeSpec{outcome: outcome, reason: reportedFailureReason, retained: reportedFailureRetainedReason, commit: "wip: ", reported: true}
}

// retainedReason reports whether a terminal row promises a retained,
// relaunchable container.
func retainedReason(status domain.RunStatus, reason string) bool {
	switch status {
	case domain.RunMerged, domain.RunAbandoned:
		return reason == retainedCloseReason
	case domain.RunCompleted:
		return reason == reportedSuccessRetainedReason
	case domain.RunFailed:
		return reason == reportedFailureRetainedReason
	}
	return false
}

// releasedReason is the reason a retained run keeps once its container is
// gone, and whether the row needs it. An agent-reported run only drops the
// retention suffix; a closed run records why the container went away.
func releasedReason(status domain.RunStatus, reason, closed string) (string, bool) {
	switch {
	case status == domain.RunCompleted && reason == reportedSuccessRetainedReason:
		return reportedSuccessReason, true
	case status == domain.RunFailed && reason == reportedFailureRetainedReason:
		return reportedFailureReason, true
	case status == domain.RunMerged || status == domain.RunAbandoned:
		return closed, true
	}
	return "", false
}

// relabelLocked rewrites a terminal run's reason without changing its
// status, which the lifecycle table only allows for merged and abandoned.
// The row keeps its outcome_unseen flag, and the event carries it. The
// caller must hold s.mu, which Seen also holds, so the flag read here is
// the one this event reports.
func (s *Scheduler) relabelLocked(ctx context.Context, run domain.RunID, workspace domain.WorkspaceID, status domain.RunStatus, reason string, actor domain.MemberID) error {
	if !status.Terminal() {
		return fmt.Errorf("%w: relabel %s", ErrInvalidTransition, status)
	}
	public := publicRunStatusReason(reason)
	if err := s.cfg.Store.UpdateRunStatus(ctx, run, status, public, nil, nil); err != nil {
		return err
	}
	row, err := s.cfg.Store.GetRun(ctx, run)
	if err != nil {
		return err
	}
	s.publish(ctx, events.Event{
		WorkspaceID: workspace,
		RunID:       run,
		ActorID:     actor,
		Payload:     events.RunStatusPayload{From: status, To: status, Reason: public, OutcomeUnseen: row.OutcomeUnseen},
	})
	return nil
}

// Seen clears a run's outcome_unseen flag once its owner has opened it.
// Only the current owner may clear it. Clearing a clear flag returns the
// run and publishes nothing. s.mu orders the clear and its event against
// the run.status events that also carry the flag.
func (s *Scheduler) Seen(ctx context.Context, run domain.RunID, actor domain.MemberID) (*domain.Run, error) {
	current, err := s.cfg.Store.GetRun(ctx, run)
	if err != nil {
		return nil, err
	}
	if current.MemberID != actor {
		return nil, fmt.Errorf("%w: only the run's owner can mark its outcome seen", permissions.ErrDenied)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	changed, err := s.cfg.Store.ClearRunOutcomeUnseen(ctx, run, actor)
	if err != nil {
		return nil, err
	}
	fresh, err := s.cfg.Store.GetRun(ctx, run)
	if err != nil {
		return nil, err
	}
	if changed {
		s.publish(ctx, events.Event{
			WorkspaceID: fresh.WorkspaceID,
			RunID:       run,
			ActorID:     actor,
			Payload:     events.RunOutcomeSeenPayload{},
		})
		s.publishTimeline(ctx, fresh.WorkspaceID, run, actor, events.TimelineNote, "outcome seen by owner")
	}
	return fresh, nil
}

// FinishReported arms a live run to finish with the outcome its agent
// reported through coord.report: completed for success, failed for failure.
// It only records the request; the run finishes on the agent's next
// turn-end wait, at reportFinishDeadline when the harness cannot report
// one, or when the process exits, whichever is first. reportedAt is when
// the report was finalized: a report older than the run's current launch
// speaks for a launch a relaunch already ended and is ignored. A run whose
// process exited first takes the reported outcome over the exit's (see
// overrideExitLocked). A run with no live owner that is not terminal yet
// returns an error so the caller retries.
func (s *Scheduler) FinishReported(ctx context.Context, run domain.RunID, outcome domain.RunStatus, reportedAt time.Time) error {
	if outcome != domain.RunCompleted && outcome != domain.RunFailed {
		return fmt.Errorf("%w: reported outcome must be completed or failed, got %q", ErrInvalidTransition, outcome)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.runs[run]
	if entry == nil || entry.status.Terminal() {
		return s.overrideExitLocked(ctx, run, outcome, reportedAt)
	}
	if reportedAt.Before(entry.startedAt) || entry.reported == outcome {
		return nil
	}
	prior, priorAt, blocked, shown := entry.reported, entry.reportedAt, entry.blockedReason, entry.blockedShown
	entry.reported, entry.reportedAt = outcome, time.Now().UTC()
	entry.blockedReason, entry.blockedShown = "", false
	if err := s.writeSidecar(entry.sidecar()); err != nil {
		entry.reported, entry.reportedAt, entry.blockedReason, entry.blockedShown = prior, priorAt, blocked, shown
		return fmt.Errorf("scheduler: arm reported finish for %s: %w", run, err)
	}
	return nil
}

// overrideExitLocked records a reported outcome on a run whose process
// exited before the report reached the scheduler, so the exit wrote the
// row first. Invariant: the agent's report outranks its exit code, as it
// does in finalize. That is the only reason this write skips
// legalTransition, which refuses failed -> completed, so it is reachable
// only from FinishReported and rewrites only an ordinary run's row whose
// status and reason the exit produced in the same launch the report came
// from. A human close, a kill, a retained close, an interrupted run and a
// mission run are left alone. The caller must hold s.mu.
func (s *Scheduler) overrideExitLocked(ctx context.Context, run domain.RunID, outcome domain.RunStatus, reportedAt time.Time) error {
	r, err := s.cfg.Store.GetRun(ctx, run)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("scheduler: finish reported run %s: %w", run, err)
	}
	if !r.Status.Terminal() {
		return fmt.Errorf("scheduler: finish reported run %s: the run is %s and has no live container yet", run, r.Status)
	}
	exited := (r.Status == domain.RunCompleted && r.Reason == exitedCompletedReason) ||
		(r.Status == domain.RunFailed && strings.HasPrefix(r.Reason, exitedFailedReasonPrefix))
	if !exited || r.MissionID != "" || (r.StartedAt != nil && reportedAt.Before(*r.StartedAt)) {
		return nil
	}
	reason := publicRunStatusReason(reportedClose(outcome).reason)
	if err := s.cfg.Store.FinishRunReported(ctx, run, outcome, reason, nil, nil); err != nil {
		return fmt.Errorf("scheduler: finish reported run %s over its exit: %w", run, err)
	}
	if e := s.runs[run]; e != nil {
		e.status = outcome
	}
	s.publish(ctx, events.Event{
		WorkspaceID: r.WorkspaceID,
		RunID:       run,
		Payload:     events.RunStatusPayload{From: r.Status, To: outcome, Reason: reason, OutcomeUnseen: true},
	})
	return nil
}

// ReportBlocked records the summary of the agent's blocked report reportID.
// The next turn-end park - a waiting-for-input report, or a stall on a
// harness with no reporter - shows it as the needs-attention reason instead
// of the generic one, and the first resume after that park clears it. A
// permission or answer wait keeps its own reason and leaves it pending.
// Parking right away would not survive: the report is made from inside a
// tool call, so a working report follows it. A replay of the report last
// applied, or a report older than the run's current launch, changes
// nothing.
func (s *Scheduler) ReportBlocked(ctx context.Context, run domain.RunID, reportID, summary string, reportedAt time.Time) error {
	reason := blockedReasonPrefix + summary
	if runes := []rune(reason); len(runes) > maxPublicRunStatusReason {
		reason = string(runes[:maxPublicRunStatusReason])
	}
	s.mu.Lock()
	if entry := s.runs[run]; entry != nil {
		defer s.mu.Unlock()
		if entry.status.Terminal() || entry.reported != "" || entry.blockedReportID == reportID ||
			reportedAt.Before(entry.startedAt) {
			return nil
		}
		blocked, shown, id := entry.blockedReason, entry.blockedShown, entry.blockedReportID
		entry.blockedReason, entry.blockedShown, entry.blockedReportID = reason, false, reportID
		if err := s.writeSidecar(entry.sidecar()); err != nil {
			entry.blockedReason, entry.blockedShown, entry.blockedReportID = blocked, shown, id
			return fmt.Errorf("scheduler: record blocked report for %s: %w", run, err)
		}
		return nil
	}
	s.mu.Unlock()
	return s.requireFinished(ctx, run, "blocked report")
}

// requireFinished is the answer for a run with no supervised owner: done
// when the row is terminal or deleted, retryable otherwise.
func (s *Scheduler) requireFinished(ctx context.Context, run domain.RunID, what string) error {
	r, err := s.cfg.Store.GetRun(ctx, run)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("scheduler: %s %s: %w", what, run, err)
	}
	if r.Status.Terminal() {
		return nil
	}
	return fmt.Errorf("scheduler: %s %s: the run is %s and has no live container yet", what, run, r.Status)
}

// startReportedFinishLocked finishes an armed run off the caller's path, so
// the hook or poll that triggered it returns first. The caller must hold
// s.mu.
func (s *Scheduler) startReportedFinishLocked(entry *supervised) {
	if entry.reportFinishing || s.superCtx.Err() != nil {
		return
	}
	entry.reportFinishing = true
	s.wg.Add(1)
	go s.finishReported(entry)
}

// finishOverdueReports finishes armed runs on harnesses that cannot say
// their turn ended. A harness that can is finished by that report alone, so
// an agent that keeps working after it reported is not cut off.
func (s *Scheduler) finishOverdueReports() {
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, entry := range s.runs {
		if entry.reported != "" && entry.reporter == harness.ReporterNone && !entry.status.Terminal() &&
			now.Sub(entry.reportedAt) >= reportFinishDeadline {
			s.startReportedFinishLocked(entry)
		}
	}
}

// finishReported closes an armed run the way a human close would, recording
// the reported outcome. A human close, a kill, or the process exit that got
// there first decides the run instead.
func (s *Scheduler) finishReported(entry *supervised) {
	defer s.wg.Done()
	ctx, cancel := context.WithTimeout(context.Background(), finalizeTimeout)
	defer cancel()
	entry.lifecycleMu.Lock()
	defer entry.lifecycleMu.Unlock()
	if err := s.finishReportedLocked(ctx, entry); err != nil {
		slog.Warn("scheduler: finish reported run", "run", entry.runID, "error", err)
	}
	s.mu.Lock()
	entry.reportFinishing = false
	s.mu.Unlock()
}

// finishReportedLocked is finishReported under entry.lifecycleMu. A failed
// close is retried after another deadline rather than on every poll.
func (s *Scheduler) finishReportedLocked(ctx context.Context, entry *supervised) error {
	s.mu.Lock()
	outcome := entry.reported
	if s.runs[entry.runID] != entry || outcome == "" || entry.finalizing || entry.killRequested || entry.destroyPending {
		s.mu.Unlock()
		return nil
	}
	if entry.status.Terminal() {
		entry.reported = ""
		err := s.writeSidecar(entry.sidecar())
		if err != nil {
			entry.reported = outcome
			err = fmt.Errorf("persist cleared reported outcome: %w", err)
		}
		s.mu.Unlock()
		return err
	}
	status, workspace, cid := entry.status, entry.workspaceID, entry.containerID
	mode, paused := entry.launchMode, entry.paused
	s.mu.Unlock()

	if err := s.closeLiveLocked(ctx, entry, status, workspace, cid, mode, paused, reportedClose(outcome)); err != nil {
		s.mu.Lock()
		entry.reportedAt = time.Now().UTC()
		s.mu.Unlock()
		return fmt.Errorf("close with reported %s: %w", outcome, err)
	}
	return nil
}

// clearRetainedClose drops what a retained sidecar promised once its run's
// row is active again: a relaunch reopened the run, so both the retention
// and the report that closed it are over. Left in place, the report would
// re-arm the reopened run. A sidecar with no retained marker is a live
// run's own, and its arm stands.
func (sc *sidecar) clearRetainedClose() {
	if !sc.Retained && sc.RetainedUntil == nil {
		return
	}
	sc.Retained = false
	sc.RetainedUntil = nil
	sc.ReportedOutcome = ""
	sc.BlockedReason = ""
	sc.BlockedShown = false
}
