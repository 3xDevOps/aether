package sshd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/control"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

func init() {
	registerMethod(protocol.MethodWorkspaceAccessList, (*Server).workspaceAccessList)
	registerMethod(protocol.MethodWorkspaceAccessGrant, (*Server).workspaceAccessGrant)
	registerMethod(protocol.MethodWorkspaceAccessRevoke, (*Server).workspaceAccessRevoke)
	registerMethod(protocol.MethodWorkspaceOwnerSet, (*Server).workspaceOwnerSet)
}

func (s *Server) workspaceAccessList(ctx context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	p, perr := decodeParams[protocol.WorkspaceAccessParams](params)
	if perr != nil {
		return nil, perr
	}
	if p.WorkspaceID == "" {
		return nil, invalidParams("workspace_id is required")
	}
	ws := domain.WorkspaceID(p.WorkspaceID)
	if perr := s.requireIn(ctx, member, permissions.WorkspaceAdmin, ws, protocol.MethodWorkspaceAccessList); perr != nil {
		return nil, perr
	}
	return s.workspaceAccess(ctx, ws)
}

func (s *Server) workspaceAccess(ctx context.Context, ws domain.WorkspaceID) (protocol.WorkspaceAccessResult, *protocol.Error) {
	w, err := s.cfg.Store.GetWorkspace(ctx, ws)
	if err != nil {
		return protocol.WorkspaceAccessResult{}, rpcError(err)
	}
	grants, err := s.cfg.Store.ListWorkspaceGrants(ctx, ws)
	if err != nil {
		return protocol.WorkspaceAccessResult{}, rpcError(err)
	}
	out := protocol.WorkspaceAccessResult{WorkspaceID: string(w.ID), OwnerMemberID: string(w.OwnerID), Grants: []protocol.WorkspaceGrant{}}
	for _, g := range grants {
		out.Grants = append(out.Grants, protocol.WorkspaceGrant{MemberID: string(g.MemberID), GrantedBy: string(g.GrantedBy),
			CreatedAt: g.CreatedAt.UTC().Format(time.RFC3339)})
	}
	return out, nil
}

// grantee loads the member a grant or revocation in a workspace the
// caller administers acts on: a super admin acts on anyone, the
// workspace's owner on collaborators and viewers.
func (s *Server) grantee(ctx context.Context, caller domain.MemberID, id domain.MemberID, method string) (*domain.Member, *protocol.Error) {
	c, err := s.cfg.Store.GetMember(ctx, caller)
	if err != nil {
		return nil, rpcError(err)
	}
	m, err := s.cfg.Store.GetMember(ctx, id)
	if err != nil {
		return nil, rpcError(err)
	}
	if c.Role != domain.RoleSuperAdmin && m.Role.Admin() {
		return nil, &protocol.Error{Code: protocol.CodeDenied, Message: fmt.Sprintf(
			"%s: %s is %s; a workspace's owner grants and revokes collaborators and viewers, and only a super admin an admin", method, m.ID, m.Role)}
	}
	return m, nil
}

// workspaceAccessGrant lets members into a workspace. Granting its owner
// or a super admin, who reach it already, changes nothing. It holds
// registerMu, which member.role holds, so a grantee's role cannot change
// between the check and the grant.
func (s *Server) workspaceAccessGrant(ctx context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	const method = protocol.MethodWorkspaceAccessGrant
	p, perr := decodeParams[protocol.WorkspaceAccessGrantParams](params)
	if perr != nil {
		return nil, perr
	}
	if p.WorkspaceID == "" || len(p.MemberIDs) == 0 {
		return nil, invalidParams("workspace_id and member_ids are required")
	}
	ws := domain.WorkspaceID(p.WorkspaceID)
	s.registerMu.Lock()
	defer s.registerMu.Unlock()
	if perr := s.requireIn(ctx, member, permissions.WorkspaceAdmin, ws, method); perr != nil {
		return nil, perr
	}
	w, err := s.cfg.Store.GetWorkspace(ctx, ws)
	if err != nil {
		return nil, rpcError(err)
	}
	var grant []domain.MemberID
	for _, id := range p.MemberIDs {
		m, perr := s.grantee(ctx, member, domain.MemberID(id), method)
		if perr != nil {
			return nil, perr
		}
		if m.Pending {
			return nil, &protocol.Error{Code: protocol.CodeInvalidState, Message: fmt.Sprintf(
				"%s: member %s is pending; approve them first: aether member approve %s", method, m.ID, m.ID)}
		}
		if m.ID != w.OwnerID && m.Role != domain.RoleSuperAdmin && !slices.Contains(grant, m.ID) {
			grant = append(grant, m.ID)
		}
	}
	for _, id := range grant {
		if err := s.cfg.Store.GrantWorkspace(ctx, ws, id, member); err != nil {
			return nil, rpcError(err)
		}
		s.publishAccess(ctx, member, ws, id, events.AccessGranted)
	}
	if len(grant) > 0 {
		noteChange(ctx)
		s.accessChanged()
		slog.Info("sshd: workspace access granted", "actor", member, "workspace", ws, "members", grant)
	}
	return s.workspaceAccess(ctx, ws)
}

// workspaceAccessRevoke removes a member's grant on a workspace. It is
// refused while the member has a live session or a schedule there, and
// holds authorizationMu, so no session is admitted for them meanwhile.
// Leases on the workspace's live sessions are fenced as member.role
// fences them.
func (s *Server) workspaceAccessRevoke(ctx context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	const method = protocol.MethodWorkspaceAccessRevoke
	p, perr := decodeParams[protocol.WorkspaceAccessRevokeParams](params)
	if perr != nil {
		return nil, perr
	}
	if p.WorkspaceID == "" || p.MemberID == "" {
		return nil, invalidParams("workspace_id and member_id are required")
	}
	ws, id := domain.WorkspaceID(p.WorkspaceID), domain.MemberID(p.MemberID)
	s.registerMu.Lock()
	defer s.registerMu.Unlock()
	s.authorizationMu.Lock()
	defer s.authorizationMu.Unlock()
	if perr := s.requireIn(ctx, member, permissions.WorkspaceAdmin, ws, method); perr != nil {
		return nil, perr
	}
	if _, perr := s.grantee(ctx, member, id, method); perr != nil {
		return nil, perr
	}
	active, err := s.cfg.Store.ListActiveRuns(ctx)
	if err != nil {
		return nil, rpcError(err)
	}
	var inWorkspace []*domain.Run
	var sessions, schedules []string
	for _, r := range active {
		if _, extra := r.ExtraRepo(ws); r.WorkspaceID != ws && !extra {
			continue
		}
		inWorkspace = append(inWorkspace, r)
		if r.MemberID == id || r.HomeMember() == id || r.AccountMember() == id {
			sessions = append(sessions, string(r.ID))
		}
	}
	if ts, ok := s.cfg.Store.(store.TemplateStore); ok {
		all, err := ts.ListSchedules(ctx, ws)
		if err != nil {
			return nil, rpcError(err)
		}
		for _, sc := range all {
			if sc.MemberID == id {
				schedules = append(schedules, sc.Template)
			}
		}
	}
	if len(sessions) > 0 || len(schedules) > 0 {
		var uses []string
		if len(sessions) > 0 {
			uses = append(uses, "sessions "+strings.Join(sessions, ", ")+" (aether kill <session-id>, or aether close <session-id>)")
		}
		if len(schedules) > 0 {
			uses = append(uses, "schedules of templates "+strings.Join(schedules, ", ")+
				" (aether schedule delete <template> --workspace "+string(ws)+")")
		}
		return nil, &protocol.Error{Code: protocol.CodeConflict, Message: fmt.Sprintf(
			"%s: in use: member %s still has %s in workspace %s; stop them first", method, id, strings.Join(uses, " and "), ws)}
	}
	revoked := false
	revoke := func() error {
		if revoked {
			return nil
		}
		if err := s.cfg.Store.RevokeWorkspaceGrant(ctx, ws, id); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return &protocol.Error{Code: protocol.CodeNotFound, Message: fmt.Sprintf("%s: member %s has no grant on workspace %s", method, id, ws)}
			}
			return err
		}
		revoked = true
		return nil
	}
	if s.cfg.Control != nil {
		for _, r := range inWorkspace {
			if _, err := s.cfg.Control.AdmitRevoke(string(r.ID), control.RevocationPermission, revoke); err != nil {
				return nil, rpcError(err)
			}
		}
	}
	if err := revoke(); err != nil {
		return nil, rpcError(err)
	}
	noteChange(ctx)
	s.publishAccess(ctx, member, ws, id, events.AccessRevoked)
	s.accessChanged()
	slog.Info("sshd: workspace access revoked", "actor", member, "workspace", ws, "member", id)
	return s.workspaceAccess(ctx, ws)
}

// workspaceOwnerSet gives a workspace a new owner, an admin or super
// admin. The previous owner, unless a super admin, keeps reaching it as a
// grant, so their sessions there go on.
func (s *Server) workspaceOwnerSet(ctx context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	const method = protocol.MethodWorkspaceOwnerSet
	p, perr := decodeParams[protocol.WorkspaceOwnerSetParams](params)
	if perr != nil {
		return nil, perr
	}
	if p.WorkspaceID == "" || p.MemberID == "" {
		return nil, invalidParams("workspace_id and member_id are required")
	}
	ws := domain.WorkspaceID(p.WorkspaceID)
	s.registerMu.Lock()
	defer s.registerMu.Unlock()
	if perr := s.requireSuperAdmin(ctx, member, method); perr != nil {
		return nil, perr
	}
	w, err := s.cfg.Store.GetWorkspace(ctx, ws)
	if err != nil {
		return nil, rpcError(err)
	}
	owner, err := s.cfg.Store.GetMember(ctx, domain.MemberID(p.MemberID))
	if err != nil {
		return nil, rpcError(err)
	}
	switch {
	case !owner.Role.Admin():
		return nil, &protocol.Error{Code: protocol.CodeInvalidState, Message: fmt.Sprintf(
			"%s: a workspace's owner is an admin or a super admin, and %s is %s; change their role first: aether member role %s admin",
			method, owner.ID, owner.Role, owner.ID)}
	case owner.Pending:
		return nil, &protocol.Error{Code: protocol.CodeInvalidState, Message: fmt.Sprintf(
			"%s: member %s is pending; approve them first: aether member approve %s", method, owner.ID, owner.ID)}
	case owner.ID == w.OwnerID:
		return s.workspaceAccess(ctx, ws)
	}
	previous, err := s.cfg.Store.SetWorkspaceOwner(ctx, ws, owner.ID, member)
	if err != nil {
		return nil, rpcError(err)
	}
	noteChange(ctx)
	s.publishAccess(ctx, member, ws, owner.ID, events.AccessOwner)
	s.accessChanged()
	slog.Info("sshd: workspace owner set", "actor", member, "workspace", ws, "owner", owner.ID, "previous", previous)
	return s.workspaceAccess(ctx, ws)
}

// publishAccess publishes a committed access change in its workspace and
// to the member it changed; a failed publish is logged, as for
// member.device.
func (s *Server) publishAccess(ctx context.Context, actor domain.MemberID, ws domain.WorkspaceID, member domain.MemberID, action string) {
	p := events.WorkspaceAccessPayload{WorkspaceID: ws, MemberID: member, Action: action}
	for _, ev := range []events.Event{
		{WorkspaceID: ws, ActorID: actor, Payload: p},
		{WorkspaceID: events.ServerScope, ActorID: actor, Payload: events.MemberAccessPayload(p)},
	} {
		if _, err := s.cfg.Bus.Publish(ctx, ev); err != nil {
			slog.Warn("sshd: publish "+string(ev.Payload.EventType()), "workspace", ws, "member", member, "action", action, "error", err)
		}
	}
}
