package server

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"testing"

	"github.com/3xDevOps/Aether/internal/coord"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/evidence"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

type wideChangeCapture struct {
	db        *store.DB
	workspace domain.WorkspaceID
	paths     []string
}

func (c wideChangeCapture) Capture(ctx context.Context, req evidence.Request) (protocol.EvidencePacket, error) {
	packet := &store.EvidencePacket{
		WorkspaceID: c.workspace, RunID: req.RunID, Origin: req.Origin, PublicationOwner: req.PublicationOwner,
		Trigger: req.Trigger, IdempotencyKey: req.IdempotencyKey, RetainedRevision: "wide-revision",
	}
	for _, p := range c.paths {
		packet.ChangedFiles = append(packet.ChangedFiles, store.ChangedFileFact{Path: p})
	}
	if err := c.db.CreateEvidencePacket(ctx, packet); err != nil {
		return protocol.EvidencePacket{}, err
	}
	return protocol.EvidencePacketFromStore(packet), nil
}

func TestRecordedWorkerReportWithManyOutOfScopeFilesSubmitsOnRetry(t *testing.T) {
	f := newWakeServerFixture(t, true)
	ctx := context.Background()
	paths := make([]string, 100)
	for i := range paths {
		paths[i] = fmt.Sprintf("outside/%03d.go", i)
	}
	capture := wideChangeCapture{db: f.db, workspace: f.run.WorkspaceID, paths: paths}
	svc, err := coord.New(coord.Config{
		Dir: filepath.Join(f.dir, "report-coord"), Store: f.db, Mail: f.db, Bus: f.bus, Peers: wakeNoPeers{},
		Mission: f.missions, Evidence: capture, EvidencePackets: f.db,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	params := protocol.CoordReportParams{Outcome: protocol.CoordOutcomeSuccess, Summary: "wide change", IdempotencyKey: "wide-1"}
	// The state a report was left in when submission failed after finalization.
	report := &store.CoordReport{
		WorkspaceID: f.run.WorkspaceID, RunID: f.run.ID, Outcome: store.CoordOutcomeSuccess,
		Summary: params.Summary, NextAction: "review retained evidence", IdempotencyKey: params.IdempotencyKey,
	}
	if _, err = f.db.ReserveCoordReport(ctx, report); err != nil {
		t.Fatal(err)
	}
	packet, err := capture.Capture(ctx, evidence.Request{
		RunID: f.run.ID, Origin: store.EvidenceOrigin{Kind: store.EvidenceOriginRun, ID: string(f.run.ID)},
		PublicationOwner: store.EvidencePublicationOwnerCoordReport, Trigger: store.EvidenceReport, IdempotencyKey: params.IdempotencyKey,
	})
	if err != nil {
		t.Fatal(err)
	}
	report.EvidenceRefs = append(report.EvidenceRefs, packet.ID)
	if _, err = f.db.FinalizeCoordReport(ctx, report); err != nil {
		t.Fatal(err)
	}
	other := params
	other.IdempotencyKey = "wide-2"
	if _, rpcErr := svc.CoordReport(ctx, f.run.ID, other); rpcErr == nil || rpcErr.Code != protocol.CodeConflict {
		t.Fatalf("report under a new key = %v, want CodeConflict", rpcErr)
	}
	retry, rpcErr := svc.CoordReport(ctx, f.run.ID, params)
	if rpcErr != nil || retry.ReportID != report.ID {
		t.Fatalf("retry under the original key = %+v, %v; want report %s", retry, rpcErr, report.ID)
	}
	submissions, err := f.db.ListSubmissions(ctx, f.mission.ID, f.attempt.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if len(submissions) != 1 || submissions[0].AttemptID != f.attempt.ID || !slices.Equal(submissions[0].ScopeViolations, paths) {
		t.Fatalf("submissions = %+v, want one for attempt %s listing all %d violations", submissions, f.attempt.ID, len(paths))
	}
	attempt, err := f.db.GetAttempt(ctx, f.attempt.ID)
	if err != nil || attempt.State != domain.AttemptSubmitted {
		t.Fatalf("attempt after retry = %+v, %v; want submitted", attempt, err)
	}
}
