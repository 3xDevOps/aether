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
// A paused run is no exception. Its container is frozen at the prompt the
// report came from and cannot repeat it, so holding the report back would
// leave the run reading Working with nothing left to correct it: silence
// from a container the member froze is not a stall either.
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
	switch report.State {
	case agentstatus.Waiting:
		// needs-attention -> needs-attention is legal and is the point: a
		// run parked by a stall, or waiting for a different thing, gets the
		// reason the agent is actually waiting for. Saying again what the
		// run already says is not news, though - Claude Code reports one
		// wait twice, as the turn ends and again once it has been idle for
		// a minute - so that costs nothing.
		if entry.status == domain.RunNeedsAttention && entry.agentReport == report {
			return nil
		}
		if err := s.transitionLocked(ctx, run, entry.workspaceID, entry.status,
			domain.RunNeedsAttention, report.Reason, ""); err != nil {
			return err
		}
	case agentstatus.Working:
		// Claude Code fires this on every tool call, so the common case has
		// to be free: a run that is already running is left alone, with no
		// store write and no event.
		if entry.status == domain.RunNeedsAttention {
			if err := s.transitionLocked(ctx, run, entry.workspaceID, entry.status,
				domain.RunRunning, agentstatus.ReasonResumed, ""); err != nil {
				return err
			}
		}
	}
	entry.agentReport = report
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
