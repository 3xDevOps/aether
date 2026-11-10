package sshd

import (
	"context"
	"errors"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

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

func workspaceWire(actor permissions.Actor, w *domain.Workspace) protocol.Workspace {
	out := protocol.WorkspaceFromDomain(w)
	role, _ := actor.RoleIn(w.ID)
	out.Role = string(role)
	return out
}

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

// memberRunSnapshot is runSnapshot as member may see it: repos in
// workspaces they cannot reach are left out, and all extras are when their
// access cannot be read.
func (s *Server) memberRunSnapshot(ctx context.Context, member domain.MemberID, run *domain.Run) protocol.Run {
	snap := s.runSnapshot(run)
	actor, err := resolveActor(ctx, s.cfg.Store, member)
	if err != nil {
		actor = permissions.Actor{}
	}
	scopeRepos(actor, &snap)
	return snap
}

// visibleMembers reports which members actor sees. A super admin sees
// every member. Anyone else sees themselves, the super admins, every
// member who owns, was granted or has a session in a workspace they
// reach, and, as an admin, the pending members they approve. A merged-away
// id is seen when the member it became is.
func (s *Server) visibleMembers(ctx context.Context, actor permissions.Actor) (func(domain.MemberID) bool, error) {
	if actor.Role == domain.RoleSuperAdmin {
		return func(domain.MemberID) bool { return true }, nil
	}
	var reach []domain.WorkspaceID
	for _, set := range []map[domain.WorkspaceID]bool{actor.Access.Owned, actor.Access.Granted} {
		for ws := range set {
			if actor.Reaches(ws) {
				reach = append(reach, ws)
			}
		}
	}
	shared, err := s.cfg.Store.WorkspaceMembers(ctx, reach)
	if err != nil {
		return nil, err
	}
	seen := map[domain.MemberID]bool{actor.ID: true}
	for _, id := range shared {
		seen[id] = true
	}
	members, err := s.cfg.Store.ListMembers(ctx)
	if err != nil {
		return nil, err
	}
	for _, m := range members {
		if m.Role == domain.RoleSuperAdmin || (m.Pending && actor.Role == domain.RoleAdmin) {
			seen[m.ID] = true
		}
	}
	mergedInto := map[domain.MemberID]domain.MemberID{}
	if ms, ok := s.cfg.Store.(store.MergeStore); ok {
		merges, err := ms.ListMerges(ctx, "")
		if err != nil {
			return nil, err
		}
		for _, m := range merges {
			mergedInto[m.AbsorbedID] = m.SurvivorID
		}
	}
	return func(id domain.MemberID) bool {
		for hops := 0; !seen[id] && mergedInto[id] != "" && hops < 16; hops++ {
			id = mergedInto[id]
		}
		return seen[id]
	}, nil
}

// visibleMember is GetMember for caller: a member caller does not see
// reads exactly as one that does not exist.
func (s *Server) visibleMember(ctx context.Context, caller, id domain.MemberID) (*domain.Member, error) {
	actor, err := resolveActor(ctx, s.cfg.Store, caller)
	if err != nil {
		return nil, err
	}
	sees, err := s.visibleMembers(ctx, actor)
	if err != nil {
		return nil, err
	}
	if !sees(id) {
		return nil, permissions.ErrNoAccess
	}
	return s.cfg.Store.GetMember(ctx, id)
}
