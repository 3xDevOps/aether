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
	"github.com/3xDevOps/Aether/internal/store"
)

// waitingReasonPrefix marks a waiting outcome in the idle reason the sidecar
// keeps. The run shows the summary alone (publicRunStatusReason).
const waitingReasonPrefix = "waiting: "

// stalledReasonPrefix starts the reason checkStalls parks a quiet run with.
const stalledReasonPrefix = "stalled: "

// waitedReasonPrefix starts the reason of a run that waited on its agents for
// waitedFactor stall thresholds with no activity. It shows idle until the
// reason changes.
const waitedReasonPrefix = "waited "

// waitedFactor is how many stall thresholds a run waits on its own agents
// with no activity before it shows idle.
const waitedFactor = 3

// activityFacts are what a live run's activity is derived from.
type activityFacts struct {
	status domain.RunStatus
	// human is an open request for a human: a pending permission, question
	// or approval, a shown needs-input or blocked outcome, a usage limit
	// Aether will not resume from, or an Enhanced turn interrupted or
	// stopped short.
	human        bool
	disconnected bool
	failed       bool
	finished     bool
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
	case f.failed:
		return domain.ActivityFailed
	case f.finished:
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

// humanReason reports a reason only a member can move on from. A turn
// cut off or stopped short waits for a message to continue; a cancelled one
// does not, since the member who cancelled it knows.
func humanReason(reason string) bool {
	return strings.HasPrefix(reason, needsInputReasonPrefix) || strings.HasPrefix(reason, blockedReasonPrefix) ||
		strings.HasPrefix(reason, usageLimitReason) || reason == acpInterruptedReason || strings.HasPrefix(reason, acpStoppedReason)
}

// failedReason reports a turn the agent could not finish, or its reported
// failure.
func failedReason(reason string) bool {
	return reason == reportedFailureReason || reason == agentstatus.ReasonFailed || strings.HasPrefix(reason, acpTurnFailedReason)
}

// waits reports whether the run's outcome is a waiting report. The caller
// must hold s.mu.
func (e *supervised) waits() bool {
	return strings.HasPrefix(e.idleReason, waitingReasonPrefix)
}

// asksMember reports whether a pending input request waits on a member. A
// Background run has no terminal to answer a question in. The caller must
// hold s.mu.
func (e *supervised) asksMember() bool {
	for _, request := range e.pendingInputs {
		if request.Kind == "permission" || e.launchMode != domain.LaunchHeadless {
			return true
		}
	}
	return false
}

// activityLocked derives the activity of run moving to status to with
// reason, raw or public. entry is the run's supervised state, nil when it
// has none. Caller holds s.mu.
func (s *Scheduler) activityLocked(ctx context.Context, run domain.RunID, workspace domain.WorkspaceID, entry *supervised, to domain.RunStatus, reason string) (domain.RunActivity, error) {
	if !to.Live() {
		return "", nil
	}
	// A waiting summary is the agent's free text: none of it is read as a
	// reason Aether wrote.
	waiting := strings.HasPrefix(strings.TrimSpace(reason), waitingReasonPrefix) ||
		(entry != nil && entry.waits() && publicRunStatusReason(reason) == publicRunStatusReason(entry.idleReason))
	written := publicRunStatusReason(reason)
	if waiting {
		written = ""
	}
	parked := to == domain.RunNeedsAttention
	stalled := strings.HasPrefix(written, stalledReasonPrefix)
	waitedOut := strings.HasPrefix(written, waitedReasonPrefix)
	f := activityFacts{
		status:        to,
		disconnected:  disconnectedReason(written),
		human:         parked && humanReason(written),
		failed:        parked && failedReason(written),
		finished:      parked && written == reportedSuccessReason,
		waitingAgents: waiting,
	}
	if entry != nil {
		held := !stalled && heldByTasks(entry.agentReport) && (!entry.acp || entry.betweenTurns)
		f.disconnected = f.disconnected || (entry.agentReport.State == agentstatus.Idle && disconnectedReason(entry.agentReport.Reason))
		f.human = f.human || entry.asksMember() || (entry.idleShown && !entry.waits() && humanReason(entry.idleReason)) ||
			(parked && entry.agentReport.State == agentstatus.Idle && entry.agentReport.UsageLimit)
		f.waitingAgents = !waitedOut && (waiting || (held && !entry.agentReport.Tools))
		f.waitingTools = held && entry.agentReport.Tools
	}
	if !f.human {
		approvals, err := s.cfg.Store.ListApprovals(ctx, workspace, store.ApprovalRequested)
		if err != nil {
			return "", fmt.Errorf("scheduler: read the run's approvals: %w", err)
		}
		for _, a := range approvals {
			f.human = f.human || a.RunID == run
		}
	}
	f.turn = to == domain.RunRunning && !f.waitingAgents && !f.waitingTools
	if parked && entry != nil && !stalled && !waitedOut && !f.disconnected && !f.human && !f.failed && !f.finished && !f.waitingAgents {
		waiting, err := s.cfg.Store.IntegratorWaiting(ctx, run, entry.turnStartedAt)
		if err != nil {
			return "", fmt.Errorf("scheduler: read the integrator's workers: %w", err)
		}
		f.waitingAgents = waiting
	}
	return deriveActivity(f), nil
}

// refreshActivityLocked rewrites a live run's activity when the facts it is
// derived from changed with no status change. Caller holds s.mu.
func (s *Scheduler) refreshActivityLocked(ctx context.Context, entry *supervised) error {
	if s.runs[entry.runID] != entry || !entry.status.Live() || entry.finalizing || entry.exitObserved {
		return nil
	}
	activity, err := s.activityLocked(ctx, entry.runID, entry.workspaceID, entry, entry.status, entry.reason)
	if err != nil || activity == entry.activity {
		return err
	}
	return s.transitionLocked(ctx, entry.runID, entry.workspaceID, entry.status, entry.status, entry.reason, "")
}

// refreshRunActivity recomputes run's activity after a fact outside the
// scheduler changed, such as an approval request raised or decided.
func (s *Scheduler) refreshRunActivity(ctx context.Context, run domain.RunID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.runs[run]
	if entry == nil {
		return nil
	}
	return s.refreshActivityLocked(ctx, entry)
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

// expireWaitLocked shows a run that waited on its own agents as idle, once
// nothing happened for waitedFactor stall thresholds (checkStalls), and
// drops a waiting outcome it showed. Caller holds s.mu.
func (s *Scheduler) expireWaitLocked(ctx context.Context, entry *supervised, quiet time.Duration) error {
	old := entry.sidecar()
	if entry.waits() {
		entry.idleReason, entry.idleShown = "", false
		if err := s.writeSidecar(entry.sidecar()); err != nil {
			entry.idleReason, entry.idleShown = old.IdleReason, old.IdleShown
			return fmt.Errorf("scheduler: persist the expired wait: %w", err)
		}
	}
	if err := s.transitionLocked(ctx, entry.runID, entry.workspaceID, entry.status, entry.status,
		fmt.Sprintf("%s%s with no activity", waitedReasonPrefix, quiet.Truncate(time.Second)), ""); err != nil {
		entry.idleReason, entry.idleShown = old.IdleReason, old.IdleShown
		return errors.Join(err, s.writeSidecar(old))
	}
	return nil
}
