package sshd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/gitengine"
	mirrorservice "github.com/3xDevOps/Aether/internal/mirror"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/protocol"
)

func init() {
	registerGuarded(protocol.MethodWorkspaceMirrorStatus, permissions.WorkspaceAdmin, nil, (*Server).workspaceMirrorStatus)
	registerGuarded(protocol.MethodWorkspaceMirrorConfigure, permissions.WorkspaceAdmin, nil, (*Server).workspaceMirrorConfigure)
	registerGuarded(protocol.MethodWorkspaceMirrorRefresh, permissions.WorkspaceAdmin, nil, (*Server).workspaceMirrorRefresh)
	registerGuarded(protocol.MethodWorkspaceMirrorAdopt, permissions.WorkspaceAdmin, nil, (*Server).workspaceMirrorAdopt)
	registerGuarded(protocol.MethodWorkspaceMirrorDisable, permissions.WorkspaceAdmin, nil, (*Server).workspaceMirrorDisable)
}

// MirrorService is the control-channel and launch view of
// internal/mirror.Service. Capture is used by the scheduler; the remaining
// methods are exposed through the admin-only control channel.
type MirrorService interface {
	Configure(context.Context, domain.WorkspaceID, mirrorservice.ConfigureRequest) (mirrorservice.Result, error)
	Status(context.Context, domain.WorkspaceID) (mirrorservice.Result, error)
	Refresh(context.Context, domain.WorkspaceID) (mirrorservice.Result, error)
	Adopt(context.Context, domain.WorkspaceID, int64) (mirrorservice.Result, error)
	Disable(context.Context, domain.WorkspaceID) (mirrorservice.Result, error)
	Capture(context.Context, domain.WorkspaceID, string) (mirrorservice.CaptureResult, error)
}

func (s *Server) mirrors() (MirrorService, *protocol.Error) {
	if s.cfg.Services.Mirrors == nil {
		return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: "workspace mirror administration is not available"}
	}
	return s.cfg.Services.Mirrors, nil
}

func (s *Server) workspaceMirrorStatus(ctx context.Context, _ domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	p, perr := decodeParams[protocol.WorkspaceMirrorParams](params)
	if perr != nil {
		return nil, perr
	}
	if p.WorkspaceID == "" {
		return nil, invalidParams("workspace_id is required")
	}
	svc, perr := s.mirrors()
	if perr != nil {
		return nil, perr
	}
	result, err := svc.Status(ctx, domain.WorkspaceID(p.WorkspaceID))
	if err != nil {
		var mirrorErr *gitengine.MirrorError
		if errors.As(err, &mirrorErr) && mirrorErr != nil && mirrorErr.Kind == gitengine.MirrorErrorNotConfigured {
			return protocol.WorkspaceMirrorResult{Enabled: false}, nil
		}
		return nil, rpcError(err)
	}
	return protocol.WorkspaceMirrorResultFromDomain(result.Mirror, true, result.PublicKey, result.Warning), nil
}

func (s *Server) workspaceMirrorConfigure(ctx context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	p, perr := decodeParams[protocol.WorkspaceMirrorConfigureParams](params)
	if perr != nil {
		return nil, perr
	}
	if p.WorkspaceID == "" {
		return nil, invalidParams("workspace_id is required")
	}
	if p.SourceURL == "" {
		return nil, invalidParams("source_url is required")
	}
	if p.Branch == "" {
		return nil, invalidParams("branch is required")
	}
	auth := domain.MirrorAuth(p.Auth)
	if !auth.Valid() {
		return nil, invalidParams("auth must be public or deploy-key")
	}
	svc, perr := s.mirrors()
	if perr != nil {
		return nil, perr
	}
	result, err := svc.Configure(ctx, domain.WorkspaceID(p.WorkspaceID), mirrorservice.ConfigureRequest{
		SourceURL:  p.SourceURL,
		Branch:     p.Branch,
		Auth:       auth,
		KnownHosts: p.KnownHosts,
	})
	if err != nil {
		return nil, rpcError(err)
	}
	s.publishMirrorTimeline(ctx, member, "configured", result.Mirror)
	return protocol.WorkspaceMirrorResultFromDomain(result.Mirror, true, result.PublicKey, result.Warning), nil
}

func (s *Server) workspaceMirrorRefresh(ctx context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	p, perr := decodeParams[protocol.WorkspaceMirrorParams](params)
	if perr != nil {
		return nil, perr
	}
	if p.WorkspaceID == "" {
		return nil, invalidParams("workspace_id is required")
	}
	svc, perr := s.mirrors()
	if perr != nil {
		return nil, perr
	}
	result, err := svc.Refresh(ctx, domain.WorkspaceID(p.WorkspaceID))
	if err != nil {
		return nil, rpcError(err)
	}
	s.publishMirrorTimeline(ctx, member, "refreshed", result.Mirror)
	return protocol.WorkspaceMirrorResultFromDomain(result.Mirror, true, result.PublicKey, result.Warning), nil
}

func (s *Server) workspaceMirrorAdopt(ctx context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	p, perr := decodeParams[protocol.WorkspaceMirrorAdoptParams](params)
	if perr != nil {
		return nil, perr
	}
	if p.WorkspaceID == "" {
		return nil, invalidParams("workspace_id is required")
	}
	if p.Generation <= 0 {
		return nil, invalidParams("generation must be greater than zero")
	}
	svc, perr := s.mirrors()
	if perr != nil {
		return nil, perr
	}
	result, err := svc.Adopt(ctx, domain.WorkspaceID(p.WorkspaceID), p.Generation)
	if err != nil {
		return nil, rpcError(err)
	}
	s.publishMirrorTimeline(ctx, member, "adopted", result.Mirror)
	return protocol.WorkspaceMirrorResultFromDomain(result.Mirror, true, result.PublicKey, result.Warning), nil
}

func (s *Server) workspaceMirrorDisable(ctx context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	p, perr := decodeParams[protocol.WorkspaceMirrorParams](params)
	if perr != nil {
		return nil, perr
	}
	if p.WorkspaceID == "" {
		return nil, invalidParams("workspace_id is required")
	}
	svc, perr := s.mirrors()
	if perr != nil {
		return nil, perr
	}
	result, err := svc.Disable(ctx, domain.WorkspaceID(p.WorkspaceID))
	if err != nil {
		return nil, rpcError(err)
	}
	s.publishMirrorTimeline(ctx, member, "disabled", result.Mirror)
	return protocol.WorkspaceMirrorResultFromDomain(result.Mirror, false, result.PublicKey, result.Warning), nil
}

func (s *Server) publishMirrorTimeline(ctx context.Context, actor domain.MemberID, action string, state domain.WorkspaceMirror) {
	commit := state.AcceptedCommit
	if commit == "" {
		commit = state.ObservedCommit
	}
	message := fmt.Sprintf("workspace mirror %s: source=%s branch=%s generation=%d commit=%s", action, state.SourceURL, state.Branch, state.Generation, commit)
	_, _ = s.cfg.Bus.Publish(ctx, events.Event{
		WorkspaceID: state.WorkspaceID,
		ActorID:     actor,
		Payload:     events.TimelinePayload{Kind: events.TimelineNote, Message: message},
	})
}

var _ MirrorService = (*mirrorservice.Service)(nil)
