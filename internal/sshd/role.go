package sshd

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

func init() {
	registerMethod(protocol.MethodMemberRole, (*Server).memberRole)
}

// memberRole sets a member's role. Only admins may call it, and the last
// admin can never be demoted: the deployment must keep at least one
// member able to administer it.
//
// Every permission check resolves the actor's role from the store at the
// moment of the call (resolveActor, gate.go), so a role change takes
// effect immediately on connections that are already open rather than at
// the target's next login.
//
// Approval and role are orthogonal: a pending member's role may be set,
// and they remain pending until an admin approves them.
func (s *Server) memberRole(ctx context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	if err := s.requireAdmin(ctx, member, protocol.MethodMemberRole); err != nil {
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
		return nil, invalidParams(fmt.Sprintf("unknown role %q; want viewer, collaborator, or admin", p.Role))
	}
	// The last-admin check below reads the member table and then writes to
	// it. Without serialization two admins demoting each other at once
	// would both count two admins, both proceed, and leave the deployment
	// with none and no way back. registerMu is the same lock bootstrap
	// uses to keep its own read-then-write of this table honest.
	s.registerMu.Lock()
	defer s.registerMu.Unlock()
	m, err := s.cfg.Store.GetMember(ctx, domain.MemberID(p.MemberID))
	if err != nil {
		return nil, rpcError(err)
	}
	if m.Role == role {
		return protocol.MemberRoleResult{Member: protocol.MemberFromDomain(m)}, nil
	}
	if m.Role == domain.RoleAdmin {
		admins, cerr := s.countAdmins(ctx)
		if cerr != nil {
			return nil, cerr
		}
		if admins <= 1 {
			return nil, &protocol.Error{Code: protocol.CodeDenied, Message: "refusing to demote the last admin"}
		}
	}
	was := m.Role
	m.Role = role
	if uerr := s.cfg.Store.UpdateMember(ctx, m); uerr != nil {
		return nil, rpcError(uerr)
	}
	slog.Info("sshd: member role changed",
		"actor", member, "member", m.ID, "from", was, "to", role)
	return protocol.MemberRoleResult{Member: protocol.MemberFromDomain(m)}, nil
}
