package sshd

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/3xDevOps/Aether/internal/collab"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

type recordingRoomService struct {
	RoomService
	posts int
}

func (r *recordingRoomService) Post(context.Context, collab.MessageInput) (collab.Result, error) {
	r.posts++
	return collab.Result{}, nil
}

func TestRunRoomPostRejectsServerReservedKind(t *testing.T) {
	rec := &recordingRoomService{}
	srv := &Server{cfg: Config{Services: Services{Rooms: rec}}}
	for _, kind := range []protocol.RoomMessageKind{protocol.RoomMessageSystem, "bogus"} {
		raw, _ := json.Marshal(protocol.RunRoomPostParams{
			WorkspaceID: "ws", RunID: "run", Body: "x", IdempotencyKey: "k", Kind: kind,
		})
		_, perr := srv.runRoomPost(context.Background(), domain.MemberID("mem"), raw)
		if perr == nil || perr.Code != protocol.CodeInvalidParams {
			t.Fatalf("kind %q = %v, want invalid params", kind, perr)
		}
	}
	if rec.posts != 0 {
		t.Fatalf("Post called %d times for rejected kinds", rec.posts)
	}
}
