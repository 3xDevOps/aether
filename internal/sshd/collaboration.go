package sshd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/collab"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

// RoomService is the transport seam for the durable run-room service.
type RoomService interface {
	List(context.Context, domain.WorkspaceID, domain.RunID, string, int) (*store.RoomMessagePage, error)
	Status(context.Context, domain.WorkspaceID, domain.RunID) (collab.Status, error)
	Post(context.Context, collab.MessageInput) (collab.Result, error)
	ApproveNow(context.Context, string, domain.MemberID, string, uint64) (collab.Result, error)
	Deny(context.Context, string, domain.MemberID, string, uint64) (*store.RoomMessage, error)
	Protect(context.Context, domain.RunID, domain.MemberID) error
}

func init() {
	registerGuarded(protocol.MethodRunRoomList, permissions.View, roomTarget, (*Server).runRoomList)
	registerGuarded(protocol.MethodRunRoomStatus, permissions.View, roomTarget, (*Server).runRoomStatus)
	registerMethod(protocol.MethodRunRoomPost, (*Server).runRoomPost)
	registerGuarded(protocol.MethodRunRoomDecide, permissions.Steer, roomMessageTarget, (*Server).runRoomDecide)

}

func (s *Server) rooms() (RoomService, *protocol.Error) {
	if s.cfg.Services.Rooms == nil {
		return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: "room service not configured"}
	}
	return s.cfg.Services.Rooms, nil
}
func roomTarget(s *Server, ctx context.Context, raw json.RawMessage) (permissions.Target, *protocol.Error) {
	var p struct {
		WorkspaceID string `json:"workspace_id"`
		RunID       string `json:"run_id"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return permissions.Target{}, invalidParams("invalid params: " + err.Error())
	}
	if p.WorkspaceID == "" || p.RunID == "" {
		return permissions.Target{}, invalidParams("workspace_id and run_id are required")
	}
	target, err := resolveRunTarget(ctx, s.cfg.Store, domain.RunID(p.RunID))
	if err != nil {
		return permissions.Target{}, rpcError(err)
	}
	if target.Workspace != domain.WorkspaceID(p.WorkspaceID) {
		return permissions.Target{}, &protocol.Error{Code: protocol.CodeNotFound, Message: "run is not in workspace"}
	}
	return target, nil
}
func roomMessageTarget(s *Server, ctx context.Context, raw json.RawMessage) (permissions.Target, *protocol.Error) {
	var p protocol.RunRoomDecideParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return permissions.Target{}, invalidParams("invalid params: " + err.Error())
	}
	if p.MessageID == "" {
		return permissions.Target{}, invalidParams("message_id is required")
	}
	msg, err := s.cfg.Store.GetRoomMessage(ctx, p.MessageID)
	if err != nil {
		return permissions.Target{}, rpcError(err)
	}
	target, err := resolveRunTarget(ctx, s.cfg.Store, msg.RunID)
	if err != nil {
		return permissions.Target{}, rpcError(err)
	}
	return target, nil
}

func (s *Server) runRoomList(ctx context.Context, _ domain.MemberID, raw json.RawMessage) (any, *protocol.Error) {
	rooms, perr := s.rooms()
	if perr != nil {
		return nil, perr
	}
	p, perr := decodeParams[protocol.RunRoomListParams](raw)
	if perr != nil {
		return nil, perr
	}
	if p.WorkspaceID == "" || p.RunID == "" {
		return nil, invalidParams("workspace_id and run_id are required")
	}
	if err := validateCollaborationPage(p.Before, p.Limit); err != nil {
		return nil, err
	}
	page, err := rooms.List(ctx, domain.WorkspaceID(p.WorkspaceID), domain.RunID(p.RunID), p.Before, p.Limit)
	if err != nil {
		return nil, collaborationRPCError(err)
	}
	return protocol.RoomMessagePageFromStore(page), nil
}

func (s *Server) runRoomStatus(ctx context.Context, member domain.MemberID, raw json.RawMessage) (any, *protocol.Error) {
	rooms, perr := s.rooms()
	if perr != nil {
		return nil, perr
	}
	p, perr := decodeParams[protocol.RunRoomStatusParams](raw)
	if perr != nil {
		return nil, perr
	}
	if p.WorkspaceID == "" || p.RunID == "" {
		return nil, invalidParams("workspace_id and run_id are required")
	}
	status, err := rooms.Status(ctx, domain.WorkspaceID(p.WorkspaceID), domain.RunID(p.RunID))
	if err != nil {
		return nil, collaborationRPCError(err)
	}
	result := protocol.RunRoomStatusResult{
		WorkspaceID: string(status.WorkspaceID), RunID: string(status.RunID),
		Protected: status.Protected, Watchers: []string{}, QueuedSteers: status.QueuedSteers,
	}
	if status.HasController {
		controller := &protocol.RoomController{
			MemberID: string(status.Controller.MemberID), Connected: status.Controller.Connected,
			AcquiredAt: collaborationTime(status.Controller.AcquiredAt),
		}
		if !status.Controller.ExpiresAt.IsZero() {
			controller.ExpiresAt = collaborationTime(status.Controller.ExpiresAt)
		}
		result.Controller = controller
	}
	if approvals := s.cfg.Services.Approvals; approvals != nil {
		for _, present := range approvals.Roster(domain.WorkspaceID(p.WorkspaceID), domain.RunID(p.RunID)) {
			if present.Member != "" {
				result.Watchers = append(result.Watchers, string(present.Member))
			}
		}
	}
	_ = member
	return result, nil
}

func collaborationTime(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func (s *Server) runRoomPost(ctx context.Context, member domain.MemberID, raw json.RawMessage) (any, *protocol.Error) {
	rooms, perr := s.rooms()
	if perr != nil {
		return nil, perr
	}
	p, perr := decodeParams[protocol.RunRoomPostParams](raw)
	if perr != nil {
		return nil, perr
	}
	if p.WorkspaceID == "" || p.RunID == "" || p.Body == "" || p.IdempotencyKey == "" {
		return nil, invalidParams("workspace_id, run_id, body, and idempotency_key are required")
	}
	if len(p.Body) > protocol.CollaborationMaxBodyBytes {
		return nil, invalidParams(fmt.Sprintf("body exceeds %d bytes", protocol.CollaborationMaxBodyBytes))
	}
	if len(p.IdempotencyKey) > protocol.CollaborationMaxIdempotencyKeyBytes {
		return nil, invalidParams("idempotency_key is too long")
	}
	if err := validateCollaborationAttachments(p.Attachments); err != nil {
		return nil, err
	}
	actor, err := resolveActor(ctx, s.cfg.Store, member)
	if err != nil {
		return nil, rpcError(err)
	}
	target, perr := roomTarget(s, ctx, raw)
	if perr != nil {
		return nil, perr
	}
	if p.Kind == protocol.RoomMessageSteerRequest {
		if checkErr := permissions.Check(permissions.Steer, actor, target); checkErr != nil {
			return nil, &protocol.Error{Code: protocol.CodeDenied, Message: protocol.MethodRunRoomPost + ": " + checkErr.Error()}
		}
	} else if actor.Role != domain.RoleAdmin && actor.Role != domain.RoleCollaborator {
		return nil, &protocol.Error{Code: protocol.CodeDenied, Message: protocol.MethodRunRoomPost + ": posting requires the collaborator role"}
	}
	input := collab.MessageInput{
		WorkspaceID: domain.WorkspaceID(p.WorkspaceID), RunID: domain.RunID(p.RunID), ActorID: member,
		Kind: store.RoomMessageKind(p.Kind), Body: p.Body, Attachments: append([]string(nil), p.Attachments...),
		CorrelationID: p.CorrelationID, IdempotencyKey: p.IdempotencyKey,
		ControllerSessionID: p.ControlSessionID, ControllerGeneration: p.ControlGeneration,
	}
	if p.Anchor != nil {
		input.Anchor = &store.RoomAnchor{Kind: p.Anchor.Kind, Path: p.Anchor.Path, StartLine: p.Anchor.StartLine, EndLine: p.Anchor.EndLine, TranscriptOffset: p.Anchor.TranscriptOffset}
	}
	result, err := rooms.Post(ctx, input)
	if err != nil {
		return nil, collaborationRPCError(err)
	}
	return roomMutationResult(result), nil
}

func (s *Server) runRoomDecide(ctx context.Context, member domain.MemberID, raw json.RawMessage) (any, *protocol.Error) {
	rooms, perr := s.rooms()
	if perr != nil {
		return nil, perr
	}
	p, perr := decodeParams[protocol.RunRoomDecideParams](raw)
	if perr != nil {
		return nil, perr
	}
	if p.MessageID == "" {
		return nil, invalidParams("message_id is required")
	}
	var (
		msg     *store.RoomMessage
		receipt collab.Receipt
		err     error
	)
	switch strings.ToLower(p.Decision) {
	case "approve":
		var result collab.Result
		result, err = rooms.ApproveNow(ctx, p.MessageID, member, p.ControlSessionID, p.ControlGeneration)
		msg, receipt = result.Message, result.Receipt
	case "deny":
		msg, err = rooms.Deny(ctx, p.MessageID, member, p.ControlSessionID, p.ControlGeneration)
	default:
		return nil, invalidParams(`decision must be "approve" or "deny"`)
	}
	if err != nil {
		return nil, collaborationRPCError(err)
	}
	if msg == nil {
		return nil, &protocol.Error{Code: protocol.CodeInternal, Message: "room service returned no message"}
	}
	return protocol.RunRoomDecideResult{Message: protocol.RoomMessageFromStore(msg), Receipt: string(receipt)}, nil
}

func roomMutationResult(result collab.Result) protocol.RunRoomPostResult {
	out := protocol.RunRoomPostResult{Receipt: string(result.Receipt)}
	if result.Message != nil {
		out.Message = protocol.RoomMessageFromStore(result.Message)
	}
	return out
}

func validateCollaborationPage(before string, limit int) *protocol.Error {
	if len(before) > protocol.CollaborationMaxCursorBytes {
		return invalidParams("before is too long")
	}
	if limit < 0 || limit > protocol.CollaborationMaxPageSize {
		return invalidParams(fmt.Sprintf("limit must be between 0 and %d", protocol.CollaborationMaxPageSize))
	}
	return nil
}

func validateCollaborationAttachments(refs []string) *protocol.Error {
	if len(refs) > protocol.CollaborationMaxAttachments {
		return invalidParams(fmt.Sprintf("attachments may contain at most %d references", protocol.CollaborationMaxAttachments))
	}
	for _, ref := range refs {
		if ref == "" || len(ref) > protocol.CollaborationMaxAttachmentBytes || strings.ContainsAny(ref, "\x00\r\n") {
			return invalidParams("attachment reference is invalid")
		}
	}
	return nil
}

func collaborationRPCError(err error) *protocol.Error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, store.ErrNotFound):
		return &protocol.Error{Code: protocol.CodeNotFound, Message: "collaboration object not found"}
	case errors.Is(err, permissions.ErrDenied), errors.Is(err, collab.ErrUnauthorizedDecision):
		return &protocol.Error{Code: protocol.CodeDenied, Message: "collaboration operation denied"}
	case errors.Is(err, collab.ErrInvalidRequest), errors.Is(err, collab.ErrInvalidAttachment), errors.Is(err, collab.ErrInvalidAnchor):
		return &protocol.Error{Code: protocol.CodeInvalidParams, Message: "invalid collaboration request"}
	default:
		return &protocol.Error{Code: protocol.CodeInternal, Message: "collaboration service error"}
	}
}
