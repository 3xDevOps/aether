package sshd

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/3xDevOps/Aether/internal/control"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/memberhome"
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
	w, err := s.createWorkspace(ctx, p, "")
	if err != nil {
		return nil, err
	}
	return protocol.WorkspaceAddResult{Workspace: protocol.WorkspaceFromDomain(w)}, nil
}

func (s *Server) createWorkspace(ctx context.Context, p protocol.WorkspaceAddParams, origin string) (*domain.Workspace, *protocol.Error) {
	if p.Name == "" || !p.Environment.Valid() {
		return nil, invalidParams("name and valid environment are required")
	}
	base := p.BaseBranch
	if base == "" {
		base = domain.DefaultBaseBranch
	}
	w := &domain.Workspace{Name: p.Name, BaseBranch: base, Origin: origin, Environment: domain.WorkspaceEnvironment{
		Variables:   p.Environment.Variables,
		SetupPolicy: domain.SetupPolicy{Script: p.Environment.SetupPolicy.Script},
	}}
	if err := s.cfg.Store.CreateWorkspace(ctx, w); err != nil {
		return nil, rpcError(err)
	}
	return w, nil
}

func (s *Server) workspaceDelete(ctx context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
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
	lock := s.workspaceLock(domain.WorkspaceID(p.WorkspaceID))
	if !lock.TryLock() {
		return nil, &protocol.Error{Code: protocol.CodeConflict, Message: "workspace operations are in progress; retry deletion when they finish"}
	}
	defer lock.Unlock()
	if !s.authorizationMu.TryLock() {
		return nil, &protocol.Error{Code: protocol.CodeConflict, Message: "run or mission admission is in progress; retry workspace deletion when it finishes"}
	}
	defer s.authorizationMu.Unlock()
	if err := s.requireAdmin(ctx, member, protocol.MethodWorkspaceDelete); err != nil {
		return nil, err
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
	if err := s.requireApprovedCaller(ctx, protocol.MethodMemberInvite, true); err != nil {
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
	// Hold the terminal lifecycle lock through removal: EnsureTerminal must not
	// recreate an owner between stopping the terminal and deleting its home.
	// Lock order is terminal lifecycle -> cache; never call StopTerminal while
	// holding the cache lock.
	remove := func() error {
		stopped := false
		err := s.cfg.Runs.WithStoppedTerminal(ctx, id, func() error {
			stopped = true
			if s.cfg.Homes != nil {
				unlock := s.cfg.Homes.LockCaches(id)
				defer unlock()
				// Even legacy homes need a discoverable retry owner before their
				// member row disappears, including for saved image cleanup.
				if err := s.cfg.Homes.EnsureCacheMetadata(id, memberhome.CachePoolTerminal); err != nil {
					slog.Warn("sshd: member cleanup marker failed", "member", id, "error", err)
					return &protocol.Error{Code: protocol.CodeUnavailable, Message: "member.remove: cleanup ownership could not be preserved; member was not removed"}
				}
			}
			if err := s.cfg.Store.DeleteMember(ctx, id); err != nil {
				return err
			}
			if s.cfg.Homes != nil {
				if err := s.cfg.Homes.Remove(ctx, id); err != nil {
					slog.Warn("sshd: member home cleanup failed", "member", id, "error", err)
					if markerErr := s.cfg.Homes.SetCacheCleanupError(id, memberhome.CachePoolTerminal, "Member home cleanup failed"); markerErr != nil {
						slog.Warn("sshd: member cleanup cause persistence failed", "member", id, "error", markerErr)
					}
				}
			}
			return nil
		})
		if err != nil && !stopped {
			return fmt.Errorf("member.remove: stop terminal: %w", err)
		}
		return err
	}
	if len(activeRuns) == 0 || s.cfg.Control == nil {
		if err := remove(); err != nil {
			return nil, rpcError(err)
		}
	} else {
		removed := false
		for _, run := range activeRuns {
			if _, removeErr := s.cfg.Control.AdmitRevoke(string(run.ID), control.RevocationPermission, func() error {
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
	s.closeMemberConns(id)
	s.notifyDirectory()
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
