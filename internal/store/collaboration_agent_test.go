package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRoomMessageAgentDeliveryInEitherOrder(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	claimed := func(key string) string {
		t.Helper()
		msg := testRoomMessage(t, db, RoomMessageSteerRequest, key)
		if err := db.CreateRoomMessage(ctx, msg); err != nil {
			t.Fatalf("CreateRoomMessage: %v", err)
		}
		if ok, err := db.ClaimRoomMessage(ctx, msg.ID, time.Now().UTC(), false, ""); err != nil || !ok {
			t.Fatalf("ClaimRoomMessage = (%v, %v)", ok, err)
		}
		return msg.ID
	}
	state := func(id string) (RoomMessageState, AgentDelivery) {
		t.Helper()
		m, err := db.GetRoomMessage(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return m.State, m.AgentDelivery
	}

	inOrder := claimed("in-order")
	if err := db.TransitionRoomMessage(ctx, inOrder, RoomMessageSent, nil, nil); err != nil {
		t.Fatal(err)
	}
	if ok, err := db.MarkRoomMessageAgentQueued(ctx, inOrder); err != nil || !ok {
		t.Fatalf("mark queued = (%v, %v)", ok, err)
	}
	if s, a := state(inOrder); s != RoomMessageSent || a != AgentQueued {
		t.Fatalf("queued row = %q/%q", s, a)
	}
	if ok, err := db.SettleRoomMessageAgentDelivery(ctx, inOrder, nil); err != nil || !ok {
		t.Fatalf("settle delivered = (%v, %v)", ok, err)
	}
	if ok, _ := db.SettleRoomMessageAgentDelivery(ctx, inOrder, &RoomMessageFailure{Code: "agent_disconnected"}); ok {
		t.Fatal("a delivered message was dropped afterwards")
	}
	if s, a := state(inOrder); s != RoomMessageSent || a != AgentDelivered {
		t.Fatalf("delivered row = %q/%q", s, a)
	}

	early := claimed("delivered-early")
	if ok, err := db.SettleRoomMessageAgentDelivery(ctx, early, nil); err != nil || !ok {
		t.Fatalf("settle before send = (%v, %v)", ok, err)
	}
	if err := db.TransitionRoomMessage(ctx, early, RoomMessageSent, nil, nil); err != nil {
		t.Fatal(err)
	}
	if ok, _ := db.MarkRoomMessageAgentQueued(ctx, early); ok {
		t.Fatal("marking queued overwrote the delivery")
	}
	if s, a := state(early); s != RoomMessageSent || a != AgentDelivered {
		t.Fatalf("early delivered row = %q/%q", s, a)
	}

	dropped := claimed("dropped-early")
	if ok, err := db.SettleRoomMessageAgentDelivery(ctx, dropped, &RoomMessageFailure{Code: "agent_disconnected"}); err != nil || !ok {
		t.Fatalf("drop before send = (%v, %v)", ok, err)
	}
	if err := db.TransitionRoomMessage(ctx, dropped, RoomMessageSent, nil, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("recording the send after the drop = %v, want conflict", err)
	}
	if s, _ := state(dropped); s != RoomMessageNotSent {
		t.Fatalf("dropped row = %q", s)
	}
}

func TestDropAgentQueuedRoomMessagesSettlesOnlyQueuedSteers(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	sent := func(key string) string {
		t.Helper()
		msg := testRoomMessage(t, db, RoomMessageSteerRequest, key)
		if err := db.CreateRoomMessage(ctx, msg); err != nil {
			t.Fatalf("CreateRoomMessage: %v", err)
		}
		if ok, err := db.ClaimRoomMessage(ctx, msg.ID, time.Now().UTC(), false, ""); err != nil || !ok {
			t.Fatalf("ClaimRoomMessage = (%v, %v)", ok, err)
		}
		if err := db.TransitionRoomMessage(ctx, msg.ID, RoomMessageSent, nil, nil); err != nil {
			t.Fatal(err)
		}
		return msg.ID
	}
	queued, delivered, plain := sent("queued"), sent("delivered"), sent("plain")
	for _, id := range []string{queued, delivered} {
		if ok, err := db.MarkRoomMessageAgentQueued(ctx, id); err != nil || !ok {
			t.Fatalf("mark queued = (%v, %v)", ok, err)
		}
	}
	if ok, err := db.SettleRoomMessageAgentDelivery(ctx, delivered, nil); err != nil || !ok {
		t.Fatalf("settle delivered = (%v, %v)", ok, err)
	}

	failure := &RoomMessageFailure{Code: "agent_disconnected", Message: "acphost: agent connection closed"}
	ids, err := db.DropAgentQueuedRoomMessages(ctx, failure)
	if err != nil || len(ids) != 1 || ids[0] != queued {
		t.Fatalf("dropped = (%v, %v), want [%s]", ids, err, queued)
	}
	got, err := db.GetRoomMessage(ctx, queued)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != RoomMessageNotSent || got.Failure == nil || got.Failure.Code != "agent_disconnected" {
		t.Fatalf("dropped row = %q, %+v", got.State, got.Failure)
	}
	for _, id := range []string{delivered, plain} {
		if m, _ := db.GetRoomMessage(ctx, id); m.State != RoomMessageSent {
			t.Fatalf("row %s = %q, want sent", id, m.State)
		}
	}
	if ids, err := db.DropAgentQueuedRoomMessages(ctx, failure); err != nil || len(ids) != 0 {
		t.Fatalf("second drop = (%v, %v), want none", ids, err)
	}
}
