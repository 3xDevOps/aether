package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/agentstatus"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
)

// These mark a waiting outcome in the idle reason the sidecar keeps, by what
// the agent waits for. The run shows the summary alone
// (publicRunStatusReason). A CI or process wait that went stale keeps its
// kind under a waited prefix, so it goes stale once.
const (
	waitingReasonPrefix        = "waiting: "
	waitingCIReasonPrefix      = "waiting for ci: "
	waitingProcessReasonPrefix = "waiting for process: "
	waitedCIReasonPrefix       = "waited for ci: "
	waitedProcessReasonPrefix  = "waited for process: "
)

var waitingPrefixes = []struct {
	prefix   string
	activity domain.RunActivity
	stale    bool
}{
	{waitingReasonPrefix, domain.ActivityWaitingAgents, false},
	{waitingCIReasonPrefix, domain.ActivityWaitingCI, false},
	{waitingProcessReasonPrefix, domain.ActivityWaitingTools, false},
	{waitedCIReasonPrefix, domain.ActivityWaitingCI, true},
	{waitedProcessReasonPrefix, domain.ActivityWaitingTools, true},
}

func cutWaiting(reason string) (domain.RunActivity, bool, string) {
	for _, w := range waitingPrefixes {
		if summary, ok := strings.CutPrefix(reason, w.prefix); ok {
			return w.activity, w.stale, summary
		}
	}
	return "", false, ""
}

// stalledReasonPrefix starts the reason checkStalls parks a quiet run with.
const stalledReasonPrefix = "stalled: "

// waitedReasonPrefix starts the reason of a run that waited for
// waitedFactor stall thresholds with no activity and nothing it waits on
// live, followed by ": " and the agent's summary when it reported one.
const waitedReasonPrefix = "no update for "

// workersReasonPattern matches an integrator's reason while it waits on its
// swarm with no waiting report, which replaces turnEndedReason.
var workersReasonPattern = regexp.MustCompile(`^(?:waiting for \d+ workers?|\d+ unread worker messages?)$`)

// waitedFactor is how many stall thresholds a run waits with no activity
// before its wait counts as stale.
const waitedFactor = 3

// turnEndedReason is the run's reason for a turn that ended with no outcome
// report (agentstatus.ReasonIdle, which status plugins still send).
const turnEndedReason = "turn ended without a report"

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
	waitingCI     bool
	waitingTools  bool
}

// deriveActivity applies the precedence disconnected > needs-you > failed >
// finished > waiting-agents > waiting-ci > waiting-tools > waiting, where a
// turn in progress is working unless a human request is open.
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
	case f.waitingCI:
		return domain.ActivityWaitingCI
	case f.waitingTools:
		return domain.ActivityWaitingTools
	}
	return domain.ActivityWaiting
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
	activity, _, _ := cutWaiting(e.idleReason)
	return activity != ""
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

// derived is a live run's activity, the public reason it shows, and what
// chose the activity. workers marks reason as the workers reason Aether
// wrote.
type derived struct {
	activity domain.RunActivity
	reason   string
	cause    events.RunStatusCause
	workers  bool
}

// activityLocked derives the activity of run moving to status to with
// reason, raw or public. entry is the run's supervised state, nil when it
// has none. Caller holds s.mu.
func (s *Scheduler) activityLocked(ctx context.Context, run domain.RunID, entry *supervised, to domain.RunStatus, reason string) (derived, error) {
	public := publicRunStatusReason(reason)
	if !to.Live() {
		return derived{reason: public}, nil
	}
	// A waiting summary is the agent's free text: none of it is read as a
	// reason Aether wrote.
	waiting, stale, _ := cutWaiting(strings.TrimSpace(reason))
	if waiting == "" && entry != nil && entry.waits() && public == publicRunStatusReason(entry.idleReason) {
		waiting, stale, _ = cutWaiting(entry.idleReason)
	}
	written := public
	if waiting != "" {
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
		waitingAgents: waiting == domain.ActivityWaitingAgents,
		waitingCI:     waiting == domain.ActivityWaitingCI,
		waitingTools:  waiting == domain.ActivityWaitingTools,
	}
	if entry != nil {
		held := !stalled && heldByTasks(entry.agentReport) && (!entry.acp || entry.betweenTurns)
		f.disconnected = f.disconnected || (entry.agentReport.State == agentstatus.Idle && disconnectedReason(entry.agentReport.Reason))
		f.human = f.human || entry.asksMember() || (entry.idleShown && !entry.waits() && humanReason(entry.idleReason)) ||
			(parked && entry.agentReport.State == agentstatus.Idle && entry.agentReport.UsageLimit)
		f.waitingAgents = !waitedOut && (f.waitingAgents || (held && !entry.agentReport.Tools))
		f.waitingCI = !waitedOut && f.waitingCI
		f.waitingTools = (!waitedOut && f.waitingTools) || (held && entry.agentReport.Tools)
	}
	f.turn = to == domain.RunRunning && !f.waitingAgents && !f.waitingCI && !f.waitingTools
	inferred, workers := false, false
	if parked && entry != nil && !stalled && !f.disconnected && !f.human && !f.failed && !f.finished &&
		!f.waitingAgents && !f.waitingCI && !f.waitingTools {
		swarm, err := s.cfg.Store.IntegratorWaiting(ctx, run, entry.turnStartedAt)
		if err != nil {
			return derived{}, fmt.Errorf("scheduler: read the integrator's workers: %w", err)
		}
		inferred = swarm.Live()
		f.waitingAgents = inferred
		ours := entry.workersReason != "" && written == entry.workersReason
		switch {
		case inferred && (written == turnEndedReason || ours):
			workers = true
			switch {
			case swarm.Workers == 1:
				public = "waiting for 1 worker"
			case swarm.Workers > 1:
				public = fmt.Sprintf("waiting for %d workers", swarm.Workers)
			case swarm.Unread == 1:
				public = "1 unread worker message"
			default:
				public = fmt.Sprintf("%d unread worker messages", swarm.Unread)
			}
		case !inferred && ours:
			public = turnEndedReason
		}
	}
	d := derived{activity: deriveActivity(f), reason: public, workers: workers}
	switch d.activity {
	case domain.ActivityDisconnected:
		d.cause = events.CauseDisconnect
	case domain.ActivityNeedsYou:
		d.cause = events.CauseHuman
	case domain.ActivityWorking:
		d.cause = events.CauseTurn
	case domain.ActivityFailed, domain.ActivityFinished:
		d.cause = events.CauseTurnEnd
		if written == reportedFailureReason || d.activity == domain.ActivityFinished {
			d.cause = events.CauseReport
		}
	case domain.ActivityWaitingAgents, domain.ActivityWaitingCI, domain.ActivityWaitingTools:
		switch {
		case inferred:
			d.cause = events.CauseWorkers
		case waiting == d.activity && stale:
			d.cause = events.CauseWaitExpired
		case waiting == d.activity:
			d.cause = events.CauseReport
		default:
			d.cause = events.CauseTasksHeld
		}
	default:
		switch {
		case stalled:
			d.cause = events.CauseStall
		case waitedOut:
			d.cause = events.CauseWaitExpired
		default:
			d.cause = events.CauseTurnEnd
		}
	}
	return d, nil
}

// refreshActivityLocked rewrites a live run's activity when the facts it is
// derived from changed with no status change. Caller holds s.mu.
func (s *Scheduler) refreshActivityLocked(ctx context.Context, entry *supervised) error {
	if s.runs[entry.runID] != entry || !entry.status.Live() || entry.finalizing || entry.exitObserved {
		return nil
	}
	// A shown wait's public reason is the agent's summary alone; deriving
	// from it would read the summary as a reason.
	reason := entry.reason
	if entry.waits() && publicRunStatusReason(entry.idleReason) == reason {
		reason = entry.idleReason
	}
	d, err := s.activityLocked(ctx, entry.runID, entry, entry.status, reason)
	if err != nil || (d.activity == entry.activity && d.reason == entry.reason) {
		return err
	}
	return s.transitionLocked(ctx, entry.runID, entry.workspaceID, entry.status, entry.status, reason, "")
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

// waitExpires reports whether a parked run shows a wait the agent reported
// that goes stale after waitedFactor stall thresholds with no activity and
// has not yet. A wait on its swarm or background tasks follows those
// (refreshActivities). Caller holds s.mu.
func (e *supervised) waitExpires() bool {
	activity, stale, _ := cutWaiting(e.idleReason)
	return activity != "" && !stale && e.activity == activity
}

// expireWaitLocked marks a run's wait stale (waitExpires) once nothing
// happened for waitedFactor stall thresholds (checkStalls), unless its
// background tasks or swarm are still live. The reason keeps the agent's
// summary; a CI or process wait keeps its state, any other shows waiting.
// Caller holds s.mu.
func (s *Scheduler) expireWaitLocked(ctx context.Context, entry *supervised, quiet time.Duration) error {
	if heldByTasks(entry.agentReport) {
		return nil
	}
	swarm, err := s.cfg.Store.IntegratorWaiting(ctx, entry.runID, entry.turnStartedAt)
	if err != nil {
		return fmt.Errorf("scheduler: read the integrator's workers: %w", err)
	}
	if swarm.Live() {
		return nil
	}
	reason := waitedReasonPrefix + span(quiet)
	old := entry.sidecar()
	if entry.waits() {
		kind, _, summary := cutWaiting(entry.idleReason)
		if summary != "" {
			reason += ": " + summary
		}
		entry.idleReason, entry.idleShown = "", false
		switch kind {
		case domain.ActivityWaitingCI:
			reason = waitedCIReasonPrefix + reason
			entry.idleReason, entry.idleShown = reason, true
		case domain.ActivityWaitingTools:
			reason = waitedProcessReasonPrefix + reason
			entry.idleReason, entry.idleShown = reason, true
		}
		if err := s.writeSidecar(entry.sidecar()); err != nil {
			entry.idleReason, entry.idleShown = old.IdleReason, old.IdleShown
			return fmt.Errorf("scheduler: persist the expired wait: %w", err)
		}
	}
	if err := s.transitionLocked(ctx, entry.runID, entry.workspaceID, entry.status, entry.status, reason, ""); err != nil {
		entry.idleReason, entry.idleShown = old.IdleReason, old.IdleShown
		return errors.Join(err, s.writeSidecar(old))
	}
	return nil
}

// span is d to the second, without a trailing "0s": 12m, 1h5m, 45s.
func span(d time.Duration) string {
	text := d.Truncate(time.Second).String()
	if trimmed, ok := strings.CutSuffix(text, "m0s"); ok {
		return trimmed + "m"
	}
	return text
}
