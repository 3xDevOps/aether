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
	registerGuarded(protocol.MethodWorkspaceMirrorStatus, permissions.View, workspaceTarget, (*Server).workspaceMirrorStatus)
	registerGuarded(protocol.MethodWorkspaceMirrorConfigure, permissions.WorkspaceAdmin, workspaceTarget, (*Server).workspaceMirrorConfigure)
	registerGuarded(protocol.MethodWorkspaceMirrorRefresh, permissions.WorkspaceAdmin, workspaceTarget, (*Server).workspaceMirrorRefresh)
	registerGuarded(protocol.MethodWorkspaceMirrorAdopt, permissions.WorkspaceAdmin, workspaceTarget, (*Server).workspaceMirrorAdopt)
	registerGuarded(protocol.MethodWorkspaceMirrorDisable, permissions.WorkspaceAdmin, workspaceTarget, (*Server).workspaceMirrorDisable)
}

// MirrorService is the control-channel and launch view of
// internal/mirror.Service. Capture is used by the scheduler; control-channel
// reads require view access and mutations require workspace administration.
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
	if p.Branch == "" && p.Auth != string(domain.MirrorAuthGitHub) {
		return nil, invalidParams("branch is required")
	}
	auth := domain.MirrorAuth(p.Auth)
	if !auth.Valid() {
		return nil, invalidParams("auth must be public, deploy-key, or github")
	}
	if auth != domain.MirrorAuthGitHub && p.GitHubAccountID != 0 {
		return nil, invalidParams("github_account_id requires github authentication")
	}
	svc, perr := s.mirrors()
	if perr != nil {
		return nil, perr
	}
	s.authorizationMu.Lock()
	defer s.authorizationMu.Unlock()
	if err := s.requireAdmin(ctx, member, protocol.MethodWorkspaceMirrorConfigure); err != nil {
		return nil, err
	}
	request := mirrorservice.ConfigureRequest{
		SourceURL: p.SourceURL, Branch: p.Branch, Auth: auth, KnownHosts: p.KnownHosts,
	}
	if auth == domain.MirrorAuthGitHub {
		request, perr = s.resolveGitHubMirror(ctx, member, p.SourceURL, p.Branch, p.GitHubAccountID)
		if perr != nil {
			return nil, perr
		}
		if _, err := s.cfg.Runs.EnsureTerminal(ctx, member); err != nil {
			return nil, rpcError(err)
		}
		if _, err := s.cfg.Runs.ConnectGitHub(ctx, member); err != nil {
			return nil, rpcError(err)
		}
	}
	result, err := svc.Configure(ctx, domain.WorkspaceID(p.WorkspaceID), request)
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
	s.authorizationMu.Lock()
	defer s.authorizationMu.Unlock()
	if err := s.requireAdmin(ctx, member, protocol.MethodWorkspaceMirrorRefresh); err != nil {
		return nil, err
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
	s.authorizationMu.Lock()
	defer s.authorizationMu.Unlock()
	if err := s.requireAdmin(ctx, member, protocol.MethodWorkspaceMirrorAdopt); err != nil {
		return nil, err
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
	s.authorizationMu.Lock()
	defer s.authorizationMu.Unlock()
	if err := s.requireAdmin(ctx, member, protocol.MethodWorkspaceMirrorDisable); err != nil {
		return nil, err
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
