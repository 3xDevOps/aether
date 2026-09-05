package sshd

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

func init() {
	registerMethod(protocol.MethodWorkspaceAdd, (*Server).workspaceAdd)
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
	if p.Name == "" || !p.Environment.Valid() {
		return nil, invalidParams("name and valid environment are required")
	}
	base := p.BaseBranch
	if base == "" {
		base = domain.DefaultBaseBranch
	}
	w := &domain.Workspace{Name: p.Name, BaseBranch: base, Environment: domain.WorkspaceEnvironment{
		Variables:   p.Environment.Variables,
		SetupPolicy: domain.SetupPolicy{Script: p.Environment.SetupPolicy.Script},
	}}
	if err := s.cfg.Store.CreateWorkspace(ctx, w); err != nil {
		return nil, rpcError(err)
	}
	return protocol.WorkspaceAddResult{Workspace: protocol.WorkspaceFromDomain(w)}, nil
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
	// Same read-then-write hazard as member.role: without this lock a
	// removal and a demotion racing each other can both see two admins.
	s.registerMu.Lock()
	defer s.registerMu.Unlock()
	id := domain.MemberID(p.MemberID)
	target, err := s.cfg.Store.GetMember(ctx, id)
	if err != nil {
		return nil, rpcError(err)
	}
	if target.Role == domain.RoleAdmin {
		admins, cerr := s.countAdmins(ctx)
		if cerr != nil {
			return nil, cerr
		}
		if admins <= 1 {
			return nil, &protocol.Error{Code: protocol.CodeDenied, Message: "refusing to delete the last admin"}
		}
	}
	if err := s.cfg.Store.DeleteMember(ctx, id); err != nil {
		return nil, rpcError(err)
	}
	// The home is bind-mounted into the terminal container; deleting the
	// tree under a container that refused to stop would leave a live
	// shell writing into a removed directory. Retain it for a later
	// cleanup instead.
	stopErr := s.cfg.Runs.StopTerminal(ctx, id)
	if stopErr != nil {
		slog.Warn("sshd: member terminal cleanup failed; retaining home", "member", id, "error", stopErr)
	}
	if stopErr == nil && s.cfg.Homes != nil {
		if err := s.cfg.Homes.Remove(id); err != nil {
			slog.Warn("sshd: member home cleanup failed", "member", id, "error", err)
		}
	}
	return struct{}{}, nil
}

// countAdmins reports how many members currently hold the admin role. It
// backs the last-admin invariant: the deployment must never lose its
// ability to administer itself, whether through removal or demotion.
func (s *Server) countAdmins(ctx context.Context) (int, *protocol.Error) {
	members, err := s.cfg.Store.ListMembers(ctx)
	if err != nil {
		return 0, rpcError(err)
	}
	admins := 0
	for _, m := range members {
		if m.Role == domain.RoleAdmin {
			admins++
		}
	}
	return admins, nil
}
