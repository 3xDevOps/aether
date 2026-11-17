package memory

import (
	"context"
	"testing"

	"github.com/3xDevOps/cloudfield/internal/domain"
	"github.com/3xDevOps/cloudfield/internal/protocol"
	"github.com/3xDevOps/cloudfield/internal/store"
)

func (f *fixture) reportNotes(t *testing.T) []protocol.MemoryItem {
	t.Helper()
	res, err := f.svc.Search(context.Background(), f.dana.ID, protocol.MemorySearchParams{WorkspaceID: string(f.ws.ID), Source: protocol.MemorySourceReport})
	if err != nil {
		t.Fatal(err)
	}
	return res.Items
}

func (f *fixture) report(t *testing.T, run *domain.Run, summary, key string) {
	t.Helper()
	if err := f.db.AppendCoordReport(context.Background(), &store.CoordReport{
		WorkspaceID: f.ws.ID, RunID: run.ID, Outcome: store.CoordOutcomeSuccess, Summary: summary, IdempotencyKey: key,
	}); err != nil {
		t.Fatal(err)
	}
}

// A report writes an outcome note only while the run owner may push, the same
// gate memory.add applies.
func TestReportOutcomeNoteRequiresPush(t *testing.T) {
	f := newFixture(t)
	f.report(t, f.run(f.member("Vic", domain.RoleViewer), "claude", "look around"), "viewer outcome", "v1")
	if got := f.reportNotes(t); len(got) != 0 {
		t.Fatalf("viewer-owned report wrote %d outcome notes, want 0", len(got))
	}
	f.report(t, f.run(f.dana, "claude", "do work"), "collaborator outcome", "c1")
	if got := f.reportNotes(t); len(got) != 1 {
		t.Fatalf("collaborator-owned report wrote %d outcome notes, want 1", len(got))
	}
}

// Demoting a run's owner mid-session stops later reports rewriting its note.
func TestReportOutcomeNoteNotRewrittenAfterDemotion(t *testing.T) {
	f := newFixture(t)
	owner := f.member("Cole", domain.RoleCollaborator)
	run := f.run(owner, "claude", "work")
	f.report(t, run, "first outcome", "r1")
	before := f.reportNotes(t)
	if len(before) != 1 {
		t.Fatalf("collaborator report wrote %d notes, want 1", len(before))
	}
	if _, err := f.db.SetMemberRole(context.Background(), owner.ID, domain.RoleViewer, f.member("Root", domain.RoleSuperAdmin).ID); err != nil {
		t.Fatal(err)
	}
	f.report(t, run, "rewritten after demotion", "r2")
	after := f.reportNotes(t)
	if len(after) != 1 || after[0].Title != before[0].Title {
		t.Fatalf("note after demotion = %+v, want unchanged %+v", after, before)
	}
}
