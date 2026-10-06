package sshd

import (
	"context"
	"errors"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

func TestCoordMessagesListIsAWorkspaceViewRead(t *testing.T) {
	e := newTestEnv(t, nil)
	ctx := context.Background()
	db := e.store.(*store.DB)
	peer := &domain.Run{
		WorkspaceID: e.ws.ID, MemberID: e.member.ID, Task: "peer",
		Harness: "claude", Mode: domain.LaunchTUI, Status: domain.RunRunning, Branch: "aether/peer",
	}
	if err := db.CreateRun(ctx, peer); err != nil {
		t.Fatalf("create peer: %v", err)
	}
	msg := &store.RunMessage{WorkspaceID: e.ws.ID, FromRun: e.run.ID, ToRun: peer.ID, Body: "hold off on auth.go", Kind: store.RunMessageKindQuestion}
	if err := db.AppendRunMessage(ctx, msg, 100); err != nil {
		t.Fatalf("AppendRunMessage: %v", err)
	}
	_, viewer := addMember(t, e, "Vera", domain.RoleViewer, false)

	var list protocol.CoordMessagesListResult
	if err := handlerCallJSON(t, e, viewer.ID, protocol.MethodCoordMessagesList, protocol.CoordMessagesListParams{
		WorkspaceID: string(e.ws.ID), RunID: string(peer.ID),
	}, &list); err != nil {
		t.Fatalf("viewer list: %v", err)
	}
	if len(list.Messages) != 1 {
		t.Fatalf("list = %+v, want the one message to the peer", list)
	}
	got := list.Messages[0]
	if got.ID != msg.ID || got.FromRunID != string(e.run.ID) || got.ToRunID != string(peer.ID) ||
		got.Kind != protocol.CoordMessageKindQuestion || got.CorrelationID != msg.ID ||
		got.Body != msg.Body || got.DeliveredAt != nil || got.AckedAt != nil {
		t.Fatalf("row = %+v, want the undelivered question", got)
	}

	for name, tc := range map[string]struct {
		params protocol.CoordMessagesListParams
		code   int
	}{
		"no workspace":      {protocol.CoordMessagesListParams{}, protocol.CodeInvalidParams},
		"unknown workspace": {protocol.CoordMessagesListParams{WorkspaceID: "ws-missing"}, protocol.CodeNotFound},
		"bad cursor":        {protocol.CoordMessagesListParams{WorkspaceID: string(e.ws.ID), Before: "not-a-cursor"}, protocol.CodeInvalidParams},
		"oversized page":    {protocol.CoordMessagesListParams{WorkspaceID: string(e.ws.ID), Limit: protocol.CollaborationMaxPageSize + 1}, protocol.CodeInvalidParams},
	} {
		err := handlerCallJSON(t, e, viewer.ID, protocol.MethodCoordMessagesList, tc.params, nil)
		var rpcErr *protocol.Error
		if !errors.As(err, &rpcErr) || rpcErr.Code != tc.code {
			t.Errorf("%s: err = %v, want code %d", name, err, tc.code)
		}
	}
}
