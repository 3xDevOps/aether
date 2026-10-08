package coord

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode"

	"github.com/3xDevOps/Aether/internal/agentstatus"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

// maxReportReason bounds the user-visible reason a report carries. It is
// the ceiling the scheduler already applies to every run status reason, so
// a report cannot put a longer one on a run card than a stall can.
const maxReportReason = 256

// ReportSink is where run.report lands: the scheduler, which is the single
// writer of run statuses. Lifecycle traffic has its own bounded transport
// budget and is not authorized against the radar.
type ReportSink interface {
	ReportAgentState(ctx context.Context, run domain.RunID, report agentstatus.Report) error
}

// OutcomeSink receives an ordinary run's published coord.report. Success and
// failure park an interactive run or finish a background run; a blocked summary
// becomes its needs-attention reason. reportedAt and reportID let the scheduler
// reject stale or replayed reports independently of native execution updates.
// A run that is already terminal or gone is not an error; any error leaves the
// publication pending for retry.
type OutcomeSink interface {
	FinishReported(ctx context.Context, run domain.RunID, reportID string, outcome domain.RunStatus, reportedAt time.Time) error
	ReportBlocked(ctx context.Context, run domain.RunID, reportID, summary string, reportedAt time.Time) error
}

// applyRunOutcome hands a run's report to the scheduler before the
// publication is marked done, so the outbox retries a failed hand-off.
// Mission integrators use the ordinary outcome path; workers keep their mission
// lifecycle. A superseded report remains evidence but no longer speaks for the run.
func (s *Service) applyRunOutcome(ctx context.Context, report *store.CoordReport) error {
	if s.cfg.Outcomes == nil || report.SupersededAt != nil {
		return nil
	}
	current, err := s.cfg.Mail.GetCoordReport(ctx, report.ID)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("report outcome lookup: %w", err)
	}
	if current.SupersededAt != nil {
		return nil
	}
	if s.cfg.Mission != nil {
		assignment, assignmentErr := s.cfg.Mission.Assignment(ctx, report.RunID)
		if errors.Is(assignmentErr, store.ErrNotFound) || errors.Is(assignmentErr, store.ErrMissionStale) {
			return nil
		}
		if assignmentErr != nil {
			return fmt.Errorf("report outcome assignment: %w", assignmentErr)
		}
		if assignment.Role == "worker" {
			return nil
		}
	}
	reportedAt := *report.FinalizedAt
	switch report.Outcome {
	case store.CoordOutcomeSuccess:
		err = s.cfg.Outcomes.FinishReported(ctx, report.RunID, report.ID, domain.RunCompleted, reportedAt)
	case store.CoordOutcomeFailure:
		err = s.cfg.Outcomes.FinishReported(ctx, report.RunID, report.ID, domain.RunFailed, reportedAt)
	case store.CoordOutcomeBlocked:
		err = s.cfg.Outcomes.ReportBlocked(ctx, report.RunID, report.ID, reportReason(report.Summary), reportedAt)
	}
	if err != nil {
		return fmt.Errorf("report outcome: %w", err)
	}
	return nil
}

// Report answers run.report for run with independent execution and input updates.
func (s *Service) Report(ctx context.Context, run domain.RunID, p protocol.RunReportParams) (protocol.RunReportResult, *protocol.Error) {
	const method = protocol.MethodRunReport
	if s.cfg.Disabled {
		return protocol.RunReportResult{}, unavailable(method)
	}
	if !s.enterRun(run) {
		return protocol.RunReportResult{}, runClosing(method)
	}
	defer s.leaveRun(run)
	if err := domain.ValidateRunInputUpdates(p.InputUpdates); err != nil {
		return protocol.RunReportResult{}, invalidParams(method, err.Error())
	}
	var state agentstatus.State
	if p.State != "" || len(p.InputUpdates) == 0 {
		var ok bool
		state, ok = agentstatus.ParseState(p.State)
		if !ok {
			return protocol.RunReportResult{}, invalidParams(method,
				"state must be \""+string(agentstatus.Working)+"\" or \""+string(agentstatus.Idle)+"\", or omitted with input_updates")
		}
	}
	if s.cfg.Reports == nil {
		return protocol.RunReportResult{}, internalError(protocol.MethodRunReport, ErrNoReportSink)
	}
	report := agentstatus.Report{State: state, Reason: reportReason(p.Reason), InputUpdates: p.InputUpdates, SessionID: p.SessionID}
	if report.SessionID != "" && !domain.ValidAgentSessionID(report.SessionID) {
		slog.Warn("coord: run.report carries an unusable session id; ignoring it", "run", run, "bytes", len(report.SessionID))
		report.SessionID = ""
	}
	if err := s.cfg.Reports.ReportAgentState(ctx, run, report); err != nil {
		return protocol.RunReportResult{}, internalError(protocol.MethodRunReport, err)
	}
	return protocol.RunReportResult{}, nil
}

// reportReason makes the agent's reason safe to render: the reason reaches
// a run card, a terminal listing and the event stream, and everything on
// the far side of the socket is only semi-trusted. Control bytes - which a
// TUI would act on - become spaces, and the whole thing is capped at the
// same length a stall reason is.
func reportReason(reason string) string {
	reason = strings.Map(func(r rune) rune {
		if r == '\t' || r == '\n' || r == '\r' || !unicode.IsControl(r) {
			return r
		}
		return ' '
	}, reason)
	reason = strings.Join(strings.Fields(reason), " ")
	if runes := []rune(reason); len(runes) > maxReportReason {
		reason = string(runes[:maxReportReason])
	}
	return reason
}
