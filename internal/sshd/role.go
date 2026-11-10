package sshd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/3xDevOps/Aether/internal/control"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

func init() {
	registerMethod(protocol.MethodMemberRole, (*Server).memberRole)
}

// memberRole sets a member's role. Only super admins may call it, and the
// last super admin can never be demoted: the server must keep at least
// one member able to administer it. A member who owns workspaces stays an
// admin until each has a new owner.
//
// Every permission check resolves the actor's role from the store at the
// moment of the call (resolveActor, gate.go), so a role change takes
// effect immediately on connections that are already open rather than at
// the target's next login.
//
// Approval and role are orthogonal: a pending member's role may be set,
// and they remain pending until an admin approves them.
func (s *Server) memberRole(ctx context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	if err := s.requireSuperAdmin(ctx, member, protocol.MethodMemberRole); err != nil {
		return nil, err
	}
	if err := s.requireApprovedCaller(ctx, protocol.MethodMemberRole, false); err != nil {
		return nil, err
	}
	p, perr := decodeParams[protocol.MemberRoleParams](params)
	if perr != nil {
		return nil, perr
	}
	if p.MemberID == "" || p.Role == "" {
		return nil, invalidParams("member_id and role are required")
	}
	role := domain.Role(p.Role)
	if !role.Valid() {
		return nil, invalidParams(fmt.Sprintf("unknown role %q; want viewer, collaborator, admin, or super_admin", p.Role))
	}
	// SetMemberRole checks the last super admin and owned workspaces in
	// its own transaction, so `aether-server member role` cannot race it.
	// registerMu still orders the change against invitations, workspace
	// owners and merges, which read roles in this process.
	s.registerMu.Lock()
	defer s.registerMu.Unlock()
	s.authorizationMu.Lock()
	defer s.authorizationMu.Unlock()
	if err := s.requireSuperAdmin(ctx, member, protocol.MethodMemberRole); err != nil {
		return nil, err
	}

	m, err := s.cfg.Store.GetMember(ctx, domain.MemberID(p.MemberID))
	if err != nil {
		return nil, rpcError(err)
	}
	if m.Role == role {
		return protocol.MemberRoleResult{Member: protocol.MemberFromDomain(m)}, nil
	}
	// An invitation grants its role and workspaces when accepted, and only
	// admins create them, so SetMemberRole revokes an admin's open
	// invitations with any demotion. createInvitation and
	// acceptInvitation hold registerMu too.
	var (
		ids  store.IdentityStore
		devs []*domain.Device
	)
	if m.Role == domain.RoleSuperAdmin || (m.Role == domain.RoleAdmin && !role.Admin()) {
		if ids, perr = s.rpcIdentityStore(); perr != nil {
			return nil, perr
		}
		if devs, err = ids.ListDevices(ctx, ""); err != nil {
			return nil, rpcError(err)
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
	was := m.Role
	update := func() error {
		var err error
		was, err = s.cfg.Store.SetMemberRole(ctx, m.ID, role)
		return err
	}
	if len(activeRuns) == 0 || s.cfg.Control == nil {
		if uerr := update(); uerr != nil {
			return nil, roleError(uerr, m.ID)
		}
	} else {
		updated := false
		for _, run := range activeRuns {
			if _, uerr := s.cfg.Control.AdmitRevoke(string(run.ID), control.RevocationPermission, func() error {
				if updated {
					return nil
				}
				if err := update(); err != nil {
					return err
				}
				updated = true
				return nil
			}); uerr != nil {
				return nil, roleError(uerr, m.ID)
			}
		}
		if !updated {
			if uerr := update(); uerr != nil {
				return nil, roleError(uerr, m.ID)
			}
		}
	}
	m.Role = role
	if ids != nil {
		s.publishDroppedDevices(ctx, member, ids, devs)
	}
	noteChange(ctx)
	s.accessChanged()
	s.publishRoleChange(ctx, member, m.ID)
	s.notifyDirectory()
	slog.Info("sshd: member role changed",
		"actor", member, "member", m.ID, "from", was, "to", role)
	return protocol.MemberRoleResult{Member: protocol.MemberFromDomain(m)}, nil
}

func roleError(err error, id domain.MemberID) *protocol.Error {
	var owns *store.OwnsWorkspacesError
	switch {
	case errors.Is(err, store.ErrLastSuperAdmin):
		return &protocol.Error{Code: protocol.CodeDenied, Message: "refusing to demote the last super admin"}
	case errors.As(err, &owns):
		return ownerRefusal("demoting", id, owns.Workspaces)
	}
	return rpcError(err)
}

// publishRoleChange tells the member their role changed, so their dashboard
// reloads it and the workspace roles derived from it.
func (s *Server) publishRoleChange(ctx context.Context, actor, changed domain.MemberID) {
	ev := events.Event{
		WorkspaceID: events.ServerScope,
		ActorID:     actor,
		Payload:     events.MemberAccessPayload{MemberID: changed, Action: events.AccessGranted},
	}
	if _, err := s.cfg.Bus.Publish(ctx, ev); err != nil {
		slog.Warn("sshd: publish "+string(ev.Payload.EventType()), "member", changed, "error", err)
	}
}
