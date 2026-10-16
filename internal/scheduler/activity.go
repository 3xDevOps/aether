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
)

// waitingReasonPrefix marks a waiting outcome in the idle reason the sidecar
// keeps. The run shows the summary alone.
const waitingReasonPrefix = "waiting: "

// waitedFactor is how many stall thresholds a waiting outcome holds a run in
// waiting-agents with no activity before it shows idle.
const waitedFactor = 3

// activityFacts are what a live run's activity is derived from.
type activityFacts struct {
	status domain.RunStatus
	reason string
	// human is an open request for a human: a pending permission or
	// question, or a shown needs-input or blocked outcome.
	human        bool
	disconnected bool
	// turn is a turn in progress.
	turn          bool
	waitingAgents bool
	waitingTools  bool
}

// deriveActivity applies the precedence disconnected > needs-you > failed >
// finished > waiting-agents > waiting-tools > idle, where a turn in progress
// is working unless a human request is open.
func deriveActivity(f activityFacts) domain.RunActivity {
	switch {
	case !f.status.Live():
		return ""
	case f.disconnected:
		return domain.ActivityDisconnected
	case f.human:
		return domain.ActivityNeedsYou
	case f.turn:
		return domain.ActivityWorking
	case f.reason == reportedFailureReason:
		return domain.ActivityFailed
	case f.reason == reportedSuccessReason:
		return domain.ActivityFinished
	case f.waitingAgents:
		return domain.ActivityWaitingAgents
	case f.waitingTools:
		return domain.ActivityWaitingTools
	}
	return domain.ActivityIdle
}

func disconnectedReason(reason string) bool {
	return strings.HasPrefix(reason, acpFailedReason) || strings.HasPrefix(reason, acpEndedReason)
}

func humanReason(reason string) bool {
	return strings.HasPrefix(reason, needsInputReasonPrefix) || strings.HasPrefix(reason, blockedReasonPrefix)
}

// shownIdleReason is the run status reason an idle outcome shows.
func shownIdleReason(reason string) string {
	return strings.TrimPrefix(reason, waitingReasonPrefix)
}

// waits reports whether the run's outcome is a waiting report. The caller
// must hold s.mu.
func (e *supervised) waits() bool {
	return strings.HasPrefix(e.idleReason, waitingReasonPrefix)
}

// activityLocked derives the activity of run moving to status to with
// reason. entry is the run's supervised state, nil when it has none. Caller
// holds s.mu.
func (s *Scheduler) activityLocked(ctx context.Context, run domain.RunID, entry *supervised, to domain.RunStatus, reason string) domain.RunActivity {
	f := activityFacts{status: to, reason: reason}
	if !to.Live() {
		return ""
	}
	f.disconnected = disconnectedReason(reason)
	f.human = to == domain.RunNeedsAttention && humanReason(reason)
	if entry != nil {
		held := heldByTasks(entry.agentReport) && (!entry.acp || entry.betweenTurns)
		f.disconnected = f.disconnected || (entry.agentReport.State == agentstatus.Idle && disconnectedReason(entry.agentReport.Reason))
		f.human = f.human || entry.atPrompt() || (entry.idleShown && humanReason(entry.idleReason))
		f.waitingAgents = (entry.waits() && reason == shownIdleReason(entry.idleReason)) || (held && !entry.heldTools)
		f.waitingTools = held && entry.heldTools
	}
	f.turn = to == domain.RunRunning && !f.waitingAgents && !f.waitingTools
	if to == domain.RunNeedsAttention && !f.disconnected && !f.human && !f.waitingAgents &&
		(entry == nil || !entry.missionAssigned) {
		waiting, err := s.cfg.Store.IntegratorWaiting(ctx, run)
		if err != nil {
			slog.Warn("scheduler: read the integrator's workers", "run", run, "error", err)
		}
		f.waitingAgents = waiting
	}
	return deriveActivity(f)
}

// refreshActivityLocked rewrites a live run's activity when the facts it is
// derived from changed with no status change. Caller holds s.mu.
func (s *Scheduler) refreshActivityLocked(ctx context.Context, entry *supervised) error {
	if s.runs[entry.runID] != entry || !entry.status.Live() || entry.finalizing || entry.exitObserved {
		return nil
	}
	if s.activityLocked(ctx, entry.runID, entry, entry.status, entry.reason) == entry.activity {
		return nil
	}
	return s.transitionLocked(ctx, entry.runID, entry.workspaceID, entry.status, entry.status, entry.reason, "")
}

// refreshActivities recomputes every live run's activity: after recovery,
// and on each poll for facts no report announces, such as an integrator's
// last worker finishing.
func (s *Scheduler) refreshActivities(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, entry := range s.runs {
		if err := s.refreshActivityLocked(ctx, entry); err != nil {
			slog.Warn("scheduler: refresh run activity", "run", entry.runID, "error", err)
		}
	}
}

// expireWaitLocked shows a shown waiting outcome as idle, once nothing
// happened for waitedFactor stall thresholds (checkStalls). Caller holds s.mu.
func (s *Scheduler) expireWaitLocked(ctx context.Context, entry *supervised, quiet time.Duration) error {
	old := entry.sidecar()
	entry.idleReason, entry.idleShown = "", false
	if err := s.writeSidecar(entry.sidecar()); err != nil {
		entry.idleReason, entry.idleShown = old.IdleReason, old.IdleShown
		return fmt.Errorf("scheduler: persist the expired wait: %w", err)
	}
	if err := s.transitionLocked(ctx, entry.runID, entry.workspaceID, entry.status, entry.status,
		fmt.Sprintf("waited %s with no activity", quiet.Truncate(time.Second)), ""); err != nil {
		entry.idleReason, entry.idleShown = old.IdleReason, old.IdleShown
		return errors.Join(err, s.writeSidecar(old))
	}
	return nil
}
