package sshd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/control"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/memberhome"
	"github.com/3xDevOps/Aether/internal/permissions"
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
	if !m.Role.Admin() {
		return &protocol.Error{Code: protocol.CodeDenied, Message: method + " requires the admin role"}
	}
	return nil
}

// requireSuperAdmin refuses method, a server-wide operation, unless
// member is a super admin.
func (s *Server) requireSuperAdmin(ctx context.Context, member domain.MemberID, method string) *protocol.Error {
	return s.requireIn(ctx, member, permissions.ServerAdmin, "", method)
}

// requireIn refuses method unless member holds cap in workspace ws, or
// outside any workspace when ws is empty. A workspace member cannot reach
// is reported exactly as one that does not exist.
func (s *Server) requireIn(ctx context.Context, member domain.MemberID, cap permissions.Capability, ws domain.WorkspaceID, method string) *protocol.Error {
	err := checkIn(ctx, s.cfg.Store, member, cap, ws)
	if errors.Is(err, permissions.ErrDenied) {
		return &protocol.Error{Code: protocol.CodeDenied, Message: method + ": " + err.Error()}
	}
	if err != nil {
		return rpcError(err)
	}
	return nil
}

func (s *Server) workspaceAdd(ctx context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	if err := s.requireIn(ctx, member, permissions.CreateWorkspace, "", protocol.MethodWorkspaceAdd); err != nil {
		return nil, err
	}
	p, perr := decodeParams[protocol.WorkspaceAddParams](params)
	if perr != nil {
		return nil, perr
	}
	s.authorizationMu.Lock()
	defer s.authorizationMu.Unlock()
	if err := s.requireIn(ctx, member, permissions.CreateWorkspace, "", protocol.MethodWorkspaceAdd); err != nil {
		return nil, err
	}
	w, err := s.createWorkspace(ctx, p, "", member)
	if err != nil {
		return nil, err
	}
	return protocol.WorkspaceAddResult{Workspace: protocol.WorkspaceFromDomain(w)}, nil
}

func (s *Server) createWorkspace(ctx context.Context, p protocol.WorkspaceAddParams, origin string, owner domain.MemberID) (*domain.Workspace, *protocol.Error) {
	if p.Name == "" || !p.Environment.Valid() {
		return nil, invalidParams("name and valid environment are required")
	}
	base := p.BaseBranch
	if base == "" {
		base = domain.DefaultBaseBranch
	}
	w := &domain.Workspace{Name: p.Name, BaseBranch: base, Origin: origin, OwnerID: owner, Environment: domain.WorkspaceEnvironment{
		Variables:   p.Environment.Variables,
		SetupPolicy: domain.SetupPolicy{Script: p.Environment.SetupPolicy.Script},
	}}
	if err := s.cfg.Store.CreateWorkspace(ctx, w); err != nil {
		return nil, rpcError(err)
	}
	s.accessChanged()
	return w, nil
}

func (s *Server) workspaceDelete(ctx context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	p, perr := decodeParams[protocol.WorkspaceDeleteParams](params)
	if perr != nil {
		return nil, perr
	}
	if p.WorkspaceID == "" {
		return nil, invalidParams("workspace_id is required")
	}
	ws := domain.WorkspaceID(p.WorkspaceID)
	if err := s.requireIn(ctx, member, permissions.WorkspaceAdmin, ws, protocol.MethodWorkspaceDelete); err != nil {
		return nil, err
	}
	lock := s.workspaceLock(ws)
	if !lock.TryLock() {
		return nil, &protocol.Error{Code: protocol.CodeConflict, Message: "workspace operations are in progress; retry deletion when they finish"}
	}
	defer lock.Unlock()
	if !s.authorizationMu.TryLock() {
		return nil, &protocol.Error{Code: protocol.CodeConflict, Message: "session admission is in progress; retry workspace deletion when it finishes"}
	}
	defer s.authorizationMu.Unlock()
	if err := s.requireIn(ctx, member, permissions.WorkspaceAdmin, ws, protocol.MethodWorkspaceDelete); err != nil {
		return nil, err
	}
	if s.cfg.DeleteWorkspace == nil {
		return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: "workspace deletion is not configured"}
	}
	if err := s.cfg.DeleteWorkspace(ctx, ws, member); err != nil {
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
	workspaces := make([]domain.WorkspaceID, len(p.WorkspaceIDs))
	for i, w := range p.WorkspaceIDs {
		workspaces[i] = domain.WorkspaceID(w)
	}
	if perr := s.inviteWorkspaces(ctx, member, workspaces, protocol.MethodMemberInvite); perr != nil {
		return nil, perr
	}
	ttl := time.Duration(p.TTLSeconds) * time.Second
	code, expires, err := mintInvite(s.cfg.InvitesDir, ttl, member, workspaces)
	if err != nil {
		return nil, rpcError(err)
	}
	return protocol.MemberInviteResult{Code: code, ExpiresAt: expires.UTC().Format(time.RFC3339)}, nil
}

func (s *Server) memberRemove(ctx context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	if err := s.requireSuperAdmin(ctx, member, protocol.MethodMemberRemove); err != nil {
		return nil, err
	}
	p, perr := decodeParams[protocol.MemberRemoveParams](params)
	if perr != nil {
		return nil, perr
	}
	if p.MemberID == "" {
		return nil, invalidParams("member_id is required")
	}
	// The checks below refuse early, before the terminal stops; the
	// delete repeats them in its own transaction, so `aether-server
	// member role` cannot race it.
	s.registerMu.Lock()
	defer s.registerMu.Unlock()
	s.authorizationMu.Lock()
	defer s.authorizationMu.Unlock()
	if err := s.requireSuperAdmin(ctx, member, protocol.MethodMemberRemove); err != nil {
		return nil, err
	}
	id := domain.MemberID(p.MemberID)
	target, err := s.cfg.Store.GetMember(ctx, id)
	if err != nil {
		return nil, rpcError(err)
	}
	if target.Role == domain.RoleSuperAdmin {
		others, cerr := s.otherSuperAdmins(ctx, id)
		if cerr != nil {
			return nil, cerr
		}
		if others == 0 {
			return nil, &protocol.Error{Code: protocol.CodeDenied, Message: "refusing to delete the last super admin"}
		}
	}
	if perr = s.refuseOwner(ctx, target, "removing"); perr != nil {
		return nil, perr
	}
	ids, perr := s.rpcIdentityStore()
	if perr != nil {
		return nil, perr
	}
	devs, err := ids.ListDevices(ctx, "")
	if err != nil {
		return nil, rpcError(err)
	}
	var activeRuns []*domain.Run
	if s.cfg.Control != nil {
		var listErr error
		activeRuns, listErr = s.cfg.Store.ListActiveRuns(ctx)
		if listErr != nil {
			return nil, rpcError(listErr)
		}
	}
	// The assistant is fenced and stopped before the terminal lock is taken:
	// starting it waits on that lock while holding its own, so stopping it
	// under the lock would deadlock. Its files go once the member is gone.
	if err := s.fenceAssistant(ctx, id); err != nil {
		return nil, rpcError(fmt.Errorf("member.remove: %w", err))
	}
	deleted := false
	defer func() {
		if !deleted && s.cfg.Services.Assistant != nil {
			s.cfg.Services.Assistant.Unfence(id)
		}
	}()
	// Hold the terminal lifecycle lock through removal: EnsureTerminal must not
	// recreate an owner between stopping the terminal and deleting its home.
	// Lock order is terminal lifecycle -> cache; never call StopTerminal while
	// holding the cache lock.
	var keyErr error
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
			// The cascade alone would leave preferences in a restored copy of
			// the database.
			deleteMember := s.cfg.Store.DeleteMember
			if prefs := s.cfg.Services.Preferences; prefs != nil {
				deleteMember = prefs.DeleteMember
			}
			if err := deleteMember(ctx, id); err != nil {
				return memberGuardError(err, id, "delete", "removing")
			}
			deleted = true
			noteChange(ctx)
			if err := s.removeAssistant(ctx, id); err != nil {
				slog.Warn("sshd: member assistant cleanup failed", "member", id, "error", err)
			}
			if s.cfg.Signing != nil {
				keyErr = s.cfg.Signing.Remove(id)
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
	s.publishDroppedDevices(ctx, member, ids, devs)
	if keyErr != nil {
		return nil, rpcError(fmt.Errorf("member.remove: the member is removed, but its signing key is not: %w", keyErr))
	}
	return struct{}{}, nil
}

// otherSuperAdmins reports how many approved members other than except
// are super admins. It backs the last-super-admin invariant: the server
// must never lose its ability to administer itself, and a pending member
// cannot call it.
func (s *Server) otherSuperAdmins(ctx context.Context, except domain.MemberID) (int, *protocol.Error) {
	members, err := s.cfg.Store.ListMembers(ctx)
	if err != nil {
		return 0, rpcError(err)
	}
	supers := 0
	for _, m := range members {
		if m.Role == domain.RoleSuperAdmin && !m.Pending && m.ID != except {
			supers++
		}
	}
	return supers, nil
}

// refuseOwner refuses removing m while m owns workspaces: a workspace's
// owner is always an admin.
func (s *Server) refuseOwner(ctx context.Context, m *domain.Member, action string) *protocol.Error {
	all, err := s.cfg.Store.ListWorkspaces(ctx)
	if err != nil {
		return rpcError(err)
	}
	var owned []string
	for _, w := range all {
		if w.OwnerID == m.ID {
			owned = append(owned, fmt.Sprintf("%s (%s)", w.Name, w.ID))
		}
	}
	if len(owned) == 0 {
		return nil
	}
	return ownerRefusal(action, m.ID, owned)
}

func ownerRefusal(action string, id domain.MemberID, owned []string) *protocol.Error {
	return &protocol.Error{Code: protocol.CodeInvalidState, Message: fmt.Sprintf(
		"%s %s is refused while they own workspaces %s; give each a new owner first: aether workspace owner <workspace> <member-id>",
		action, id, strings.Join(owned, ", "))}
}

// inviteWorkspaces checks the workspaces an invitation from member grants:
// a super admin names any, or none; an admin names at least one, and
// only workspaces they own.
func (s *Server) inviteWorkspaces(ctx context.Context, member domain.MemberID, workspaces []domain.WorkspaceID, method string) *protocol.Error {
	caller, err := s.cfg.Store.GetMember(ctx, member)
	if err != nil {
		return rpcError(err)
	}
	if caller.Role != domain.RoleSuperAdmin && len(workspaces) == 0 {
		return invalidParams(method + ": an admin's invitation names at least one workspace they own (--workspace <workspace>)")
	}
	for _, ws := range workspaces {
		if perr := s.requireIn(ctx, member, permissions.WorkspaceAdmin, ws, method); perr != nil {
			return perr
		}
		if _, err := s.cfg.Store.GetWorkspace(ctx, ws); err != nil {
			return rpcError(err)
		}
	}
	return nil
}
