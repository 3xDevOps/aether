package sshd

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/gitengine"
	mirrorservice "github.com/3xDevOps/Aether/internal/mirror"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/protocol"
)

func init() {
	registerGuarded(protocol.MethodWorkspaceImport, permissions.WorkspaceAdmin, nil, (*Server).workspaceImport)
}

// workspaceImport creates exactly one workspace per request. Once creation has
// committed, all subsequent failures are result data, not RPC errors: the caller
// must retain the workspace ID and repair its mirror rather than create again.
func (s *Server) workspaceImport(ctx context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	p, decodeErr := decodeParams[protocol.WorkspaceImportParams](params)
	if decodeErr != nil {
		return nil, decodeErr
	}
	if p.SourceURL == "" || (p.BaseBranch == "" && p.Auth != string(domain.MirrorAuthGitHub)) {
		return nil, invalidParams("source_url and base_branch are required (GitHub imports may use the default branch)")
	}
	auth := domain.MirrorAuth(p.Auth)
	if !auth.Valid() {
		return nil, invalidParams("auth must be public, deploy-key, or github")
	}
	if auth != domain.MirrorAuthGitHub && p.GitHubAccountID != 0 {
		return nil, invalidParams("github_account_id requires github authentication")
	}
	// Validate before normalizing, just as workspace.origin does. In particular,
	// the read-only mirror source never supplies a missing checkout Origin.
	if !domain.ValidOrigin(p.Origin) {
		return nil, invalidParams("origin must be empty or a git URL")
	}
	svc, perr := s.mirrors()
	if perr != nil {
		return nil, perr
	}
	s.authorizationMu.Lock()
	defer s.authorizationMu.Unlock()
	if adminErr := s.requireAdmin(ctx, member, protocol.MethodWorkspaceImport); adminErr != nil {
		return nil, adminErr
	}
	request := mirrorservice.ConfigureRequest{
		SourceURL: p.SourceURL, Branch: p.BaseBranch, Auth: auth, KnownHosts: p.KnownHosts,
	}
	if auth == domain.MirrorAuthGitHub {
		request, perr = s.resolveGitHubMirror(ctx, member, p.SourceURL, p.BaseBranch, p.GitHubAccountID)
		if perr != nil {
			return nil, perr
		}
		if _, err := s.cfg.Runs.EnsureTerminal(ctx, member); err != nil {
			return nil, rpcError(err)
		}
		if _, err := s.cfg.Runs.ConnectGitHub(ctx, member); err != nil {
			return nil, rpcError(err)
		}
		p.BaseBranch = request.Branch
	}
	workspace, perr := s.createWorkspace(ctx, protocol.WorkspaceAddParams{
		Name: p.Name, Environment: p.Environment, BaseBranch: p.BaseBranch,
	}, domain.NormalizeOrigin(p.Origin))
	if perr != nil {
		return nil, perr
	}
	result := protocol.WorkspaceImportResult{Workspace: protocol.WorkspaceFromDomain(workspace), Created: true}
	_, _ = s.cfg.Bus.Publish(ctx, events.Event{
		WorkspaceID: workspace.ID, ActorID: member,
		Payload: events.TimelinePayload{Kind: events.TimelineNote, Message: "workspace created for remote import"},
	})
	configured, err := svc.Configure(ctx, workspace.ID, request)
	if err != nil {
		return s.workspaceImportResult(ctx, svc, result, configured, err), nil
	}
	s.publishMirrorTimeline(ctx, member, "configured", configured.Mirror)
	// A newly generated deploy key must first be installed upstream. Do not
	// report a predictable authentication failure as a failed import, or rotate
	// the key by reconfiguring on verification: use mirror.refresh afterward.
	if auth == domain.MirrorAuthDeployKey {
		return s.workspaceImportResult(ctx, svc, result, configured, nil), nil
	}
	fetched, err := svc.Refresh(ctx, workspace.ID)
	if err == nil {
		s.publishMirrorTimeline(ctx, member, "refreshed", fetched.Mirror)
	}
	// Refresh only observes the initial candidate. Explicit generation adoption
	// remains a separate administrator action through workspace.mirror.adopt.
	return s.workspaceImportResult(ctx, svc, result, fetched, err), nil
}

func (s *Server) workspaceImportResult(ctx context.Context, svc MirrorService, result protocol.WorkspaceImportResult, state mirrorservice.Result, cause error) protocol.WorkspaceImportResult {
	if cause != nil {
		result.Error = cause.Error()
		// Prefer returned Git state even when persistence failed. Only fall back
		// to stored status if the service could not return state at all.
		if state.Mirror.WorkspaceID == "" {
			statusCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			stored, err := svc.Status(statusCtx, domain.WorkspaceID(result.Workspace.ID))
			cancel()
			if err == nil {
				state = stored
			} else {
				var mirrorErr *gitengine.MirrorError
				if !errors.As(err, &mirrorErr) || mirrorErr.Kind != gitengine.MirrorErrorNotConfigured {
					result.Error = errors.Join(cause, err).Error()
				}
			}
		}
	}
	result.Mirror = protocol.WorkspaceMirrorResultFromDomain(state.Mirror, state.Mirror.WorkspaceID != "", state.PublicKey, state.Warning)
	return result
}
