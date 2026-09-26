package sshd

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

func init() {
	registerMethod(protocol.MethodWorkspaceAdd, (*Server).workspaceAdd)
	registerMethod(protocol.MethodWorkspaceDelete, (*Server).workspaceDelete)
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

func (s *Server) workspaceDelete(ctx context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	if !s.authorizationMu.TryLock() {
		return nil, &protocol.Error{Code: protocol.CodeConflict, Message: "run or mission admission is in progress; retry workspace deletion when it finishes"}
	}
	defer s.authorizationMu.Unlock()
	if err := s.requireAdmin(ctx, member, protocol.MethodWorkspaceDelete); err != nil {
		return nil, err
	}
	p, perr := decodeParams[protocol.WorkspaceDeleteParams](params)
	if perr != nil {
		return nil, perr
	}
	if p.WorkspaceID == "" {
		return nil, invalidParams("workspace_id is required")
	}
	if s.cfg.DeleteWorkspace == nil {
		return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: "workspace deletion is not configured"}
	}
	if err := s.cfg.DeleteWorkspace(ctx, domain.WorkspaceID(p.WorkspaceID), member); err != nil {
		return nil, rpcError(err)
	}
	return protocol.WorkspaceDeleteResult{OK: true}, nil
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
	s.authorizationMu.Lock()
	defer s.authorizationMu.Unlock()
	if err := s.requireAdmin(ctx, member, protocol.MethodMemberRemove); err != nil {
		return nil, err
	}
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
	var activeRuns []*domain.Run
	if s.cfg.Control != nil {
		var listErr error
		activeRuns, listErr = s.cfg.Store.ListActiveRuns(ctx)
		if listErr != nil {
			return nil, rpcError(listErr)
		}
	}
	// Stop the bind-mounted terminal before deleting its member row. If
	// cleanup fails, retain the member as the durable recovery path.
	remove := func() error {
		if err := s.cfg.Runs.StopTerminal(ctx, id); err != nil {
			return fmt.Errorf("member.remove: stop terminal: %w", err)
		}
		return s.cfg.Store.DeleteMember(ctx, id)
	}
	if len(activeRuns) == 0 || s.cfg.Control == nil {
		if err := remove(); err != nil {
			return nil, rpcError(err)
		}
	} else {
		removed := false
		for _, run := range activeRuns {
			if _, removeErr := s.cfg.Control.AdmitRevoke(string(run.ID), func() error {
				if removed {
					return nil
				}
				if err := remove(); err != nil {
					return err
				}
				removed = true
				return nil
			}); removeErr != nil {
				return nil, rpcError(removeErr)
			}
		}
		if !removed {
			if err := remove(); err != nil {
				return nil, rpcError(err)
			}
		}
	}
	if s.cfg.Homes != nil {
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
