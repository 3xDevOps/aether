package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/agentstatus"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/harness"
	"github.com/3xDevOps/Aether/internal/store"
)

// reportFinishDeadline is the existing fallback for outcome settlement when a
// harness cannot report a turn end, or a stall parked it at needs-attention.
// Interactive outcomes park; background outcomes finish.
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

// closeSpec is what ending a run through closeRun records.
type closeSpec struct {
	outcome domain.RunStatus
	actor   domain.MemberID
	// reason is used when no container is retained, retained when one is.
	reason   string
	retained string
	// mission marks a mission worker's completion, which retains the
	// container but never makes it relaunchable.
	mission bool
	// reported marks the close an agent's report causes, which leaves the
	// outcome unseen by the run's owner.
	reported bool
}

func (c closeSpec) cause() finishCause {
	if c.reported {
		return causeReported
	}
	return memberCause(c.actor)
}

func humanClose(outcome domain.RunStatus, actor domain.MemberID) closeSpec {
	return closeSpec{outcome: outcome, actor: actor, reason: "closed", retained: retainedCloseReason}
}

func missionClose(outcome domain.RunStatus) closeSpec {
	return closeSpec{outcome: outcome, reason: "closed", retained: retainedCompletionReason, mission: true}
}

func reportedClose(outcome domain.RunStatus) closeSpec {
	if outcome == domain.RunCompleted {
		return closeSpec{outcome: outcome, reason: reportedSuccessReason, retained: reportedSuccessRetainedReason, reported: true}
	}
	return closeSpec{outcome: outcome, reason: reportedFailureReason, retained: reportedFailureRetainedReason, reported: true}
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

// retentionValid reports whether a terminal row's reason matches the
// retained container its sidecar records.
func retentionValid(mode domain.LaunchMode, missionAssigned bool, status domain.RunStatus, reason string) bool {
	return (mode.Interactive() && retainedReason(status, reason)) ||
		(missionAssigned && reason == retainedCompletionReason)
}

// releasedReason is the reason a retained run keeps once its container is
// gone, and whether the row needs it.
func releasedReason(status domain.RunStatus, reason, closed string) (string, bool) {
	switch {
	case status == domain.RunCompleted && reason == reportedSuccessRetainedReason:
		return reportedSuccessReason, true
	case status == domain.RunFailed && reason == reportedFailureRetainedReason:
		return reportedFailureReason, true
	case (status == domain.RunCompleted || status == domain.RunFailed) && reason == retainedCompletionReason:
		return "worker finished", true
	case status == domain.RunMerged || status == domain.RunAbandoned:
		return closed, true
	}
	return "", false
}

// relabelLocked rewrites a terminal run's reason without changing its status.
// actor is the member whose close or kill caused it, if any: their act opens
// the finish in the same write. The caller must hold s.mu, which Seen also
// holds, so the outcome_unseen and finish_unopened flags read here are the
// ones this event reports.
func (s *Scheduler) relabelLocked(ctx context.Context, run domain.RunID, workspace domain.WorkspaceID, status domain.RunStatus, reason string, actor domain.MemberID) error {
	if !status.Terminal() {
		return fmt.Errorf("%w: relabel %s", ErrInvalidTransition, status)
	}
	public := publicRunStatusReason(reason)
	write := s.cfg.Store.UpdateRunStatus
	if memberCause(actor) == causeMember {
		write = s.cfg.Store.FinishRunByMember
	}
	if err := write(ctx, run, status, public, nil, nil); err != nil {
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
		Payload:     events.RunStatusPayload{From: status, To: status, Reason: public, OutcomeUnseen: row.OutcomeUnseen, FinishUnopened: row.FinishUnopened},
	})
	return nil
}

// dismissFinished opens the finish of a run a member closed or killed when
// that act changed no status: the run had already finished, and the member
// has still dealt with it. A finish their act already opened publishes
// nothing, so each act tells clients once.
func (s *Scheduler) dismissFinished(ctx context.Context, run domain.RunID, actor domain.MemberID) error {
	if memberCause(actor) != causeMember {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	opened, err := s.cfg.Store.ClearRunFinishUnopened(ctx, run)
	if err != nil || !opened {
		return err
	}
	row, err := s.cfg.Store.GetRun(ctx, run)
	if err != nil {
		return err
	}
	s.publish(ctx, events.Event{
		WorkspaceID: row.WorkspaceID,
		RunID:       run,
		ActorID:     actor,
		Payload:     events.RunFinishOpenedPayload{},
	})
	return nil
}

// Seen records that actor opened a run: it clears finish_unopened, and
// outcome_unseen too when actor owns the run. One call publishes at most one
// event; run.outcome_seen covers both flags. s.mu orders the clears and
// their event against the run.status events that also carry the flags.
func (s *Scheduler) Seen(ctx context.Context, run domain.RunID, actor domain.MemberID) (*domain.Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	outcomeSeen, err := s.cfg.Store.ClearRunOutcomeUnseen(ctx, run, actor)
	if err != nil {
		return nil, err
	}
	opened, err := s.cfg.Store.ClearRunFinishUnopened(ctx, run)
	if err != nil {
		return nil, err
	}
	fresh, err := s.cfg.Store.GetRun(ctx, run)
	if err != nil {
		return nil, err
	}
	switch {
	case outcomeSeen:
		s.publish(ctx, events.Event{
			WorkspaceID: fresh.WorkspaceID,
			RunID:       run,
			ActorID:     actor,
			Payload:     events.RunOutcomeSeenPayload{},
		})
		s.publishTimeline(ctx, fresh.WorkspaceID, run, actor, events.TimelineNote, "outcome seen by owner")
	case opened:
		s.publish(ctx, events.Event{
			WorkspaceID: fresh.WorkspaceID,
			RunID:       run,
			ActorID:     actor,
			Payload:     events.RunFinishOpenedPayload{},
		})
	}
	return fresh, nil
}

// FinishReported settles an interactive run at its turn boundary without
// ending its execution lifetime. Background runs retain automatic completion;
// assigned workers are left to CompleteMission. The report watermark survives
// follow-up work so outbox retries cannot resurrect a cleared outcome.
func (s *Scheduler) FinishReported(ctx context.Context, run domain.RunID, reportID string, outcome domain.RunStatus, reportedAt time.Time) error {
	if outcome != domain.RunCompleted && outcome != domain.RunFailed {
		return fmt.Errorf("%w: reported outcome must be completed or failed, got %q", ErrInvalidTransition, outcome)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.runs[run]
	if entry == nil || entry.status.Terminal() {
		return s.overrideExitLocked(ctx, run, reportID, outcome)
	}
	if reportedAt.Before(entry.relaunchedAt) {
		return nil
	}
	if entry.missionAssigned {
		r, err := s.cfg.Store.GetRun(ctx, run)
		if err != nil {
			return fmt.Errorf("scheduler: finish reported run %s: %w", run, err)
		}
		if r.MissionRole != "integrator" {
			return nil
		}
	}
	if superseded, err := s.reportSuperseded(ctx, reportID); err != nil || superseded {
		return err
	}
	if entry.launchMode.Interactive() {
		return s.recordIdleReportLocked(ctx, entry, reportID, reportedClose(outcome).reason, reportedAt)
	}
	if entry.reported == outcome {
		return nil
	}
	prior, priorAt, idle, shown := entry.reported, entry.reportedAt, entry.idleReason, entry.idleShown
	entry.reported, entry.reportedAt = outcome, time.Now().UTC()
	entry.idleReason, entry.idleShown = "", false
	if err := s.writeSidecar(entry.sidecar()); err != nil {
		entry.reported, entry.reportedAt, entry.idleReason, entry.idleShown = prior, priorAt, idle, shown
		return fmt.Errorf("scheduler: arm reported finish for %s: %w", run, err)
	}
	// The hand-off can trail the turn end it belongs to - the agent
	// reports, then stops - and no further turn-end wait will come.
	if entry.turnEnded() && !entry.atPrompt() {
		s.startReportedFinishLocked(entry)
	}
	return nil
}

// reportSuperseded fences an outbox hand-off loaded before a newer reservation.
// Direct lifecycle callers may not have a coordination row.
func (s *Scheduler) reportSuperseded(ctx context.Context, id string) (bool, error) {
	reports, ok := s.cfg.Store.(store.CoordTerminalReportStore)
	if !ok {
		return false, nil
	}
	report, err := reports.GetCoordReport(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("scheduler: read reported outcome %s: %w", id, err)
	}
	return report.SupersededAt != nil, nil
}

func reportedIdleReason(reason string) bool {
	return reason == reportedSuccessReason || reason == reportedFailureReason
}

func idleCause(reason string) finishCause {
	if reportedIdleReason(reason) {
		return causeReported
	}
	return causeUnattended
}

// recordIdleReportLocked shares the blocked-report watermark and park machinery
// with interactive outcomes. No native execution report is synthesized.
func (s *Scheduler) recordIdleReportLocked(ctx context.Context, entry *supervised, id, reason string, at time.Time) error {
	if entry.killRequested || entry.finalizing || entry.destroyPending ||
		entry.idleReportID == id || !at.After(entry.idleReportAt) || at.Before(entry.relaunchedAt) {
		return nil
	}
	old := entry.sidecar()
	entry.idleReason, entry.idleShown = reason, false
	entry.idleReportID, entry.idleReportAt = id, at
	if err := s.writeSidecar(entry.sidecar()); err != nil {
		entry.idleReason, entry.idleShown = old.IdleReason, old.IdleShown
		entry.idleReportID = old.IdleReportID
		entry.idleReportAt = time.Time{}
		if old.IdleReportAt != nil {
			entry.idleReportAt = *old.IdleReportAt
		}
		return fmt.Errorf("scheduler: record idle report for %s: %w", entry.runID, err)
	}
	if entry.turnEnded() && !entry.atPrompt() {
		return s.parkIdleReportLocked(ctx, entry)
	}
	return nil
}

// parkIdleReportLocked never pauses the runtime or stops its adapter. Persist
// the shown marker before the row; a retry repairs a crash between those writes.
func (s *Scheduler) parkIdleReportLocked(ctx context.Context, entry *supervised) error {
	if entry.idleReason == "" || entry.atPrompt() || entry.status.Terminal() ||
		entry.killRequested || entry.finalizing || entry.destroyPending {
		return nil
	}
	shown := entry.idleShown
	entry.idleShown = true
	if err := s.writeSidecar(entry.sidecar()); err != nil {
		entry.idleShown = shown
		return fmt.Errorf("scheduler: persist idle outcome: %w", err)
	}
	if err := s.transitionOutcomeLocked(ctx, entry.runID, entry.workspaceID, entry.status,
		domain.RunNeedsAttention, entry.idleReason, "", idleCause(entry.idleReason)); err != nil {
		entry.idleShown = shown
		return errors.Join(err, s.writeSidecar(entry.sidecar()))
	}
	entry.parkedAt, entry.postParkActivity = time.Now().UTC(), time.Time{}
	return nil
}

// overrideExitLocked records report reportID's outcome on a run whose process
// exit already wrote the row. The agent's report outranks its exit code, as in
// finalize; that is the only reason this skips legalTransition (which refuses
// failed -> completed), so it only rewrites an ordinary run's exit-written row.
// The already-published commit keeps its exit prefix; rewriting history would
// be worse. Relaunch supersedes the terminal report under s.mu, so an
// unsuperseded report belongs to the launch whose exit wrote the row. The
// caller must hold s.mu.
func (s *Scheduler) overrideExitLocked(ctx context.Context, run domain.RunID, reportID string, outcome domain.RunStatus) error {
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
	if !exited || r.MissionID != "" {
		return nil
	}
	if reports, ok := s.cfg.Store.(store.CoordTerminalReportStore); ok {
		report, err := reports.GetCoordReport(ctx, reportID)
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("scheduler: finish reported run %s: read report %s: %w", run, reportID, err)
		}
		if report.SupersededAt != nil {
			return nil
		}
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
		Payload:     events.RunStatusPayload{From: r.Status, To: outcome, Reason: reason, OutcomeUnseen: true, FinishUnopened: true},
	})
	return nil
}

// ReportBlocked records a nonterminal outcome at its settled turn boundary.
// Same-turn Working callbacks do not erase it; the first resume after it is
// shown does. The shared watermark fences replays, older reports and relaunches.
func (s *Scheduler) ReportBlocked(ctx context.Context, run domain.RunID, reportID, summary string, reportedAt time.Time) error {
	reason := blockedReasonPrefix + summary
	if runes := []rune(reason); len(runes) > maxPublicRunStatusReason {
		reason = string(runes[:maxPublicRunStatusReason])
	}
	s.mu.Lock()
	if entry := s.runs[run]; entry != nil {
		defer s.mu.Unlock()
		if entry.status.Terminal() || entry.reported != "" {
			return nil
		}
		return s.recordIdleReportLocked(ctx, entry, reportID, reason, reportedAt)
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

// startReportedFinishLocked finishes an armed run off the caller's path so
// the triggering hook or poll returns first. The caller must hold s.mu.
func (s *Scheduler) startReportedFinishLocked(entry *supervised) {
	if entry.reportFinishing || s.superCtx.Err() != nil {
		return
	}
	entry.reportFinishing = true
	s.wg.Add(1)
	go s.finishReported(entry)
}

// finishOverdueReports settles outcomes whose native turn end cannot arrive.
// Input requests always fence settlement. Interactive reports only park; a
// reporting harness that is still working is never cut off by the deadline.
func (s *Scheduler) finishOverdueReports() {
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, entry := range s.runs {
		if entry.status.Terminal() || entry.atPrompt() {
			continue
		}
		ready := entry.reporter == harness.ReporterNone || entry.turnEnded() || entry.status == domain.RunNeedsAttention
		if entry.reported != "" && ready && now.Sub(entry.reportedAt) >= reportFinishDeadline {
			s.startReportedFinishLocked(entry)
		}
		if entry.idleReason != "" && !entry.idleShown &&
			(entry.turnEnded() || (reportedIdleReason(entry.idleReason) && ready &&
				now.Sub(entry.idleReportAt) >= reportFinishDeadline)) {
			if err := s.parkIdleReportLocked(s.superCtx, entry); err != nil {
				slog.Warn("scheduler: park reported outcome", "run", entry.runID, "error", err)
			}
		}
	}
}

// atPrompt reports whether a permission or question parks the run mid-turn.
// The caller must hold s.mu.
func (e *supervised) atPrompt() bool {
	return len(e.pendingInputs) != 0
}

// turnEnded must be called with s.mu held.
func (e *supervised) turnEnded() bool {
	return turnEnd(e.agentReport)
}

// turnEnd excludes an idle report for a failed turn, which parks the run
// with its own reason.
func turnEnd(report agentstatus.Report) bool {
	return report.State == agentstatus.Idle && report.Reason == agentstatus.ReasonIdle
}

// finishReported closes an armed run like a human close would. A human close,
// a kill, or a process exit that got there first decides the run instead.
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

// finishReportedLocked runs under entry.lifecycleMu. A failed close restarts
// reportFinishDeadline so the retry waits for it rather than every poll.
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
	mode, paused, assigned := entry.launchMode, entry.paused, entry.missionAssigned
	s.mu.Unlock()

	if err := s.closeLiveLocked(ctx, entry, status, workspace, cid, mode, paused, assigned, reportedClose(outcome)); err != nil {
		s.mu.Lock()
		entry.reportedAt = time.Now().UTC()
		s.mu.Unlock()
		return fmt.Errorf("close with reported %s: %w", outcome, err)
	}
	return nil
}
