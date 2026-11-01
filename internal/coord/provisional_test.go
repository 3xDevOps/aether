package coord

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

func newProvisionalHarness(t *testing.T) (*coordHarness, domain.RunID, domain.RunID) {
	t.Helper()
	h := newHarness(t, 2, func(c *Config) { c.Evidence = &coordReportEvidenceCapture{id: "provisional-packet"} })
	child, parent := h.run(0), h.run(1)
	h.svc.cfg.Children = parentOf{child: child, parent: parent}
	return h, child, parent
}

func TestProvisionalReportForwardsTheLastMessageOncePerTurn(t *testing.T) {
	ctx := context.Background()
	h, child, parent := newProvisionalHarness(t)
	turn := time.Now().UTC()
	for range 2 {
		if err := h.svc.ProvisionalReport(ctx, child, turn, "Pushed PR #7."); err != nil {
			t.Fatalf("ProvisionalReport: %v", err)
		}
	}
	long := strings.Repeat("é", protocol.CoordMaxBodyBytes)
	if err := h.svc.ProvisionalReport(ctx, child, turn.Add(time.Second), long); err != nil {
		t.Fatalf("ProvisionalReport of a long message: %v", err)
	}
	inbox, rpcErr := h.svc.Inbox(ctx, parent, protocol.CoordInboxParams{})
	if rpcErr != nil || len(inbox.Messages) != 2 {
		t.Fatalf("inbox = %+v, %v; want one provisional report per turn", inbox, rpcErr)
	}
	first, clipped := inbox.Messages[0], inbox.Messages[1]
	if first.FromRunID != string(child) || first.Kind != string(store.RunMessageKindMessage) ||
		!strings.HasPrefix(first.CorrelationID, store.ProvisionalReportPrefix) ||
		first.Body != provisionalHeader+"Pushed PR #7." {
		t.Fatalf("provisional report = %+v", first)
	}
	if len(clipped.Body) > protocol.CoordMaxBodyBytes || !utf8.ValidString(clipped.Body) ||
		!strings.HasPrefix(clipped.Body, provisionalHeader+"é") {
		t.Fatalf("long provisional report has %d bytes, valid UTF-8 %v", len(clipped.Body), utf8.ValidString(clipped.Body))
	}
}

func TestProvisionalReportSkipsATurnThatReported(t *testing.T) {
	ctx := context.Background()
	h, child, parent := newProvisionalHarness(t)
	turn := time.Now().UTC()
	result, rpcErr := h.svc.CoordReport(ctx, child, protocol.CoordReportParams{
		Outcome: protocol.CoordOutcomeBlocked, Summary: "need the staging token", IdempotencyKey: "blocked",
	})
	if rpcErr != nil {
		t.Fatalf("report blocked: %v", rpcErr)
	}
	if err := h.svc.ProvisionalReport(ctx, child, turn, "Stopped for a token."); err != nil {
		t.Fatalf("ProvisionalReport: %v", err)
	}
	inbox, rpcErr := h.svc.Inbox(ctx, parent, protocol.CoordInboxParams{})
	if rpcErr != nil || len(inbox.Messages) != 1 || inbox.Messages[0].CorrelationID != result.ReportID {
		t.Fatalf("inbox = %+v, %v; want only the blocked report", inbox, rpcErr)
	}
	report, err := h.db.GetCoordReport(ctx, result.ReportID)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.svc.ProvisionalReport(ctx, child, report.CreatedAt.Add(time.Microsecond), "Next turn."); err != nil {
		t.Fatalf("ProvisionalReport of a later turn: %v", err)
	}
	if unread, err := h.db.CountUnackedRunMessages(ctx, parent); err != nil || unread != 2 {
		t.Fatalf("unread = %d, %v; want the later turn's provisional report too", unread, err)
	}
}

func TestReportSupersedesAnUnreadProvisionalReportInAFullInbox(t *testing.T) {
	ctx := context.Background()
	h, child, parent := newProvisionalHarness(t)
	for range protocol.CoordMaxUnread - 1 {
		if err := h.db.AppendRunMessage(ctx, &store.RunMessage{
			WorkspaceID: h.workspace, FromRun: child, ToRun: parent, Body: "earlier message",
		}, protocol.CoordMaxUnread); err != nil {
			t.Fatalf("fill inbox: %v", err)
		}
	}
	if err := h.svc.ProvisionalReport(ctx, child, time.Now().UTC(), "I think it works."); err != nil {
		t.Fatalf("ProvisionalReport: %v", err)
	}
	result, rpcErr := h.svc.CoordReport(ctx, child, protocol.CoordReportParams{
		Outcome: protocol.CoordOutcomeSuccess, Summary: "Tests pass on the branch.", IdempotencyKey: "success",
	})
	if rpcErr != nil {
		t.Fatalf("report success: %v", rpcErr)
	}
	key := "coord-report-success:" + result.ReportID + ":" + string(parent)
	if _, err := h.db.GetRunMessageByIdempotency(ctx, child, key); err != nil {
		t.Fatalf("the report did not reach the full inbox: %v", err)
	}
	if unread, err := h.db.CountUnackedRunMessages(ctx, parent); err != nil || unread != protocol.CoordMaxUnread {
		t.Fatalf("unread = %d, %v; want the report in place of the provisional report", unread, err)
	}
}

func TestAnEarlierReportKeepsALaterTurnsProvisionalReport(t *testing.T) {
	ctx := context.Background()
	h, child, parent := newProvisionalHarness(t)
	result, rpcErr := h.svc.CoordReport(ctx, child, protocol.CoordReportParams{
		Outcome: protocol.CoordOutcomeBlocked, Summary: "need the staging token", IdempotencyKey: "blocked",
	})
	if rpcErr != nil {
		t.Fatalf("report blocked: %v", rpcErr)
	}
	report, err := h.db.GetCoordReport(ctx, result.ReportID)
	if err != nil {
		t.Fatal(err)
	}
	if err = h.svc.ProvisionalReport(ctx, child, report.CreatedAt.Add(time.Microsecond), "Used the token."); err != nil {
		t.Fatalf("ProvisionalReport: %v", err)
	}
	if err = h.svc.enqueueChildReport(ctx, report); err != nil {
		t.Fatalf("replay the blocked report: %v", err)
	}
	if unread, err := h.db.CountUnackedRunMessages(ctx, parent); err != nil || unread != 2 {
		t.Fatalf("unread = %d, %v; want the blocked report and the later provisional report", unread, err)
	}
}

type parentOf struct {
	child, parent domain.RunID
}

func (p parentOf) Capabilities(context.Context, domain.RunID) ([]string, error) { return nil, nil }

func (p parentOf) HandleAgent(context.Context, domain.RunID, string, json.RawMessage) (any, error) {
	return nil, nil
}

func (p parentOf) Related(_ context.Context, a, b domain.RunID) (bool, error) {
	return a == p.child && b == p.parent || a == p.parent && b == p.child, nil
}

func (p parentOf) Lineage(_ context.Context, run domain.RunID) ([]domain.RunID, error) {
	switch run {
	case p.child:
		return []domain.RunID{p.parent}, nil
	case p.parent:
		return []domain.RunID{p.child}, nil
	}
	return nil, nil
}

func (p parentOf) ReportRecipient(_ context.Context, run domain.RunID) (domain.RunID, error) {
	if run == p.child {
		return p.parent, nil
	}
	return "", nil
}

func (p parentOf) ValidateReport(context.Context, domain.RunID, store.CoordOutcome) error { return nil }

func (p parentOf) NeedsInput(context.Context, domain.RunID, string) {}
