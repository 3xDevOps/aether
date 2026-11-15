package memory

import (
	"context"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

// A viewer-owned run's report must not write a team-memory outcome note, the
// same Push gate that memory.add enforces.
func TestReportOutcomeNoteRequiresPush(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	viewer := f.member("Vic", domain.RoleViewer)
	viewerRun := f.run(viewer, "claude", "look around")
	if err := f.db.AppendCoordReport(ctx, &store.CoordReport{
		WorkspaceID: f.ws.ID, RunID: viewerRun.ID, Outcome: store.CoordOutcomeSuccess,
		Summary: "viewer outcome", IdempotencyKey: "v1",
	}); err != nil {
		t.Fatal(err)
	}
	res, err := f.svc.Search(ctx, f.dana.ID, protocol.MemorySearchParams{WorkspaceID: string(f.ws.ID), Source: protocol.MemorySourceReport})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != 0 {
		t.Fatalf("viewer-owned report wrote %d outcome notes, want 0", len(res.Items))
	}

	// A collaborator-owned run still records its outcome note.
	collabRun := f.run(f.dana, "claude", "do work")
	if err := f.db.AppendCoordReport(ctx, &store.CoordReport{
		WorkspaceID: f.ws.ID, RunID: collabRun.ID, Outcome: store.CoordOutcomeSuccess,
		Summary: "collaborator outcome", IdempotencyKey: "c1",
	}); err != nil {
		t.Fatal(err)
	}
	res, err = f.svc.Search(ctx, f.dana.ID, protocol.MemorySearchParams{WorkspaceID: string(f.ws.ID), Source: protocol.MemorySourceReport})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != 1 {
		t.Fatalf("collaborator-owned report wrote %d outcome notes, want 1", len(res.Items))
	}
}
