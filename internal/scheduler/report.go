package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/agentstatus"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
)

// ReportAgentState accepts execution and correlated input independently. An
// input-only report preserves the last execution report, including an idle hold.
// Sidecar persistence precedes any observable change; no input delta is saved as
// the last execution report or replayed on recovery.
//
// Interactive outcomes share blocked reports' nonterminal park: only a real
// turn end with no pending input shows the reason, and only Working after
// that park clears it. Background outcomes instead start their terminal finish.
func (s *Scheduler) ReportAgentState(ctx context.Context, run domain.RunID, report agentstatus.Report) error {
	newSession, err := s.applyAgentReport(ctx, run, report)
	if newSession != nil {
		s.recordAgentSession(ctx, newSession)
	}
	return err
}

// recordAgentSession stores the run's latest reported session. sessionMu
// keeps a slower write of an older session from landing last.
func (s *Scheduler) recordAgentSession(ctx context.Context, entry *supervised) {
	entry.sessionMu.Lock()
	defer entry.sessionMu.Unlock()
	s.mu.Lock()
	session := entry.agentSessionID
	s.mu.Unlock()
	if err := s.cfg.Store.SetRunAgentSession(ctx, entry.runID, session); err != nil {
		slog.Warn("scheduler: record the agent's session", "run", entry.runID, "error", err)
	}
}

// applyAgentReport returns the run's entry when its agent reported a new
// session, for the caller to record outside the scheduler lock.
func (s *Scheduler) applyAgentReport(ctx context.Context, run domain.RunID, report agentstatus.Report) (*supervised, error) {
	if report.State != agentstatus.Working && report.State != agentstatus.Idle &&
		(report.State != "" || len(report.InputUpdates) == 0) {
		return nil, fmt.Errorf("scheduler: invalid agent execution state %q", report.State)
	}
	if err := domain.ValidateRunInputUpdates(report.InputUpdates); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.runs[run]
	if entry == nil {
		return nil, fmt.Errorf("scheduler: agent status report for %s: the run has no live container", run)
	}
	if (entry.status != domain.RunRunning && entry.status != domain.RunNeedsAttention) ||
		entry.exitObserved || entry.finalizing || entry.retained || entry.destroyPending {
		return nil, fmt.Errorf("scheduler: agent status report for %s: the run is no longer active (%s)", run, entry.status)
	}
	pending, err := reduceRunInputs(entry.pendingInputs, report.InputUpdates)
	if err != nil {
		return nil, err
	}
	inputsChanged := len(report.InputUpdates) != 0 && !slices.Equal(pending, entry.pendingInputs)
	execution := entry.agentReport
	if report.State != "" {
		execution = agentstatus.Report{State: report.State, Reason: report.Reason}
	}
	reportChanged := execution.State != entry.agentReport.State || execution.Reason != entry.agentReport.Reason
	finishes := entry.reported != "" && turnEnd(execution) && len(pending) == 0
	reason := report.Reason
	showsIdle, clearsIdle := false, false
	switch {
	case turnEnd(execution) && len(pending) == 0 && !finishes && entry.idleReason != "":
		reason = entry.idleReason
		showsIdle = !entry.idleShown
	case report.State == agentstatus.Working && entry.idleShown:
		clearsIdle = true
	}
	var oldSidecar sidecar
	persist := inputsChanged || reportChanged || showsIdle || clearsIdle
	if persist {
		oldSidecar = entry.sidecar()
		next := oldSidecar
		next.AgentState, next.AgentReason = execution.State, execution.Reason
		next.PendingInputs = pending
		next.InputPublishPending = entry.inputPublishPending || inputsChanged
		next.InputStartedAt = nil
		if len(pending) != 0 || next.InputPublishPending {
			started := entry.startedAt
			next.InputStartedAt = &started
		}
		if showsIdle {
			next.IdleShown = true
		}
		if clearsIdle {
			next.IdleReason, next.IdleShown = "", false
		}
		if err := s.writeSidecar(next); err != nil {
			return nil, fmt.Errorf("scheduler: persist agent report: %w", err)
		}
	}

	var transitionErr error
	switch {
	case report.State == agentstatus.Idle || showsIdle:
		if !finishes && (entry.status != domain.RunNeedsAttention || reportChanged || showsIdle) {
			cause := causeUnattended
			if showsIdle {
				cause = idleCause(reason)
			}
			transitionErr = s.transitionOutcomeLocked(ctx, run, entry.workspaceID, entry.status,
				domain.RunNeedsAttention, reason, "", cause)
		}
	case report.State == agentstatus.Working:
		if entry.status == domain.RunNeedsAttention {
			transitionErr = s.transitionLocked(ctx, run, entry.workspaceID, entry.status,
				domain.RunRunning, agentstatus.ReasonResumed, "")
		}
	}
	if transitionErr != nil {
		if showsIdle {
			// The native boundary is still true when parking its outcome
			// fails. Keep it durable so the poll/recovery path can retry
			// without requiring an idle agent to send another turn end.
			entry.agentReport = execution
			entry.pendingInputs = pending
			entry.inputPublishPending = entry.inputPublishPending || inputsChanged
			entry.parkedAt, entry.postParkActivity = time.Now().UTC(), time.Time{}
			return nil, errors.Join(transitionErr, s.writeSidecar(entry.sidecar()), s.publishPendingInputLocked(ctx, entry))
		}
		if persist {
			transitionErr = errors.Join(transitionErr, s.writeSidecar(oldSidecar))
		}
		return nil, transitionErr
	}
	switch report.State {
	case agentstatus.Working:
		// A repeated working report remains a no-write heartbeat for the stall
		// detector. Native hooks produce no terminal or file activity.
		entry.lastWorking = time.Now().UTC()
		entry.parkedAt, entry.postParkActivity = time.Time{}, time.Time{}
	case agentstatus.Idle:
		if !finishes {
			entry.parkedAt, entry.postParkActivity = time.Now().UTC(), time.Time{}
		}
	}
	entry.agentReport = execution
	if inputsChanged {
		entry.pendingInputs = pending
		entry.inputPublishPending = true
	}
	if showsIdle {
		entry.idleShown = true
		entry.parkedAt, entry.postParkActivity = time.Now().UTC(), time.Time{}
	}
	if clearsIdle {
		entry.idleReason, entry.idleShown = "", false
	}
	if finishes {
		s.startReportedFinishLocked(entry)
	}
	var newSession *supervised
	if s.recordReportedSessionLocked(entry, report.SessionID) {
		newSession = entry
	}
	return newSession, s.publishPendingInputLocked(ctx, entry)
}

// recordReportedSessionLocked keeps the latest session a Standard run's agent
// reports and reports whether it is new. An enhanced run's session id comes
// from its session host instead.
func (s *Scheduler) recordReportedSessionLocked(entry *supervised, session string) bool {
	if session == "" || entry.launchMode != domain.LaunchTUI || session == entry.agentSessionID {
		return false
	}
	entry.agentSessionID = session
	if err := s.writeSidecar(entry.sidecar()); err != nil {
		slog.Warn("scheduler: persist the agent's session", "run", entry.runID, "error", err)
	}
	return true
}

// publishPendingInputLocked repairs only an outstanding publication, never an
// ordinary duplicate report. The sidecar is the outbox for the current set;
// later reports supersede it rather than replaying stale deltas.
func (s *Scheduler) publishPendingInputLocked(ctx context.Context, entry *supervised) error {
	if !entry.inputPublishPending {
		return nil
	}
	if _, err := s.cfg.Bus.Publish(ctx, events.Event{
		WorkspaceID: entry.workspaceID,
		RunID:       entry.runID,
		Payload:     events.RunInputPayload{PendingInputs: inputList(entry.pendingInputs)},
	}); err != nil {
		return fmt.Errorf("scheduler: publish run input: %w", err)
	}
	next := entry.sidecar()
	next.InputPublishPending = false
	if len(next.PendingInputs) == 0 {
		next.InputStartedAt = nil
	}
	if err := s.writeSidecar(next); err != nil {
		return fmt.Errorf("scheduler: acknowledge run input publication: %w", err)
	}
	entry.inputPublishPending = false
	return nil
}

// PendingInputs decorates API snapshots from the current supervised lifetime.
// The caller owns the returned slice; no request can survive a terminal row.
func (s *Scheduler) PendingInputs(run domain.RunID) []domain.RunInputRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.runs[run]
	if entry == nil || entry.status.Terminal() || entry.exitObserved || entry.retained || entry.destroyPending {
		return []domain.RunInputRequest{}
	}
	return inputList(slices.Clone(entry.pendingInputs))
}

func inputList(inputs []domain.RunInputRequest) []domain.RunInputRequest {
	if inputs == nil {
		return []domain.RunInputRequest{}
	}
	return inputs
}

// reduceRunInputs never mutates the stored set. Small bounded sets keep exact
// identity matching simple; canonical order makes replacement order irrelevant.
func reduceRunInputs(current []domain.RunInputRequest, updates []domain.RunInputUpdate) ([]domain.RunInputRequest, error) {
	if len(updates) == 0 {
		return current, nil
	}
	next := slices.Clone(current)
	for _, update := range updates {
		request := domain.RunInputRequest{ID: update.ID, SessionID: update.SessionID, Kind: update.Kind}
		switch update.Operation {
		case "open":
			if !slices.Contains(next, request) {
				next = append(next, request)
			}
		case "close":
			if i := slices.Index(next, request); i >= 0 {
				next = slices.Delete(next, i, i+1)
			}
		case "clear":
			next = slices.DeleteFunc(next, func(request domain.RunInputRequest) bool { return request.SessionID == update.SessionID })
		case "replace":
			next = append(next[:0], update.Requests...)
		}
		slices.SortFunc(next, compareRunInputs)
		next = slices.Compact(next)
		if len(next) > domain.MaxRunInputRequests {
			return nil, fmt.Errorf("scheduler: pending input exceeds %d requests", domain.MaxRunInputRequests)
		}
	}
	return next, nil
}

func compareRunInputs(a, b domain.RunInputRequest) int {
	if order := strings.Compare(a.SessionID, b.SessionID); order != 0 {
		return order
	}
	if order := strings.Compare(a.Kind, b.Kind); order != 0 {
		return order
	}
	return strings.Compare(a.ID, b.ID)
}
