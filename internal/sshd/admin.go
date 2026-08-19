package sshd

import (
	"context"
	"encoding/json"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/protocol"
)

func init() {
	registerMethod(protocol.MethodWorkspaceAdd, (*Server).workspaceAdd)
	// Creating a session is a collaborator action, not an admin one: the
	// role table gives collaborators "launch runs", and a run needs a
	// session to live in. Viewers are still refused.
	registerGuarded(protocol.MethodSessionNew, permissions.Launch, nil, (*Server).sessionNew)
	registerMethod(protocol.MethodMemberInvite, (*Server).memberInvite)
	registerMethod(protocol.MethodMemberRemove, (*Server).memberRemove)
}

func (s *Server) requireAdmin(ctx context.Context, member domain.MemberID, method string) *protocol.Error {
	m, err := s.cfg.Store.GetMember(ctx, member)
	if err != nil {
		return rpcError(err)
	}
	if m.Role != domain.RoleAdmin {
		return &protocol.Error{Code: protocol.CodeDenied, Message: method + " requires the admin role"}
	}
	return nil
}

func (s *Server) workspaceAdd(ctx context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	if err := s.requireAdmin(ctx, member, protocol.MethodWorkspaceAdd); err != nil {
		return nil, err
	}
	p, perr := decodeParams[protocol.WorkspaceAddParams](params)
	if perr != nil {
		return nil, perr
	}
	if p.Name == "" || p.Image == "" {
		return nil, invalidParams("name and image are required")
	}
	w := &domain.Workspace{Name: p.Name, Image: p.Image, Env: p.Env, SetupScript: p.SetupScript}
	if err := s.cfg.Store.CreateWorkspace(ctx, w); err != nil {
		return nil, rpcError(err)
	}
	return protocol.WorkspaceAddResult{Workspace: protocol.WorkspaceFromDomain(w)}, nil
}

func (s *Server) sessionNew(ctx context.Context, _ domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	p, perr := decodeParams[protocol.SessionNewParams](params)
	if perr != nil {
		return nil, perr
	}
	if p.WorkspaceID == "" || p.Name == "" {
		return nil, invalidParams("workspace_id and name are required")
	}
	if _, err := s.cfg.Store.GetWorkspace(ctx, domain.WorkspaceID(p.WorkspaceID)); err != nil {
		return nil, rpcError(err)
	}
	base := p.BaseBranch
	if base == "" {
		base = "main"
	}
	sess := &domain.Session{WorkspaceID: domain.WorkspaceID(p.WorkspaceID), Name: p.Name, BaseBranch: base}
	if err := s.cfg.Store.CreateSession(ctx, sess); err != nil {
		return nil, rpcError(err)
	}
	return protocol.SessionNewResult{Session: protocol.SessionFromDomain(sess)}, nil
}

func (s *Server) memberInvite(ctx context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	if err := s.requireAdmin(ctx, member, protocol.MethodMemberInvite); err != nil {
		return nil, err
	}
	if s.cfg.InvitesDir == "" {
		return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: "invites are not configured"}
	}
	p, perr := decodeParams[protocol.MemberInviteParams](params)
	if perr != nil {
		return nil, perr
	}
	if p.TTLSeconds < 0 {
		return nil, invalidParams("ttl_seconds must not be negative")
	}
	ttl := time.Duration(p.TTLSeconds) * time.Second
	code, expires, err := mintInvite(s.cfg.InvitesDir, ttl)
	if err != nil {
		return nil, rpcError(err)
	}
	return protocol.MemberInviteResult{Code: code, ExpiresAt: expires.UTC().Format(time.RFC3339)}, nil
}

func (s *Server) memberRemove(ctx context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	if err := s.requireAdmin(ctx, member, protocol.MethodMemberRemove); err != nil {
		return nil, err
	}
	p, perr := decodeParams[protocol.MemberRemoveParams](params)
	if perr != nil {
		return nil, perr
	}
	if p.MemberID == "" {
		return nil, invalidParams("member_id is required")
	}
	id := domain.MemberID(p.MemberID)
	target, err := s.cfg.Store.GetMember(ctx, id)
	if err != nil {
		return nil, rpcError(err)
	}
	if target.Role == domain.RoleAdmin {
		members, lerr := s.cfg.Store.ListMembers(ctx)
		if lerr != nil {
			return nil, rpcError(lerr)
		}
		admins := 0
		for _, m := range members {
			if m.Role == domain.RoleAdmin {
				admins++
			}
		}
		if admins <= 1 {
			return nil, &protocol.Error{Code: protocol.CodeDenied, Message: "refusing to delete the last admin"}
		}
	}
	if err := s.cfg.Store.DeleteMember(ctx, id); err != nil {
		return nil, rpcError(err)
	}
	return struct{}{}, nil
}
