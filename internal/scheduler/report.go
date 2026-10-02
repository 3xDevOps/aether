package scheduler

import (
	"context"
	"errors"
	"fmt"
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
func (s *Scheduler) ReportAgentState(ctx context.Context, run domain.RunID, report agentstatus.Report) error {
	if report.State != agentstatus.Working && report.State != agentstatus.Idle &&
		(report.State != "" || len(report.InputUpdates) == 0) {
		return fmt.Errorf("scheduler: invalid agent execution state %q", report.State)
	}
	if err := domain.ValidateRunInputUpdates(report.InputUpdates); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.runs[run]
	if entry == nil {
		return fmt.Errorf("scheduler: agent status report for %s: the run has no live container", run)
	}
	if (entry.status != domain.RunRunning && entry.status != domain.RunNeedsAttention) ||
		entry.exitObserved || entry.finalizing || entry.retained || entry.destroyPending {
		return fmt.Errorf("scheduler: agent status report for %s: the run is no longer active (%s)", run, entry.status)
	}
	pending, err := reduceRunInputs(entry.pendingInputs, report.InputUpdates)
	if err != nil {
		return err
	}
	inputsChanged := len(report.InputUpdates) != 0 && !slices.Equal(pending, entry.pendingInputs)
	execution := entry.agentReport
	if report.State != "" {
		execution = agentstatus.Report{State: report.State, Reason: report.Reason}
	}
	reportChanged := execution.State != entry.agentReport.State || execution.Reason != entry.agentReport.Reason
	var oldSidecar sidecar
	if inputsChanged || reportChanged {
		oldSidecar = entry.sidecar()
		next := oldSidecar
		next.AgentState, next.AgentReason = execution.State, execution.Reason
		next.PendingInputs = pending
		next.InputStartedAt = nil
		if len(pending) != 0 {
			started := entry.startedAt
			next.InputStartedAt = &started
		}
		if err := s.writeSidecar(next); err != nil {
			return fmt.Errorf("scheduler: persist agent report: %w", err)
		}
	}

	var transitionErr error
	switch report.State {
	case agentstatus.Idle:
		if entry.status != domain.RunNeedsAttention || reportChanged {
			transitionErr = s.transitionLocked(ctx, run, entry.workspaceID, entry.status,
				domain.RunNeedsAttention, report.Reason, "")
		}
	case agentstatus.Working:
		if entry.status == domain.RunNeedsAttention {
			transitionErr = s.transitionLocked(ctx, run, entry.workspaceID, entry.status,
				domain.RunRunning, agentstatus.ReasonResumed, "")
		}
	}
	if transitionErr != nil {
		if inputsChanged || reportChanged {
			transitionErr = errors.Join(transitionErr, s.writeSidecar(oldSidecar))
		}
		return transitionErr
	}
	switch report.State {
	case agentstatus.Working:
		// A repeated working report remains a no-write heartbeat for the stall
		// detector. Native hooks produce no terminal or file activity.
		entry.lastWorking = time.Now().UTC()
		entry.parkedAt, entry.postParkActivity = time.Time{}, time.Time{}
	case agentstatus.Idle:
		entry.parkedAt, entry.postParkActivity = time.Now().UTC(), time.Time{}
	}
	entry.agentReport = execution
	if inputsChanged {
		entry.pendingInputs = pending
		// The sidecar remains authoritative if the durable event log fails.
		// Return the failure rather than claiming successful delivery.
		if _, err := s.cfg.Bus.Publish(ctx, events.Event{
			WorkspaceID: entry.workspaceID,
			RunID:       run,
			Payload:     events.RunInputPayload{PendingInputs: inputList(pending)},
		}); err != nil {
			return fmt.Errorf("scheduler: publish run input: %w", err)
		}
	}
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
