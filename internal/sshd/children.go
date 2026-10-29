package sshd

import (
	"context"
	"encoding/json"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// ChildService is the human side of child runs.
type ChildService interface {
	Settings(ctx context.Context, member domain.MemberID) (protocol.ChildSettings, error)
	SetSettings(ctx context.Context, member domain.MemberID, p protocol.ChildSettingsParams) (protocol.ChildSettings, error)
	Allow(ctx context.Context, actor *domain.Member, run domain.RunID, value string) (protocol.RunChildrenAllowResult, error)
	Detach(ctx context.Context, child domain.RunID) (protocol.Child, error)
	Children(ctx context.Context, run domain.RunID) (protocol.RunChildrenResult, error)
	Limits(ctx context.Context, ws domain.WorkspaceID) (protocol.ChildLimits, error)
	SetLimits(ctx context.Context, p protocol.WorkspaceChildLimitsParams) (protocol.ChildLimits, error)
}

func init() {
	registerMethod(protocol.MethodChildSettings, (*Server).childSettings)
	registerGuarded(protocol.MethodRunChildren, permissions.View, runTarget, (*Server).runChildren)
	registerMethod(protocol.MethodRunChildrenAllow, (*Server).runChildrenAllow)
	registerGuarded(protocol.MethodRunChildDetach, permissions.Kill, runTarget, (*Server).runChildDetach)
	registerMethod(protocol.MethodWorkspaceChildLimits, (*Server).workspaceChildLimits)
}

func (s *Server) children() (ChildService, *protocol.Error) {
	if s.cfg.Services.Children == nil {
		data, _ := json.Marshal(map[string]string{"reason": protocol.ErrorReasonChildrenUnavailable})
		return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: "child runs are unavailable on this server", Data: data}
	}
	return s.cfg.Services.Children, nil
}

func (s *Server) childSettings(ctx context.Context, member domain.MemberID, raw json.RawMessage) (any, *protocol.Error) {
	svc, perr := s.children()
	if perr != nil {
		return nil, perr
	}
	p, perr := decodeParams[protocol.ChildSettingsParams](raw)
	if perr != nil {
		return nil, perr
	}
	if p.Enabled == nil && p.Fallback == nil && p.SharedWhenMissing == nil && p.Nudge == "" {
		out, err := svc.Settings(ctx, member)
		return childResult(out, err)
	}
	out, err := svc.SetSettings(ctx, member, p)
	return childResult(out, err)
}

func (s *Server) runChildren(ctx context.Context, _ domain.MemberID, raw json.RawMessage) (any, *protocol.Error) {
	svc, perr := s.children()
	if perr != nil {
		return nil, perr
	}
	p, perr := decodeParams[protocol.RunChildrenParams](raw)
	if perr != nil {
		return nil, perr
	}
	out, err := svc.Children(ctx, domain.RunID(p.RunID))
	return childResult(out, err)
}

func (s *Server) runChildrenAllow(ctx context.Context, member domain.MemberID, raw json.RawMessage) (any, *protocol.Error) {
	svc, perr := s.children()
	if perr != nil {
		return nil, perr
	}
	p, perr := decodeParams[protocol.RunChildrenAllowParams](raw)
	if perr != nil {
		return nil, perr
	}
	if p.RunID == "" {
		return nil, invalidParams("run_id is required")
	}
	actor, err := s.cfg.Store.GetMember(ctx, member)
	if err != nil {
		return nil, rpcError(err)
	}
	out, err := svc.Allow(ctx, actor, domain.RunID(p.RunID), p.Children)
	return childResult(out, err)
}

func (s *Server) runChildDetach(ctx context.Context, _ domain.MemberID, raw json.RawMessage) (any, *protocol.Error) {
	svc, perr := s.children()
	if perr != nil {
		return nil, perr
	}
	p, perr := decodeParams[protocol.RunIDParams](raw)
	if perr != nil {
		return nil, perr
	}
	out, err := svc.Detach(ctx, domain.RunID(p.RunID))
	return childResult(out, err)
}

// workspaceChildLimits reads limits for any member and changes them for an
// admin.
func (s *Server) workspaceChildLimits(ctx context.Context, member domain.MemberID, raw json.RawMessage) (any, *protocol.Error) {
	svc, perr := s.children()
	if perr != nil {
		return nil, perr
	}
	p, perr := decodeParams[protocol.WorkspaceChildLimitsParams](raw)
	if perr != nil {
		return nil, perr
	}
	if p.WorkspaceID == "" {
		return nil, invalidParams("workspace_id is required")
	}
	if p.MaxLive == nil && p.MaxDepth == nil && p.MaxTotal == nil {
		out, err := svc.Limits(ctx, domain.WorkspaceID(p.WorkspaceID))
		return childResult(out, err)
	}
	actor, err := resolveActor(ctx, s.cfg.Store, member)
	if err != nil {
		return nil, rpcError(err)
	}
	if cerr := permissions.Check(permissions.WorkspaceAdmin, actor, permissions.Target{Workspace: domain.WorkspaceID(p.WorkspaceID)}); cerr != nil {
		return nil, &protocol.Error{Code: protocol.CodeDenied, Message: protocol.MethodWorkspaceChildLimits + ": " + cerr.Error()}
	}
	out, err := svc.SetLimits(ctx, p)
	return childResult(out, err)
}

func childResult(out any, err error) (any, *protocol.Error) {
	if err != nil {
		return nil, rpcError(err)
	}
	return out, nil
}

// setRunChildren records the launcher's child-run choice on the run it just
// started.
func (s *Server) setRunChildren(ctx context.Context, member domain.MemberID, run domain.RunID, value string) *protocol.Error {
	svc, perr := s.children()
	if perr != nil {
		return perr
	}
	actor, err := s.cfg.Store.GetMember(ctx, member)
	if err != nil {
		return rpcError(err)
	}
	if _, err = svc.Allow(ctx, actor, run, value); err != nil {
		return rpcError(err)
	}
	return nil
}
