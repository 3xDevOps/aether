package store

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

func testRoomMessage(t *testing.T, db *DB, kind RoomMessageKind, key string) *RoomMessage {
	t.Helper()
	workspace := mustCreateWorkspace(t, db)
	member := mustCreateMember(t, db)
	run := mustCreateRun(t, db, workspace.ID, member.ID, domain.RunRunning)
	return &RoomMessage{
		WorkspaceID:    workspace.ID,
		RunID:          run.ID,
		ActorID:        member.ID,
		Kind:           kind,
		Body:           "hello",
		IdempotencyKey: key,
	}
}

func TestRoomMessageDeliverAfterRoundTrip(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	deadline := time.Now().UTC().Add(45 * time.Second).Truncate(time.Microsecond)
	msg := testRoomMessage(t, db, RoomMessageSteerRequest, "steer-1")
	msg.DeliverAfter = &deadline
	if err := db.CreateRoomMessage(ctx, msg); err != nil {
		t.Fatalf("CreateRoomMessage: %v", err)
	}
	got, err := db.GetRoomMessage(ctx, msg.ID)
	if err != nil {
		t.Fatalf("GetRoomMessage: %v", err)
	}
	if got.DeliverAfter == nil || !got.DeliverAfter.Equal(deadline) {
		t.Fatalf("deliver_after = %v, want %v", got.DeliverAfter, deadline)
	}

	for _, kind := range []RoomMessageKind{RoomMessageComment, RoomMessageReply} {
		immediate := testRoomMessage(t, db, kind, string(kind)+"-1")
		if err := db.CreateRoomMessage(ctx, immediate); err != nil {
			t.Fatalf("CreateRoomMessage(%s): %v", kind, err)
		}
		got, err := db.GetRoomMessage(ctx, immediate.ID)
		if err != nil {
			t.Fatalf("GetRoomMessage(%s): %v", kind, err)
		}
		if got.DeliverAfter != nil {
			t.Fatalf("%s deliver_after = %v, want immediate nil", kind, got.DeliverAfter)
		}
	}
}

func TestRoomMessageDeliveryAndDecisionHaveOneWinner(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	msg := testRoomMessage(t, db, RoomMessageSteerRequest, "race-1")
	due := time.Now().UTC().Add(-time.Second)
	msg.DeliverAfter = &due
	if err := db.CreateRoomMessage(ctx, msg); err != nil {
		t.Fatalf("CreateRoomMessage: %v", err)
	}
	decided, err := db.DecideRoomMessage(ctx, msg.ID, RoomMessageQueued, RoomMessageDenied, "moderator", time.Now().UTC())
	if err != nil || !decided {
		t.Fatalf("DecideRoomMessage = (%v, %v), want (true, nil)", decided, err)
	}
	if transitionErr := db.TransitionRoomMessage(ctx, msg.ID, RoomMessageSent, nil, nil); !errors.Is(transitionErr, ErrConflict) {
		t.Fatalf("delivery after denial = %v, want conflict", transitionErr)
	}

	msg = testRoomMessage(t, db, RoomMessageSteerRequest, "race-2")
	msg.DeliverAfter = &due
	if createErr := db.CreateRoomMessage(ctx, msg); createErr != nil {
		t.Fatalf("CreateRoomMessage second: %v", createErr)
	}
	if transitionErr := db.TransitionRoomMessage(ctx, msg.ID, RoomMessageSent, nil, nil); transitionErr != nil {
		t.Fatalf("delivery: %v", transitionErr)
	}
	decided, err = db.DecideRoomMessage(ctx, msg.ID, RoomMessageQueued, RoomMessageDenied, "moderator", time.Now().UTC())
	if err != nil {
		t.Fatalf("decision after delivery: %v", err)
	}
	if decided {
		t.Fatal("decision after delivery won; want one winner")
	}
}

func TestRoomMessageDecisionAttributionRoundTrip(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	msg := testRoomMessage(t, db, RoomMessageSteerRequest, "decision-1")
	if err := db.CreateRoomMessage(ctx, msg); err != nil {
		t.Fatalf("CreateRoomMessage: %v", err)
	}
	at := time.Now().UTC().Add(-time.Second).Truncate(time.Microsecond)
	decided, err := db.DecideRoomMessage(ctx, msg.ID, RoomMessageQueued, RoomMessageDenied, "reviewer-1", at)
	if err != nil || !decided {
		t.Fatalf("DecideRoomMessage = (%v, %v), want (true, nil)", decided, err)
	}
	got, err := db.GetRoomMessage(ctx, msg.ID)
	if err != nil {
		t.Fatalf("GetRoomMessage: %v", err)
	}
	if got.State != RoomMessageDenied || got.DecidedBy != "reviewer-1" || got.DecidedAt == nil || !got.DecidedAt.Equal(at) {
		t.Fatalf("decision = state=%q by=%q at=%v, want denied/reviewer-1/%v", got.State, got.DecidedBy, got.DecidedAt, at)
	}
}

func TestRoomMessageClaimHonorsDueAndForceApproval(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	msg := testRoomMessage(t, db, RoomMessageSteerRequest, "claim-1")
	now := time.Now().UTC().Truncate(time.Microsecond)
	future := now.Add(time.Minute)
	msg.DeliverAfter = &future
	if err := db.CreateRoomMessage(ctx, msg); err != nil {
		t.Fatalf("CreateRoomMessage: %v", err)
	}
	if claimed, err := db.ClaimRoomMessage(ctx, msg.ID, now, false, ""); err != nil || claimed {
		t.Fatalf("normal future claim = (%v, %v), want (false, nil)", claimed, err)
	}
	if claimed, err := db.ClaimRoomMessage(ctx, msg.ID, now, true, "moderator"); err != nil || !claimed {
		t.Fatalf("force claim = (%v, %v), want (true, nil)", claimed, err)
	}
	got, err := db.GetRoomMessage(ctx, msg.ID)
	if err != nil {
		t.Fatalf("GetRoomMessage: %v", err)
	}
	if got.State != RoomMessageUncertain || got.DecidedBy != "moderator" || got.DecidedAt == nil || !got.DecidedAt.Equal(now) {
		t.Fatalf("claim = state=%q by=%q at=%v, want uncertain/moderator/%v", got.State, got.DecidedBy, got.DecidedAt, now)
	}
	if claimed, err := db.ClaimRoomMessage(ctx, msg.ID, now, true, "other"); err != nil || claimed {
		t.Fatalf("second claim = (%v, %v), want (false, nil)", claimed, err)
	}
	if err := db.TransitionRoomMessage(ctx, msg.ID, RoomMessageUncertain, nil, &RoomMessageFailure{Code: "write_uncertain"}); err != nil {
		t.Fatalf("uncertain retry transition: %v", err)
	}
}
func TestCancelQueuedSteerRequestsIsAtomicAndScoped(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	msg := testRoomMessage(t, db, RoomMessageSteerRequest, "cancel-1")
	if err := db.CreateRoomMessage(ctx, msg); err != nil {
		t.Fatalf("CreateRoomMessage: %v", err)
	}
	second := *msg
	second.ID = ""
	second.IdempotencyKey = "cancel-2"
	if err := db.CreateRoomMessage(ctx, &second); err != nil {
		t.Fatalf("CreateRoomMessage second: %v", err)
	}
	at := time.Now().UTC().Truncate(time.Microsecond)
	cancelled, err := db.CancelQueuedSteerRequests(ctx, msg.RunID, "moderator", at)
	if err != nil {
		t.Fatalf("CancelQueuedSteerRequests: %v", err)
	}
	if len(cancelled) != 2 {
		t.Fatalf("cancelled = %d, want 2", len(cancelled))
	}
	for _, got := range cancelled {
		if got.State != RoomMessageCancelled || got.DecidedBy != "moderator" || got.DecidedAt == nil || !got.DecidedAt.Equal(at) {
			t.Fatalf("cancelled row = state=%q by=%q at=%v", got.State, got.DecidedBy, got.DecidedAt)
		}
	}
	cancelled, err = db.CancelQueuedSteerRequests(ctx, msg.RunID, "moderator", at)
	if err != nil {
		t.Fatalf("second CancelQueuedSteerRequests: %v", err)
	}
	if len(cancelled) != 0 {
		t.Fatalf("second cancellation returned %d rows, want 0", len(cancelled))
	}

	claimed := *msg
	claimed.ID = ""
	claimed.IdempotencyKey = "cancel-claimed"
	claimed.State = ""
	claimed.DecidedBy = ""
	claimed.DecidedAt = nil
	if createErr := db.CreateRoomMessage(ctx, &claimed); createErr != nil {
		t.Fatalf("CreateRoomMessage claimed: %v", createErr)
	}
	if ok, claimErr := db.ClaimRoomMessage(ctx, claimed.ID, at, true, "moderator"); claimErr != nil || !ok {
		t.Fatalf("ClaimRoomMessage = (%v, %v), want (true, nil)", ok, claimErr)
	}
	cancelled, err = db.CancelQueuedSteerRequests(ctx, msg.RunID, "moderator", at)
	if err != nil {
		t.Fatalf("third CancelQueuedSteerRequests: %v", err)
	}
	if len(cancelled) != 0 {
		t.Fatalf("cancellation claimed row, returned %d rows", len(cancelled))
	}
}

func TestEvidencePacketExpiresAtRoundTrip(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	workspace := mustCreateWorkspace(t, db)
	member := mustCreateMember(t, db)
	run := mustCreateRun(t, db, workspace.ID, member.ID, domain.RunCompleted)
	expires := time.Now().UTC().Add(24 * time.Hour).Truncate(time.Microsecond)
	packet := &EvidencePacket{
		WorkspaceID:    workspace.ID,
		RunID:          run.ID,
		CreatorID:      member.ID,
		Trigger:        EvidenceFinish,
		Objective:      "release evidence",
		ExpiresAt:      &expires,
		IdempotencyKey: "evidence-1",
	}
	if err := db.CreateEvidencePacket(ctx, packet); err != nil {
		t.Fatalf("CreateEvidencePacket: %v", err)
	}
	got, err := db.GetEvidencePacket(ctx, packet.ID)
	if err != nil {
		t.Fatalf("GetEvidencePacket: %v", err)
	}
	if got.ExpiresAt == nil || !got.ExpiresAt.Equal(expires) {
		t.Fatalf("expires_at = %v, want %v", got.ExpiresAt, expires)
	}
}

func TestListExpiredEvidencePacketsFiltersOrdersAndBounds(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	workspace := mustCreateWorkspace(t, db)
	member := mustCreateMember(t, db)
	run := mustCreateRun(t, db, workspace.ID, member.ID, domain.RunCompleted)
	cutoff := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	oldest := cutoff.Add(-3 * time.Hour)
	tied := cutoff.Add(-2 * time.Hour)
	boundary := cutoff
	future := cutoff.Add(time.Hour)
	create := func(key string, expiresAt *time.Time) *EvidencePacket {
		t.Helper()
		packet := &EvidencePacket{
			WorkspaceID:    workspace.ID,
			RunID:          run.ID,
			CreatorID:      member.ID,
			Trigger:        EvidenceReport,
			Objective:      key,
			ExpiresAt:      expiresAt,
			IdempotencyKey: key,
		}
		if err := db.CreateEvidencePacket(ctx, packet); err != nil {
			t.Fatalf("CreateEvidencePacket(%q): %v", key, err)
		}
		return packet
	}
	nullable := create("nullable", nil)
	nonExpired := create("non-expired", &future)
	old := create("old", &oldest)
	tieA := create("tie-a", &tied)
	tieB := create("tie-b", &tied)
	atBoundary := create("boundary", &boundary)

	got, err := db.ListExpiredEvidencePackets(ctx, cutoff, 1000)
	if err != nil {
		t.Fatalf("ListExpiredEvidencePackets: %v", err)
	}
	expected := []*EvidencePacket{old, tieA, tieB, atBoundary}
	sort.Slice(expected, func(i, j int) bool {
		if expected[i].ExpiresAt.Equal(*expected[j].ExpiresAt) {
			return expected[i].ID < expected[j].ID
		}
		return expected[i].ExpiresAt.Before(*expected[j].ExpiresAt)
	})
	if len(got) != len(expected) {
		t.Fatalf("expired packet count = %d, want %d", len(got), len(expected))
	}
	for i := range expected {
		if got[i].ID != expected[i].ID {
			t.Fatalf("expired packet %d = %q, want %q", i, got[i].ID, expected[i].ID)
		}
		if got[i].ID == nullable.ID || got[i].ID == nonExpired.ID {
			t.Fatalf("expired packet list included excluded packet %q", got[i].ID)
		}
	}
	if got[0].ID != old.ID || got[len(got)-1].ID != atBoundary.ID {
		t.Fatalf("expired packet ordering/boundary = %q..%q, want %q..%q", got[0].ID, got[len(got)-1].ID, old.ID, atBoundary.ID)
	}
}

func TestDeleteEvidencePacketIsIdempotent(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	workspace := mustCreateWorkspace(t, db)
	member := mustCreateMember(t, db)
	run := mustCreateRun(t, db, workspace.ID, member.ID, domain.RunCompleted)
	packet := &EvidencePacket{
		WorkspaceID:    workspace.ID,
		RunID:          run.ID,
		CreatorID:      member.ID,
		Trigger:        EvidenceReport,
		Objective:      "delete me",
		IdempotencyKey: "delete-evidence",
	}
	if err := db.CreateEvidencePacket(ctx, packet); err != nil {
		t.Fatalf("CreateEvidencePacket: %v", err)
	}
	if err := db.DeleteEvidencePacket(ctx, packet.ID); err != nil {
		t.Fatalf("first DeleteEvidencePacket: %v", err)
	}
	if err := db.DeleteEvidencePacket(ctx, packet.ID); err != nil {
		t.Fatalf("repeated DeleteEvidencePacket: %v", err)
	}
	if _, err := db.GetEvidencePacket(ctx, packet.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetEvidencePacket after delete = %v, want ErrNotFound", err)
	}
	if _, err := db.GetRun(ctx, run.ID); err != nil {
		t.Fatalf("GetRun after packet delete: %v", err)
	}
	if _, err := db.GetWorkspace(ctx, workspace.ID); err != nil {
		t.Fatalf("GetWorkspace after packet delete: %v", err)
	}
}
func TestEvidenceTombstoneCursorAndPublicationOutbox(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	workspace := mustCreateWorkspace(t, db)
	member := mustCreateMember(t, db)
	run := mustCreateRun(t, db, workspace.ID, member.ID, domain.RunCompleted)
	newer := time.Date(2026, 9, 14, 2, 0, 0, 0, time.UTC)
	older := newer.Add(-time.Hour)
	expiry := newer.Add(-time.Minute)
	first := &EvidencePacket{
		WorkspaceID: workspace.ID, RunID: run.ID, CreatorID: member.ID,
		Trigger: EvidenceFinish, Objective: "newer", CapturedAt: newer,
		ExpiresAt: &expiry, RetainedRevision: "deadbeef", IdempotencyKey: "tombstone-new",
		Sources: []EvidenceSourceFact{{Name: "git", Available: true}},
	}
	second := &EvidencePacket{
		WorkspaceID: workspace.ID, RunID: run.ID, CreatorID: member.ID,
		Trigger: EvidenceFinish, Objective: "older", CapturedAt: older,
		IdempotencyKey: "tombstone-old",
	}
	if err := db.CreateEvidencePacket(ctx, first); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateEvidencePacket(ctx, second); err != nil {
		t.Fatal(err)
	}
	at := newer.Add(time.Minute)
	if err := db.MarkEvidenceExpired(ctx, first.ID, at, []EvidenceSourceFact{{Name: "git", Reason: "retention expired"}}); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetEvidencePacket(ctx, first.ID)
	if err != nil || got.Availability != EvidenceExpired || got.RetainedRevision != "" || got.ExpiredAt == nil {
		t.Fatalf("tombstone = %+v, %v", got, err)
	}
	page, err := db.ListEvidencePackets(ctx, workspace.ID, run.ID, "", 1)
	if err != nil || len(page.Items) != 1 || page.NextBefore == "" {
		t.Fatalf("first evidence page = %+v, %v", page, err)
	}
	cursor := page.NextBefore
	if deleteErr := db.DeleteEvidencePacket(ctx, page.Items[0].ID); deleteErr != nil {
		t.Fatal(deleteErr)
	}
	page, err = db.ListEvidencePackets(ctx, workspace.ID, run.ID, cursor, 1)
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != second.ID {
		t.Fatalf("stable cursor page = %+v, %v", page, err)
	}
	pubs, _, err := db.ListPendingEvidencePublications(ctx, time.Now().UTC(), "", 10)
	if err != nil || len(pubs) != 1 || pubs[0].PacketID != second.ID {
		t.Fatalf("pending publications = %+v, %v", pubs, err)
	}
	if err := db.MarkEvidencePublicationPublished(ctx, second.ID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
}
func TestRoomOnlyMessagesAreFinalAndNotModeratable(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	for _, kind := range []RoomMessageKind{
		RoomMessageComment, RoomMessageQuestion, RoomMessageReply, RoomMessageSystem,
	} {
		msg := testRoomMessage(t, db, kind, string(kind)+"-final")
		if err := db.CreateRoomMessage(ctx, msg); err != nil {
			t.Fatalf("CreateRoomMessage(%s): %v", kind, err)
		}
		got, err := db.GetRoomMessage(ctx, msg.ID)
		if err != nil {
			t.Fatalf("GetRoomMessage(%s): %v", kind, err)
		}
		if got.State != RoomMessageSent || got.DeliveredAt == nil {
			t.Fatalf("%s state=%q delivered_at=%v, want sent/final", kind, got.State, got.DeliveredAt)
		}
		won, err := db.DecideRoomMessage(ctx, msg.ID, RoomMessageQueued, RoomMessageDenied, "moderator", time.Now().UTC())
		if err != nil {
			t.Fatalf("DecideRoomMessage(%s): %v", kind, err)
		}
		if won {
			t.Fatalf("DecideRoomMessage(%s) won for non-steer row", kind)
		}
	}
}

func TestRoomMessageSurvivesActorRemovalWithDisplaySnapshot(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	workspace := mustCreateWorkspace(t, db)
	owner := mustCreateMember(t, db)
	actor := &domain.Member{
		DisplayName: "Room participant",
		PublicKey:   testKey(t, "room-participant"),
		Color:       "#3cb44b",
		Role:        domain.RoleCollaborator,
	}
	if err := db.CreateMember(ctx, actor); err != nil {
		t.Fatalf("CreateMember actor: %v", err)
	}
	run := mustCreateRun(t, db, workspace.ID, owner.ID, domain.RunRunning)
	msg := &RoomMessage{
		WorkspaceID:    workspace.ID,
		RunID:          run.ID,
		ActorID:        actor.ID,
		Kind:           RoomMessageComment,
		Body:           "historical note",
		IdempotencyKey: "room-only-removal",
	}
	if err := db.CreateRoomMessage(ctx, msg); err != nil {
		t.Fatalf("CreateRoomMessage: %v", err)
	}
	originalKey := actor.PublicKey
	actor.DisplayName = "Renamed participant"
	if err := db.UpdateMember(ctx, actor); err != nil {
		t.Fatalf("UpdateMember: %v", err)
	}

	if err := db.DeleteMember(ctx, actor.ID); err != nil {
		t.Fatalf("DeleteMember with room-only history: %v", err)
	}
	if _, err := db.GetMember(ctx, actor.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetMember after removal: %v, want ErrNotFound", err)
	}
	if _, err := db.GetMemberByPublicKey(ctx, originalKey); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetMemberByPublicKey after removal: %v, want ErrNotFound", err)
	}

	got, err := db.GetRoomMessage(ctx, msg.ID)
	if err != nil {
		t.Fatalf("GetRoomMessage after actor removal: %v", err)
	}
	if got.ActorID != actor.ID || got.ActorDisplayName != "Room participant" || got.Body != msg.Body {
		t.Fatalf("room history = actor %q/%q body %q, want %q/%q body %q",
			got.ActorID, got.ActorDisplayName, got.Body, actor.ID, "Room participant", msg.Body)
	}
	page, err := db.ListRoomMessages(ctx, workspace.ID, run.ID, "", 10)
	if err != nil {
		t.Fatalf("ListRoomMessages after actor removal: %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].ActorID != actor.ID ||
		page.Items[0].ActorDisplayName != "Room participant" {
		t.Fatalf("listed room history = %+v, want immutable actor attribution", page.Items)
	}
}
