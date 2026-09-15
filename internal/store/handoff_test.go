package store

import (
	"context"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

func TestHandoffOutboxPhasesAreDurableAndIdempotent(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	workspace := mustCreateWorkspace(t, db)
	from := mustCreateMember(t, db)
	to := mustCreateMember(t, db)
	run := mustCreateRun(t, db, workspace.ID, from.ID, domain.RunRunning)
	h := &HandoffOutbox{
		ID: "handoff-op-1", WorkspaceID: workspace.ID, RunID: run.ID,
		ActorID: from.ID, FromMemberID: from.ID, ToMemberID: to.ID,
		CreatedAt: time.Unix(42, 0).UTC(),
	}
	if err := db.TransferRunWithHandoff(ctx, h); err != nil {
		t.Fatalf("TransferRunWithHandoff: %v", err)
	}
	gotRun, err := db.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if gotRun.MemberID != to.ID {
		t.Fatalf("run owner = %q, want %q", gotRun.MemberID, to.ID)
	}
	pending, err := db.ListPendingHandoffOutbox(ctx, 10)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending after transfer = %v, err=%v", pending, err)
	}
	if pending[0].TimelineState != HandoffTimelinePending || pending[0].CoauthorState != HandoffCoauthorPending {
		t.Fatalf("initial phases = %+v", pending[0])
	}
	if timelineErr := db.MarkHandoffTimeline(ctx, h.ID); timelineErr != nil {
		t.Fatalf("MarkHandoffTimeline: %v", timelineErr)
	}
	if retryTimelineErr := db.MarkHandoffTimeline(ctx, h.ID); retryTimelineErr != nil {
		t.Fatalf("MarkHandoffTimeline retry: %v", retryTimelineErr)
	}
	if coauthorErr := db.MarkHandoffCoauthor(ctx, h.ID); coauthorErr != nil {
		t.Fatalf("MarkHandoffCoauthor: %v", coauthorErr)
	}
	if retryCoauthorErr := db.MarkHandoffCoauthor(ctx, h.ID); retryCoauthorErr != nil {
		t.Fatalf("MarkHandoffCoauthor retry: %v", retryCoauthorErr)
	}
	if evidenceErr := db.SetHandoffEvidence(ctx, h.ID, "packet-1", HandoffEvidenceAvailable); evidenceErr != nil {
		t.Fatalf("SetHandoffEvidence: %v", evidenceErr)
	}
	if retryEvidenceErr := db.SetHandoffEvidence(ctx, h.ID, "packet-1", HandoffEvidenceAvailable); retryEvidenceErr != nil {
		t.Fatalf("SetHandoffEvidence retry: %v", retryEvidenceErr)
	}
	if publishErr := db.MarkHandoffPublished(ctx, h.ID, h.CreatedAt); publishErr != nil {
		t.Fatalf("MarkHandoffPublished: %v", publishErr)
	}
	if retryPublishErr := db.MarkHandoffPublished(ctx, h.ID, h.CreatedAt); retryPublishErr != nil {
		t.Fatalf("MarkHandoffPublished retry: %v", retryPublishErr)
	}
	pending, err = db.ListPendingHandoffOutbox(ctx, 10)
	if err != nil || len(pending) != 0 {
		t.Fatalf("pending after all phases = %v, err=%v", pending, err)
	}
}
func TestCompletedHandoffDoesNotRetainMemberForeignKeys(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	workspace := mustCreateWorkspace(t, db)
	from := mustCreateMember(t, db)
	to := mustCreateMember(t, db)
	run := &domain.Run{
		WorkspaceID: workspace.ID, MemberID: from.ID, AccountMemberID: to.ID,
		Task: "transfer ownership", Harness: "claude", Mode: domain.LaunchTUI,
		Status: domain.RunRunning,
	}
	if err := db.CreateRun(ctx, run); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	h := &HandoffOutbox{
		ID: "handoff-former-member", WorkspaceID: workspace.ID, RunID: run.ID,
		ActorID: from.ID, FromMemberID: from.ID, ToMemberID: to.ID,
		CreatedAt: time.Unix(42, 0).UTC(),
	}
	if err := db.TransferRunWithHandoff(ctx, h); err != nil {
		t.Fatalf("TransferRunWithHandoff: %v", err)
	}
	if err := db.MarkHandoffTimeline(ctx, h.ID); err != nil {
		t.Fatalf("MarkHandoffTimeline: %v", err)
	}
	if err := db.MarkHandoffCoauthor(ctx, h.ID); err != nil {
		t.Fatalf("MarkHandoffCoauthor: %v", err)
	}
	if err := db.SetHandoffEvidence(ctx, h.ID, "packet-former-member", HandoffEvidenceAvailable); err != nil {
		t.Fatalf("SetHandoffEvidence: %v", err)
	}
	if err := db.MarkHandoffPublished(ctx, h.ID, h.CreatedAt); err != nil {
		t.Fatalf("MarkHandoffPublished: %v", err)
	}
	if err := db.DeleteMember(ctx, from.ID); err != nil {
		t.Fatalf("DeleteMember former handoff member: %v", err)
	}
	got, err := db.GetHandoffOutbox(ctx, h.ID)
	if err != nil {
		t.Fatalf("GetHandoffOutbox: %v", err)
	}
	if got.ActorID != from.ID || got.FromMemberID != from.ID || got.ToMemberID != to.ID {
		t.Fatalf("retained handoff member IDs = %+v", got)
	}
}

func TestHandoffOutboxPageDrainsBeyondOneHundredRows(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	workspace := mustCreateWorkspace(t, db)
	from := mustCreateMember(t, db)
	to := mustCreateMember(t, db)
	run := mustCreateRun(t, db, workspace.ID, from.ID, domain.RunRunning)
	for i := range 101 {
		actor, recipient := from.ID, to.ID
		if i%2 == 1 {
			actor, recipient = to.ID, from.ID
		}
		h := &HandoffOutbox{
			ID:          "handoff-page-" + string(rune('a'+i%26)) + string(rune('0'+i/26)),
			WorkspaceID: workspace.ID, RunID: run.ID, ActorID: actor,
			FromMemberID: actor, ToMemberID: recipient, CreatedAt: time.Unix(int64(i+1), 0).UTC(),
		}
		if err := db.TransferRunWithHandoff(ctx, h); err != nil {
			t.Fatalf("TransferRunWithHandoff(%d): %v", i, err)
		}
	}
	first, cursor, err := db.ListPendingHandoffOutboxPage(ctx, "", 100)
	if err != nil || len(first) != 100 || cursor == "" {
		t.Fatalf("first handoff page = %d rows cursor=%q err=%v", len(first), cursor, err)
	}
	second, next, err := db.ListPendingHandoffOutboxPage(ctx, cursor, 100)
	if err != nil || len(second) != 1 || next != "" {
		t.Fatalf("second handoff page = %d rows cursor=%q err=%v", len(second), next, err)
	}
}
