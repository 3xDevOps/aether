package collab

import (
	"context"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/control"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/store"
)

// contextStore refuses a cancelled context, as the database/sql store does.
type contextStore struct{ *collabStore }

func (s contextStore) TransitionRoomMessage(ctx context.Context, id string, state store.RoomMessageState, deliveredAt *time.Time, failure *store.RoomMessageFailure) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.collabStore.TransitionRoomMessage(ctx, id, state, deliveredAt, failure)
}

func TestDeliveredMessageSettlesAfterTheCallerGoesAway(t *testing.T) {
	st, clock, ws, owner, _, run := setupCollab(t)
	controlService := control.New(control.Config{Now: clock.Now})
	lease, _, err := controlService.Acquire(string(run.ID), string(owner.ID), "tab", false)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	service, err := New(Config{
		Store: contextStore{st}, Runs: st, Workspaces: st, Control: controlService, Now: clock.Now,
		Inject: func(context.Context, domain.RunID, domain.MemberID, domain.AgentPrompt, bool, func(error)) (string, error) {
			cancel()
			return "", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Post(ctx, MessageInput{
		WorkspaceID: ws.ID, RunID: run.ID, ActorID: owner.ID, Kind: store.RoomMessageSteerRequest,
		Body: "ship it", IdempotencyKey: "caller-gone",
		ControllerSessionID: lease.SessionID, ControllerGeneration: lease.Generation,
	})
	if err != nil {
		t.Fatalf("Post: %v", err)
	}
	stored, err := st.GetRoomMessage(context.Background(), result.Message.ID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Receipt != ReceiptSent || stored.State != store.RoomMessageSent {
		t.Fatalf("receipt %q, stored state %q, want both sent", result.Receipt, stored.State)
	}
}
