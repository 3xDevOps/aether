package store

import (
	"context"
	"errors"
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
