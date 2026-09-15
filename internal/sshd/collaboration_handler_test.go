package sshd

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/collab"
	"github.com/3xDevOps/Aether/internal/control"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

type handlerRoomService struct {
	listPage          *store.RoomMessagePage
	status            collab.Status
	postResult        collab.Result
	approveResult     collab.Result
	denyResult        *store.RoomMessage
	postInput         collab.MessageInput
	approveID         string
	approveSession    string
	approveGeneration uint64
	denyID            string
	denySession       string
	denyGeneration    uint64
	protectRun        domain.RunID
}

func (f *handlerRoomService) List(context.Context, domain.WorkspaceID, domain.RunID, string, int) (*store.RoomMessagePage, error) {
	if f.listPage == nil {
		return &store.RoomMessagePage{}, nil
	}
	return f.listPage, nil
}

func (f *handlerRoomService) Status(context.Context, domain.WorkspaceID, domain.RunID) (collab.Status, error) {
	return f.status, nil
}

func (f *handlerRoomService) Post(_ context.Context, in collab.MessageInput) (collab.Result, error) {
	f.postInput = in
	return f.postResult, nil
}

func (f *handlerRoomService) ApproveNow(_ context.Context, id string, _ domain.MemberID, session string, generation uint64) (collab.Result, error) {
	f.approveID = id
	f.approveSession = session
	f.approveGeneration = generation
	return f.approveResult, nil
}
func (f *handlerRoomService) Deny(_ context.Context, id string, _ domain.MemberID, session string, generation uint64) (*store.RoomMessage, error) {
	f.denyID = id
	f.denySession = session
	f.denyGeneration = generation
	return f.denyResult, nil
}
func (f *handlerRoomService) Protect(_ context.Context, run domain.RunID, _ domain.MemberID) error {
	f.protectRun = run
	return nil
}

type persistentRoomService struct {
	db   store.Store
	last collab.MessageInput
}

func (f *persistentRoomService) List(ctx context.Context, ws domain.WorkspaceID, run domain.RunID, before string, limit int) (*store.RoomMessagePage, error) {
	return f.db.ListRoomMessages(ctx, ws, run, before, limit)
}

func (f *persistentRoomService) Status(context.Context, domain.WorkspaceID, domain.RunID) (collab.Status, error) {
	return collab.Status{}, nil
}

func (f *persistentRoomService) Post(ctx context.Context, in collab.MessageInput) (collab.Result, error) {
	f.last = in
	m := &store.RoomMessage{
		WorkspaceID: in.WorkspaceID, RunID: in.RunID, ActorID: in.ActorID,
		Kind: in.Kind, Body: in.Body, Attachments: in.Attachments, Anchor: in.Anchor,
		CorrelationID: in.CorrelationID, IdempotencyKey: in.IdempotencyKey,
	}
	if err := f.db.CreateRoomMessage(ctx, m); err != nil {
		return collab.Result{}, err
	}
	return collab.Result{Message: m}, nil
}

func (f *persistentRoomService) ApproveNow(ctx context.Context, id string, _ domain.MemberID, _ string, _ uint64) (collab.Result, error) {
	m, err := f.db.GetRoomMessage(ctx, id)
	return collab.Result{Message: m}, err
}

func (f *persistentRoomService) Deny(ctx context.Context, id string, _ domain.MemberID, _ string, _ uint64) (*store.RoomMessage, error) {
	return f.db.GetRoomMessage(ctx, id)
}

func (f *persistentRoomService) Protect(ctx context.Context, run domain.RunID, _ domain.MemberID) error {
	return f.db.SetRunProtected(ctx, run, true)
}
func handlerRoomMessage(id string, ws domain.WorkspaceID, run domain.RunID, actor domain.MemberID) *store.RoomMessage {
	return &store.RoomMessage{
		ID:             id,
		WorkspaceID:    ws,
		RunID:          run,
		ActorID:        actor,
		Kind:           store.RoomMessageSteerRequest,
		Body:           "please continue",
		IdempotencyKey: "message-" + id,
		State:          store.RoomMessageQueued,
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
	}
}

func handlerCallJSON(t *testing.T, e *testEnv, member domain.MemberID, method string, params any, result any) error {
	t.Helper()
	rawParams, err := json.Marshal(params)
	if err != nil {
		return err
	}
	rawResult, perr := e.srv.Local(member).Call(context.Background(), method, rawParams)
	if perr != nil {
		return perr
	}
	if result == nil || len(rawResult) == 0 {
		return nil
	}
	return json.Unmarshal(rawResult, result)
}
func TestRoomHandlersPermissionsBoundsAndMutations(t *testing.T) {
	e := newTestEnv(t, nil)
	rooms := &handlerRoomService{
		listPage:   &store.RoomMessagePage{Items: []*store.RoomMessage{handlerRoomMessage("room-1", e.ws.ID, e.run.ID, e.member.ID)}},
		postResult: collab.Result{Message: handlerRoomMessage("room-2", e.ws.ID, e.run.ID, e.member.ID), Receipt: collab.ReceiptSent},
	}
	e.srv.cfg.Services.Rooms = rooms
	_, viewer := addMember(t, e, "Vera", domain.RoleViewer, false)

	var list protocol.RoomMessageListResult
	if err := handlerCallJSON(t, e, viewer.ID, protocol.MethodRunRoomList, protocol.RunRoomListParams{
		WorkspaceID: string(e.ws.ID), RunID: string(e.run.ID), Limit: 1,
	}, &list); err != nil {
		t.Fatalf("room list: %v", err)
	}
	if len(list.Messages) != 1 || list.Messages[0].ID != "room-1" {
		t.Fatalf("room list = %+v", list)
	}
	if err := handlerCallJSON(t, e, viewer.ID, protocol.MethodRunRoomPost, protocol.RunRoomPostParams{
		WorkspaceID: string(e.ws.ID), RunID: string(e.run.ID), Kind: protocol.RoomMessageKind(store.RoomMessageComment), Body: "no", IdempotencyKey: "viewer-post",
	}, nil); err == nil {
		t.Fatal("viewer room post succeeded")
	}
	if err := handlerCallJSON(t, e, e.member.ID, protocol.MethodRunRoomList, protocol.RunRoomListParams{
		WorkspaceID: string(e.ws.ID), RunID: string(e.run.ID), Limit: protocol.CollaborationMaxPageSize + 1,
	}, nil); err == nil {
		t.Fatal("oversized room list succeeded")
	}

	var posted protocol.RunRoomPostResult
	if err := handlerCallJSON(t, e, e.member.ID, protocol.MethodRunRoomPost, protocol.RunRoomPostParams{
		WorkspaceID: string(e.ws.ID), RunID: string(e.run.ID), Kind: protocol.RoomMessageKind(store.RoomMessageComment), Body: "approved", IdempotencyKey: "admin-post",
	}, &posted); err != nil {
		t.Fatalf("admin room post: %v", err)
	}
	if rooms.postInput.ActorID != e.member.ID || rooms.postInput.Kind != store.RoomMessageComment || posted.Receipt != string(collab.ReceiptSent) {
		t.Fatalf("room post input/result = %+v / %+v", rooms.postInput, posted)
	}

	queued := handlerRoomMessage("room-queued", e.ws.ID, e.run.ID, e.member.ID)
	if err := e.store.CreateRoomMessage(context.Background(), queued); err != nil {
		t.Fatalf("create queued message: %v", err)
	}
	rooms.approveResult = collab.Result{Message: queued, Receipt: collab.ReceiptSent}
	var decided protocol.RunRoomDecideResult
	if err := handlerCallJSON(t, e, e.member.ID, protocol.MethodRunRoomDecide, protocol.RunRoomDecideParams{
		MessageID: queued.ID, Decision: "approve", ControlSessionID: "controller-tab", ControlGeneration: 7,
	}, &decided); err != nil {
		t.Fatalf("approve room message: %v", err)
	}
	if rooms.approveID != queued.ID || rooms.approveSession != "controller-tab" || rooms.approveGeneration != 7 || decided.Message.ID != queued.ID {
		t.Fatalf("approve = %+v, service decision=%q/%q/%d", decided, rooms.approveID, rooms.approveSession, rooms.approveGeneration)
	}
	rooms.denyResult = queued
	if err := handlerCallJSON(t, e, e.member.ID, protocol.MethodRunRoomDecide, protocol.RunRoomDecideParams{
		MessageID: queued.ID, Decision: "deny", ControlSessionID: "controller-tab", ControlGeneration: 7,
	}, &decided); err != nil {
		t.Fatalf("deny room message: %v", err)
	}
	if rooms.denyID != queued.ID || rooms.denySession != "controller-tab" || rooms.denyGeneration != 7 {
		t.Fatalf("deny service decision=%q/%q/%d", rooms.denyID, rooms.denySession, rooms.denyGeneration)
	}
}

func TestRoomStatusAndLegacyInjectAndProtect(t *testing.T) {
	e := newTestEnv(t, nil)
	rooms := &handlerRoomService{
		status:     collab.Status{WorkspaceID: e.ws.ID, RunID: e.run.ID, Protected: true, HasController: true, Controller: controlSnapshot(e.member.ID), QueuedSteers: 3},
		postResult: collab.Result{Message: handlerRoomMessage("room-steer", e.ws.ID, e.run.ID, e.member.ID)},
	}
	e.srv.cfg.Services.Rooms = rooms

	var status protocol.RunRoomStatusResult
	if err := handlerCallJSON(t, e, e.member.ID, protocol.MethodRunRoomStatus, protocol.RunRoomStatusParams{
		WorkspaceID: string(e.ws.ID), RunID: string(e.run.ID),
	}, &status); err != nil {
		t.Fatalf("room status: %v", err)
	}
	if !status.Protected || status.Controller == nil || status.Controller.MemberID != string(e.member.ID) || status.QueuedSteers != 3 {
		t.Fatalf("room status = %+v", status)
	}

	var injected protocol.RunRoomPostResult
	if err := handlerCallJSON(t, e, e.member.ID, protocol.MethodRunInject, protocol.RunInjectParams{
		RunID: string(e.run.ID), Message: "queued steer", IdempotencyKey: "legacy-inject-test",
	}, &injected); err != nil {
		t.Fatalf("legacy run.inject: %v", err)
	}
	if rooms.postInput.Kind != store.RoomMessageSteerRequest || rooms.postInput.Body != "queued steer" ||
		rooms.postInput.IdempotencyKey != "legacy-inject-test" || rooms.postInput.ControllerSessionID != "" {
		t.Fatalf("legacy inject bypassed room queue: %+v", rooms.postInput)
	}
	if err := handlerCallJSON(t, e, e.member.ID, protocol.MethodRunProtect, protocol.RunProtectParams{
		RunID: string(e.run.ID), Protected: true,
	}, nil); err != nil {
		t.Fatalf("run.protect: %v", err)
	}
	if rooms.protectRun != e.run.ID {
		t.Fatalf("protect run = %q, want %q", rooms.protectRun, e.run.ID)
	}
}

func controlSnapshot(member domain.MemberID) control.Snapshot {
	return control.Snapshot{MemberID: member, AcquiredAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(time.Minute), SessionID: "private", Generation: 99}
}
