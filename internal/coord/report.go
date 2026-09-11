package coord

import (
	"context"
	"strings"
	"unicode"

	"github.com/3xDevOps/Aether/internal/agentstatus"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// maxReportReason bounds the user-visible reason a report carries. It is
// the ceiling the scheduler already applies to every run status reason, so
// a report cannot put a longer one on a run card than a stall can.
const maxReportReason = 256

// ReportSink is where run.report lands: the scheduler, which is the single
// writer of run statuses. A report is not part of the mailbox, so it is
// neither rate-limited nor authorized against the radar - it is one cheap
// call the harness's own hooks make on every turn boundary.
type ReportSink interface {
	ReportAgentState(ctx context.Context, run domain.RunID, report agentstatus.Report) error
}

// Report answers run.report for run: the agent behind this socket says it
// is working, or that it needs its member and why.
func (s *Service) Report(ctx context.Context, run domain.RunID, p protocol.RunReportParams) (protocol.RunReportResult, *protocol.Error) {
	if s.cfg.Disabled {
		return protocol.RunReportResult{}, unavailable(protocol.MethodRunReport)
	}
	state, ok := agentstatus.ParseState(p.State)
	if !ok {
		return protocol.RunReportResult{}, invalidParams(protocol.MethodRunReport,
			"state must be \""+string(agentstatus.Working)+"\" or \""+string(agentstatus.Waiting)+"\"")
	}
	if s.cfg.Reports == nil {
		return protocol.RunReportResult{}, internalError(protocol.MethodRunReport, ErrNoReportSink)
	}
	report := agentstatus.Report{State: state, Reason: reportReason(p.Reason)}
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
