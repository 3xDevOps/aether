package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/store"
)

// reportFinishDeadline is how long an armed run waits for its agent's turn
// to end before the poll loop finishes it anyway: a harness with no status
// reporter never says the turn ended, and an agent may keep going.
const reportFinishDeadline = 2 * time.Minute

const (
	reportedSuccessReason         = "agent reported success"
	reportedFailureReason         = "agent reported failure"
	reportedSuccessRetainedReason = reportedSuccessReason + "; retained container"
	reportedFailureRetainedReason = reportedFailureReason + "; retained container"
	blockedReasonPrefix           = "blocked: "
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
		return closeSpec{outcome: outcome, reason: reportedSuccessReason, retained: reportedSuccessRetainedReason, commit: "aether: "}
	}
	return closeSpec{outcome: outcome, reason: reportedFailureReason, retained: reportedFailureRetainedReason, commit: "wip: "}
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
// The caller must hold s.mu.
func (s *Scheduler) relabelLocked(ctx context.Context, run domain.RunID, workspace domain.WorkspaceID, status domain.RunStatus, reason string, actor domain.MemberID) error {
	if !status.Terminal() {
		return fmt.Errorf("%w: relabel %s", ErrInvalidTransition, status)
	}
	public := publicRunStatusReason(reason)
	if err := s.cfg.Store.UpdateRunStatus(ctx, run, status, public, nil, nil); err != nil {
		return err
	}
	s.publish(ctx, events.Event{
		WorkspaceID: workspace,
		RunID:       run,
		ActorID:     actor,
		Payload:     events.RunStatusPayload{From: status, To: status, Reason: public},
	})
	return nil
}

// FinishReported arms a live run to finish with the outcome its agent
// reported through coord.report: completed for success, failed for failure.
// It only records the request; the run finishes on the agent's next waiting
// report, at reportFinishDeadline, or when the process exits, whichever is
// first. A run that is already terminal or gone needs nothing. A run with
// no live owner yet returns an error so the caller retries.
func (s *Scheduler) FinishReported(ctx context.Context, run domain.RunID, outcome domain.RunStatus) error {
	if outcome != domain.RunCompleted && outcome != domain.RunFailed {
		return fmt.Errorf("%w: reported outcome must be completed or failed, got %q", ErrInvalidTransition, outcome)
	}
	s.mu.Lock()
	if entry := s.runs[run]; entry != nil {
		defer s.mu.Unlock()
		if entry.status.Terminal() || entry.reported == outcome {
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
	s.mu.Unlock()
	return s.requireFinished(ctx, run, "finish reported run")
}

// ReportBlocked records the summary of the agent's blocked report. The next
// park - a waiting report or a stall - shows it as the needs-attention
// reason instead of the generic one, and the first resume after that park
// clears it. Parking right away would not survive: the report is made from
// inside a tool call, so a working report follows it.
func (s *Scheduler) ReportBlocked(ctx context.Context, run domain.RunID, summary string) error {
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
		blocked, shown := entry.blockedReason, entry.blockedShown
		entry.blockedReason, entry.blockedShown = reason, false
		if err := s.writeSidecar(entry.sidecar()); err != nil {
			entry.blockedReason, entry.blockedShown = blocked, shown
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

// finishOverdueReports finishes armed runs whose agent never ended its turn.
func (s *Scheduler) finishOverdueReports() {
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, entry := range s.runs {
		if entry.reported != "" && !entry.status.Terminal() && now.Sub(entry.reportedAt) >= reportFinishDeadline {
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
	defer func() {
		s.mu.Lock()
		entry.reportFinishing = false
		s.mu.Unlock()
	}()

	s.mu.Lock()
	outcome := entry.reported
	if s.runs[entry.runID] != entry || outcome == "" || entry.finalizing || entry.killRequested || entry.destroyPending {
		s.mu.Unlock()
		return
	}
	if entry.status.Terminal() {
		entry.reported = ""
		if err := s.writeSidecar(entry.sidecar()); err != nil {
			slog.Warn("scheduler: persist cleared reported outcome", "run", entry.runID, "error", err)
		}
		s.mu.Unlock()
		return
	}
	status, workspace, cid := entry.status, entry.workspaceID, entry.containerID
	mode, paused := entry.launchMode, entry.paused
	s.mu.Unlock()

	if err := s.closeLiveLocked(ctx, entry, status, workspace, cid, mode, paused, reportedClose(outcome)); err != nil {
		slog.Warn("scheduler: finish reported run", "run", entry.runID, "outcome", outcome, "error", err)
		// Retry after another deadline rather than on every poll.
		s.mu.Lock()
		entry.reportedAt = time.Now().UTC()
		s.mu.Unlock()
	}
}
