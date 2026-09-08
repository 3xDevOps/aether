package sshd

import (
	"context"
	"encoding/json"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/protocol"
)

func init() {
	registerGuarded(protocol.MethodWorkspaceSettings, permissions.WorkspaceAdmin, nil, (*Server).workspaceSettings)
	registerGuarded(protocol.MethodWorkspaceOrigin, permissions.Push, workspaceTarget, (*Server).workspaceOrigin)
	registerGuarded(protocol.MethodRunProtect, permissions.Protect, runTarget, (*Server).runProtect)
}

// workspaceSettings updates a workspace's settings (admin only; the guard
// has already checked WorkspaceAdmin). The change is stamped into the
// workspace timeline attributed to the caller.
func (s *Server) workspaceSettings(ctx context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	p, perr := decodeParams[protocol.WorkspaceSettingsParams](params)
	if perr != nil {
		return nil, perr
	}
	if p.WorkspaceID == "" {
		return nil, invalidParams("workspace_id is required")
	}
	if !domain.ValidSteerOthers(p.SteerOthers) {
		return nil, invalidParams(`steer_others must be "" or "admins_only"`)
	}
	id := domain.WorkspaceID(p.WorkspaceID)
	if err := s.cfg.Store.SetWorkspaceSteerOthers(ctx, id, p.SteerOthers); err != nil {
		return nil, rpcError(err)
	}
	ws, err := s.cfg.Store.GetWorkspace(ctx, id)
	if err != nil {
		return nil, rpcError(err)
	}
	setting := p.SteerOthers
	if setting == "" {
		setting = "default"
	}
	_, _ = s.cfg.Bus.Publish(ctx, events.Event{
		WorkspaceID: ws.ID,
		ActorID:     member,
		Payload: events.TimelinePayload{
			Kind:    events.TimelineNote,
			Message: "workspace settings: steer_others set to " + setting,
		},
	})
	return protocol.WorkspaceSettingsResult{Workspace: protocol.WorkspaceFromDomain(ws)}, nil
}

// workspaceOrigin sets the upstream git URL the workspace's run checkouts
// push to (collaborator or admin; the guard has already checked Push
// against the workspace). This is the one place an origin is normalized,
// so every caller records the same URL. The change is stamped into the
// workspace timeline attributed to the caller.
func (s *Server) workspaceOrigin(ctx context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	p, perr := decodeParams[protocol.WorkspaceOriginParams](params)
	if perr != nil {
		return nil, perr
	}
	if p.WorkspaceID == "" {
		return nil, invalidParams("workspace_id is required")
	}
	// The raw input is what gets validated: normalization would turn an
	// option-shaped scp form into an innocent https URL, and the caller
	// should hear that the input was refused.
	if !domain.ValidOrigin(p.Origin) {
		return nil, invalidParams("origin must be empty or a git URL (https://, http://, ssh://, git://, an absolute path, or user@host:path)")
	}
	origin := domain.NormalizeOrigin(p.Origin)
	id := domain.WorkspaceID(p.WorkspaceID)
	if err := s.cfg.Store.SetWorkspaceOrigin(ctx, id, origin); err != nil {
		return nil, rpcError(err)
	}
	ws, err := s.cfg.Store.GetWorkspace(ctx, id)
	if err != nil {
		return nil, rpcError(err)
	}
	note := "workspace origin cleared"
	if ws.Origin != "" {
		note = "workspace origin set to " + ws.Origin
	}
	_, _ = s.cfg.Bus.Publish(ctx, events.Event{
		WorkspaceID: ws.ID,
		ActorID:     member,
		Payload: events.TimelinePayload{
			Kind:    events.TimelineNote,
			Message: note,
		},
	})
	return protocol.WorkspaceOriginResult{Workspace: protocol.WorkspaceFromDomain(ws)}, nil
}

// runProtect toggles a run's protected flag (owner or admin; the guard has
// already checked Protect against the run). The change is stamped into the
// workspace timeline attributed to the caller.
func (s *Server) runProtect(ctx context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	p, perr := decodeParams[protocol.RunProtectParams](params)
	if perr != nil {
		return nil, perr
	}
	if p.RunID == "" {
		return nil, invalidParams("run_id is required")
	}
	id := domain.RunID(p.RunID)
	if err := s.cfg.Store.SetRunProtected(ctx, id, p.Protected); err != nil {
		return nil, rpcError(err)
	}
	run, err := s.cfg.Store.GetRun(ctx, id)
	if err != nil {
		return nil, rpcError(err)
	}
	_, _ = s.cfg.Bus.Publish(ctx, events.Event{
		WorkspaceID: run.WorkspaceID,
		RunID:       run.ID,
		ActorID:     member,
		Payload:     events.RunProtectedPayload{Protected: p.Protected},
	})
	msg := "run protection disabled"
	if run.Protected {
		msg = "run protection enabled"
	}
	_, _ = s.cfg.Bus.Publish(ctx, events.Event{
		WorkspaceID: run.WorkspaceID,
		RunID:       run.ID,
		ActorID:     member,
		Payload:     events.TimelinePayload{Kind: events.TimelineNote, Message: msg},
	})
	return protocol.RunResult{Run: protocol.RunFromDomain(run)}, nil
}
