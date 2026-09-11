package scheduler

import (
	"context"
	"fmt"

	"github.com/3xDevOps/Aether/internal/agentstatus"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/harness"
)

// ReportAgentState records what the agent behind a run just said about
// itself and moves the run between running and needs-attention to match.
// It is the coord.ReportSink the run's coordination socket forwards
// run.report to, so "the agent needs you" reaches the board the moment the
// agent says so instead of after a stall threshold of silence.
//
// The caller is a hook inside the run container that exits immediately
// either way, so an error here is diagnostic: it is returned with enough
// context to read in a transcript, never surfaced to the member.
func (s *Scheduler) ReportAgentState(ctx context.Context, run domain.RunID, report agentstatus.Report) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.runs[run]
	if entry == nil {
		return fmt.Errorf("scheduler: agent status report for %s: the run has no live container", run)
	}
	if entry.status != domain.RunRunning && entry.status != domain.RunNeedsAttention {
		return fmt.Errorf("scheduler: agent status report for %s: the run is %s", run, entry.status)
	}
	entry.agentState = report.State
	// A paused run is deliberately held where it is; the report is still
	// recorded so the stall detector knows why the agent is quiet.
	if entry.paused {
		return nil
	}
	switch report.State {
	case agentstatus.Waiting:
		// needs-attention -> needs-attention is legal and is the point: a
		// run parked by a stall, or waiting for a different thing, gets the
		// reason the agent is actually waiting for.
		return s.transitionLocked(ctx, run, entry.workspaceID, entry.status,
			domain.RunNeedsAttention, report.Reason, "")
	case agentstatus.Working:
		// Claude Code fires this on every tool call, so the common case has
		// to be free: a run that is already running is left alone, with no
		// store write and no event.
		if entry.status == domain.RunNeedsAttention {
			return s.transitionLocked(ctx, run, entry.workspaceID, entry.status,
				domain.RunRunning, agentstatus.ReasonResumed, "")
		}
	}
	return nil
}

// reporterFor is how much the harness a run was launched on can say about
// its own state. Recovery uses it to rebuild what the launch knew: an argv
// override drops the reporter exactly as it drops MCP registration, and a
// harness the registry never shipped has none to begin with.
func (s *Scheduler) reporterFor(name string) harness.Reporter {
	if _, overridden := s.harnesses[name]; overridden {
		return harness.ReporterNone
	}
	p, ok := harness.Lookup(name)
	if !ok {
		return harness.ReporterNone
	}
	return p.Reporter
}
