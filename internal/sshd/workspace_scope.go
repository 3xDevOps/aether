package sshd

import (
	"context"
	"errors"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
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

// reachesRun reports whether actor reaches every workspace of run, its
// extra repositories' included. A run a member does not reach does not
// exist for them.
func reachesRun(actor permissions.Actor, run *domain.Run) bool {
	return reachesAll(actor, runWorkspaces(run))
}

// reachableRun is GetRun for member: a run they do not reach in every
// workspace is permissions.ErrNoAccess, which reads as a missing run.
func (s *Server) reachableRun(ctx context.Context, member domain.MemberID, id domain.RunID) (*domain.Run, error) {
	actor, err := resolveActor(ctx, s.cfg.Store, member)
	if err != nil {
		return nil, err
	}
	run, err := s.cfg.Store.GetRun(ctx, id)
	if err != nil {
		return nil, err
	}
	if !reachesRun(actor, run) {
		return nil, permissions.ErrNoAccess
	}
	return run, nil
}

func reachesAll(actor permissions.Actor, workspaces []domain.WorkspaceID) bool {
	for _, ws := range workspaces {
		if !actor.Reaches(ws) {
			return false
		}
	}
	return true
}

// runScopes caches the workspaces of the runs one reader's events name. A
// run's extra repositories are stored with the run and never change.
type runScopes struct {
	st   store.Store
	runs map[domain.RunID][]domain.WorkspaceID
}

func newRunScopes(st store.Store) *runScopes {
	return &runScopes{st: st, runs: map[domain.RunID][]domain.WorkspaceID{}}
}

// reaches reports whether actor reaches every workspace of run id. A run
// that no longer exists, or cannot be read, is reached by super admins
// alone: the extra repositories it had are no longer known.
func (c *runScopes) reaches(ctx context.Context, actor permissions.Actor, id domain.RunID) bool {
	if actor.Role == domain.RoleSuperAdmin {
		return true
	}
	workspaces, ok := c.runs[id]
	if !ok {
		run, err := c.st.GetRun(ctx, id)
		if err != nil {
			return false
		}
		workspaces = runWorkspaces(run)
		c.runs[id] = workspaces
	}
	return reachesAll(actor, workspaces)
}

// eventScope is what one read of the event log may show actor.
func (s *Server) eventScope(ctx context.Context, actor permissions.Actor) events.Scope {
	runs := newRunScopes(s.cfg.Store)
	var sees func(domain.MemberID) bool
	return events.Scope{
		Workspace: actor.Reaches,
		Run:       func(id domain.RunID) bool { return runs.reaches(ctx, actor, id) },
		Sees: func(id domain.MemberID) bool {
			if sees == nil {
				sees = s.memberSight(ctx, actor)
			}
			return sees(id)
		},
	}
}

// memberSight is visibleMembers for an event reader: an actor that could
// not be loaded, or whose sight cannot be read, sees no one.
func (s *Server) memberSight(ctx context.Context, actor permissions.Actor) func(domain.MemberID) bool {
	if actor.ID == "" {
		return func(domain.MemberID) bool { return false }
	}
	sees, err := s.visibleMembers(ctx, actor)
	if err != nil {
		return func(domain.MemberID) bool { return false }
	}
	return sees
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
