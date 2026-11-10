package sshd

import (
	"context"
	"errors"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// reachableWorkspaces is every workspace actor reaches, in ListWorkspaces
// order.
func (s *Server) reachableWorkspaces(ctx context.Context, actor permissions.Actor) ([]*domain.Workspace, error) {
	all, err := s.cfg.Store.ListWorkspaces(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*domain.Workspace, 0, len(all))
	for _, w := range all {
		if actor.Reaches(w.ID) {
			out = append(out, w)
		}
	}
	return out, nil
}

// reachableIDs restricts a store query to actor's workspaces: nil for a
// super admin, who reaches all of them, and otherwise the reachable IDs,
// empty when there are none.
func (s *Server) reachableIDs(ctx context.Context, actor permissions.Actor) ([]domain.WorkspaceID, error) {
	if actor.Role == domain.RoleSuperAdmin {
		return nil, nil
	}
	list, err := s.reachableWorkspaces(ctx, actor)
	if err != nil {
		return nil, err
	}
	ids := make([]domain.WorkspaceID, 0, len(list))
	for _, w := range list {
		ids = append(ids, w.ID)
	}
	return ids, nil
}

// superAdmin reports whether member sees every member's server-wide
// figures.
func (s *Server) superAdmin(ctx context.Context, member domain.MemberID) bool {
	actor, err := resolveActor(ctx, s.cfg.Store, member)
	return err == nil && permissions.Check(permissions.ServerAdmin, actor, permissions.Target{}) == nil
}

// checkError is the wire error of a failed permissions.Check in method. An
// unreachable workspace reads exactly as a missing one.
func checkError(method string, err error) *protocol.Error {
	if errors.Is(err, permissions.ErrNoAccess) {
		return rpcError(err)
	}
	return &protocol.Error{Code: protocol.CodeDenied, Message: method + ": " + err.Error()}
}

// workspaceWire is w as actor sees it, with the role they act with there.
func workspaceWire(actor permissions.Actor, w *domain.Workspace) protocol.Workspace {
	out := protocol.WorkspaceFromDomain(w)
	role, _ := actor.RoleIn(w.ID)
	out.Role = string(role)
	return out
}

// scopeRepos drops from a run snapshot the extra repositories in
// workspaces actor does not reach.
func scopeRepos(actor permissions.Actor, run *protocol.Run) {
	if len(run.Repos) == 0 {
		return
	}
	kept := run.Repos[:0]
	for _, r := range run.Repos {
		if r.Primary || actor.Reaches(domain.WorkspaceID(r.WorkspaceID)) {
			kept = append(kept, r)
		}
	}
	run.Repos = kept
}
