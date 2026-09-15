package sshd

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/collab"
	"github.com/3xDevOps/Aether/internal/control"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/evidence"
	"github.com/3xDevOps/Aether/internal/gitengine"
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

type handlerEvidenceService struct {
	packet      protocol.EvidencePacket
	list        protocol.EvidencePacketListResult
	patch       gitengine.Patch
	transcript  []byte
	capture     protocol.EvidencePacket
	captureErr  error
	captureSeen bool
}

func (f *handlerEvidenceService) List(context.Context, domain.WorkspaceID, domain.RunID, string, int) (protocol.EvidencePacketListResult, error) {
	return f.list, nil
}

func (f *handlerEvidenceService) Get(context.Context, domain.WorkspaceID, string) (protocol.EvidencePacket, error) {
	return f.packet, nil
}

func (f *handlerEvidenceService) RenderPatch(context.Context, domain.WorkspaceID, string, int) (gitengine.Patch, error) {
	return f.patch, nil
}

func (f *handlerEvidenceService) ReadTranscript(context.Context, domain.WorkspaceID, string, int) ([]byte, bool, error) {
	return f.transcript, false, nil
}
func (f *handlerEvidenceService) Capture(context.Context, evidence.Request) (protocol.EvidencePacket, error) {
	f.captureSeen = true
	return f.capture, f.captureErr
}

func handlerPacket(id string, ws domain.WorkspaceID, run domain.RunID, creator domain.MemberID) *store.EvidencePacket {
	return &store.EvidencePacket{
		ID:             id,
		WorkspaceID:    ws,
		RunID:          run,
		CreatorID:      creator,
		Trigger:        store.EvidenceFinish,
		IdempotencyKey: "packet-" + id,
		EventBoundary:  1,
	}
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

func TestEvidenceHandlersScopeAndBoundedReads(t *testing.T) {
	e := newTestEnv(t, nil)
	stored := handlerPacket("packet-1", e.ws.ID, e.run.ID, e.member.ID)
	if err := e.store.CreateEvidencePacket(context.Background(), stored); err != nil {
		t.Fatalf("create packet: %v", err)
	}
	packet := protocol.EvidencePacketFromStore(stored)
	evidenceFake := &handlerEvidenceService{
		packet:     packet,
		list:       protocol.EvidencePacketListResult{Packets: []protocol.EvidencePacket{packet}},
		patch:      gitengine.Patch{Text: "diff --git a/a b/a", Truncated: true},
		transcript: []byte("shell output"),
	}
	e.srv.cfg.Services.Evidence = evidenceFake

	var got protocol.RunEvidenceGetResult
	if err := handlerCallJSON(t, e, e.member.ID, protocol.MethodRunEvidenceGet, protocol.RunEvidenceGetParams{
		WorkspaceID: string(e.ws.ID), PacketID: stored.ID,
	}, &got); err != nil {
		t.Fatalf("evidence get: %v", err)
	}
	if got.Packet.ID != stored.ID {
		t.Fatalf("evidence packet = %+v", got.Packet)
	}
	var patch protocol.RunEvidencePatchResult
	if err := handlerCallJSON(t, e, e.member.ID, protocol.MethodRunEvidencePatch, protocol.RunEvidencePatchParams{
		WorkspaceID: string(e.ws.ID), PacketID: stored.ID, MaxBytes: 128,
	}, &patch); err != nil {
		t.Fatalf("evidence patch: %v", err)
	}
	if patch.Patch != evidenceFake.patch.Text || !patch.Truncated {
		t.Fatalf("evidence patch = %+v", patch)
	}
	var transcript protocol.RunEvidenceTranscriptResult
	if err := handlerCallJSON(t, e, e.member.ID, protocol.MethodRunEvidenceTranscript, protocol.RunEvidenceTranscriptParams{
		WorkspaceID: string(e.ws.ID), PacketID: stored.ID, MaxBytes: 128,
	}, &transcript); err != nil {
		t.Fatalf("evidence transcript: %v", err)
	}
	decoded, err := base64.StdEncoding.DecodeString(transcript.DataBase64)
	if err != nil || string(decoded) != string(evidenceFake.transcript) {
		t.Fatalf("transcript = %q, decode error=%v", transcript.DataBase64, err)
	}
	if err := handlerCallJSON(t, e, e.member.ID, protocol.MethodRunEvidenceTranscript, protocol.RunEvidenceTranscriptParams{
		WorkspaceID: string(e.ws.ID), PacketID: stored.ID, MaxBytes: evidence.MaxTranscriptBytes + 1,
	}, nil); err == nil {
		t.Fatal("oversized transcript read succeeded")
	}
	if err := handlerCallJSON(t, e, e.member.ID, protocol.MethodRunEvidenceGet, protocol.RunEvidenceGetParams{
		WorkspaceID: "workspace-not-mine", PacketID: stored.ID,
	}, nil); err == nil {
		t.Fatal("cross-workspace evidence read succeeded")
	}
	var list protocol.EvidencePacketListResult
	if err := handlerCallJSON(t, e, e.member.ID, protocol.MethodRunEvidenceList, protocol.RunEvidenceListParams{
		WorkspaceID: string(e.ws.ID), RunID: string(e.run.ID), Limit: 1,
	}, &list); err != nil || len(list.Packets) != 1 {
		t.Fatalf("evidence list = %+v, err=%v", list, err)
	}
}

func TestHandoffPostsImmediateEvidenceContext(t *testing.T) {
	e := newTestEnv(t, nil)
	_, recipient := addMember(t, e, "Grace", domain.RoleCollaborator, false)
	rooms := &handlerRoomService{postResult: collab.Result{Message: handlerRoomMessage("handoff-context", e.ws.ID, e.run.ID, e.member.ID)}}
	capture := &handlerEvidenceService{capture: protocol.EvidencePacket{ID: "handoff-packet"}}
	e.srv.cfg.Services.Rooms = rooms
	e.srv.cfg.Services.Evidence = capture
	if err := handlerCallJSON(t, e, e.member.ID, protocol.MethodRunHandoff, protocol.RunHandoffParams{
		RunID: string(e.run.ID), ToMemberID: string(recipient.ID),
	}, nil); err != nil {
		t.Fatalf("run.handoff: %v", err)
	}
	if !capture.captureSeen || rooms.postInput.Kind != store.RoomMessageSystem || !strings.Contains(rooms.postInput.Body, "handoff-packet") {
		t.Fatalf("handoff context = capture=%v input=%+v", capture.captureSeen, rooms.postInput)
	}
}

func TestHandoffTransientEvidenceRetriesAndPublishesOneRoomRecord(t *testing.T) {
	e := newTestEnv(t, nil)
	_, recipient := addMember(t, e, "Grace", domain.RoleCollaborator, false)
	rooms := e.srv.cfg.Services.Rooms
	capture := &handlerEvidenceService{
		capture:    protocol.EvidencePacket{ID: "handoff-packet"},
		captureErr: errors.New("temporary capture failure"),
	}
	e.srv.cfg.Services.Evidence = capture
	if err := handlerCallJSON(t, e, e.member.ID, protocol.MethodRunHandoff, protocol.RunHandoffParams{
		RunID: string(e.run.ID), ToMemberID: string(recipient.ID),
	}, nil); err != nil {
		t.Fatalf("run.handoff: %v", err)
	}
	outbox := e.store.(store.HandoffOutboxStore)
	pending, err := outbox.ListPendingHandoffOutbox(t.Context(), 10)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending handoff after transient capture = %v, err=%v", pending, err)
	}
	if pending[0].EvidenceState != store.HandoffEvidencePending ||
		pending[0].PublicationState != store.HandoffPublicationPending {
		t.Fatalf("transient capture settled permanently = %+v", pending[0])
	}

	capture.captureErr = nil
	e.srv.processHandoffOutbox(t.Context(), pending[0], outbox, rooms, capture)
	e.srv.processHandoffOutbox(t.Context(), pending[0], outbox, rooms, capture)
	remaining, err := outbox.ListPendingHandoffOutbox(t.Context(), 10)
	if err != nil || len(remaining) != 0 {
		t.Fatalf("pending handoff after replay = %v, err=%v", remaining, err)
	}
	page, err := e.store.ListRoomMessages(t.Context(), e.ws.ID, e.run.ID, "", 10)
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("handoff room records = %v, err=%v; duplicate replay", page, err)
	}
	if !strings.Contains(page.Items[0].Body, "handoff-packet") {
		t.Fatalf("handoff room record = %q, want packet attribution", page.Items[0].Body)
	}
}
