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
