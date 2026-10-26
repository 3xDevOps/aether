package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/3xDevOps/Aether/internal/agentstatus"
	"github.com/3xDevOps/Aether/internal/coordhooks"
	"github.com/3xDevOps/Aether/internal/domain"
)

// heldStopGrace is how long a Standard turn whose stop was held may go
// without a hook before Aether applies the stop itself: the reporter may
// have lost the answer, leaving Claude Code stopped with nothing reported.
var heldStopGrace = time.Minute

// HoldTurnEnd returns the reminder for the turn run's agent is ending when
// it owes a report: the run takes reports, and none arrived since the turn
// started. It holds each turn at most once, so the caller asks the agent for
// the report once, and returns "" otherwise. A Background run is never
// held: its turn end finishes it.
func (s *Scheduler) HoldTurnEnd(ctx context.Context, run domain.RunID) (string, error) {
	c := s.coordinationSeam()
	s.mu.Lock()
	entry := s.runs[run]
	if c == nil || !c.enabled || entry == nil || !entry.reportOwed() {
		s.mu.Unlock()
		return "", nil
	}
	started, assigned := entry.turnStartedAt, entry.missionAssigned
	s.mu.Unlock()
	last, err := s.cfg.Store.LastCoordReportAt(ctx, run)
	if err != nil {
		return "", fmt.Errorf("scheduler: hold the turn end of %s: %w", run, err)
	}
	if !last.Before(started) {
		return "", nil
	}
	reminder := coordhooks.ReportReminder(s.runGuide(ctx, run, assigned, false, true))
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.runs[run] != entry || !entry.turnStartedAt.Equal(started) || !entry.reportOwed() {
		return "", nil
	}
	entry.reportAsked = started
	if entry.launchMode == domain.LaunchTUI {
		heldAt := time.Now().UTC()
		time.AfterFunc(heldStopGrace, func() { s.applyHeldStop(entry, started, heldAt) })
	}
	return reminder, nil
}

// applyHeldStop ends a held Standard turn that sent no hook since heldAt.
func (s *Scheduler) applyHeldStop(entry *supervised, started, heldAt time.Time) {
	s.mu.Lock()
	quiet := s.runs[entry.runID] == entry && entry.turnStartedAt.Equal(started) && entry.reportAsked.Equal(started) &&
		entry.agentReport.State == agentstatus.Working && !entry.lastWorking.After(heldAt)
	s.mu.Unlock()
	if !quiet || s.superCtx.Err() != nil {
		return
	}
	if err := s.ReportAgentState(s.superCtx, entry.runID, agentstatus.Report{State: agentstatus.Idle, Reason: agentstatus.ReasonIdle}); err != nil {
		slog.Warn("scheduler: end a held turn that went quiet", "run", entry.runID, "error", err)
	}
}

// reportOwed reports whether the run's current turn may be held for its
// report. A pending request already waits on a member. The caller must hold
// s.mu.
func (e *supervised) reportOwed() bool {
	return e.coordDir != "" && e.launchMode.Interactive() && !e.turnStartedAt.IsZero() && !e.reportAsked.Equal(e.turnStartedAt) &&
		(e.status == domain.RunRunning || e.status == domain.RunNeedsAttention) && e.reported == "" && !e.atPrompt() &&
		!e.killRequested && !e.finalizing && !e.destroyPending && !e.exitObserved
}
