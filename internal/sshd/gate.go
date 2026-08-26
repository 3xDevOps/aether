package sshd

import (
	"context"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/ptyhost"
	"github.com/3xDevOps/Aether/internal/store"
)

// NewWriteGate returns the PTY host's write-mode attach gate: writing to a
// run's terminal is the Steer capability, checked against the same policy
// as the control-channel steering methods. Read-only attaches never reach
// the gate (ptyhost only consults it for write mode), so viewers keep
// read access. Wired into ptyhost.Config.Gate by the server assembly.
func NewWriteGate(st store.Store) ptyhost.WriteGate {
	return func(ctx context.Context, member domain.MemberID, run domain.RunID) error {
		actor, err := resolveActor(ctx, st, member)
		if err != nil {
			return err
		}
		target, err := resolveRunTarget(ctx, st, run)
		if err != nil {
			return err
		}
		return permissions.Check(permissions.Steer, actor, target)
	}
}

// checkPush authorizes git receive-pack: pushing writes to the workspace
// repository, so it needs Push. The role is read at exec time, so a
// demoted member cannot push on an already-open connection.
func (s *Server) checkPush(ctx context.Context, member domain.MemberID) error {
	actor, err := resolveActor(ctx, s.cfg.Store, member)
	if err != nil {
		return err
	}
	return permissions.Check(permissions.Push, actor, permissions.Target{})
}

// resolveActor loads the member's current role for a permission check.
func resolveActor(ctx context.Context, st store.Store, id domain.MemberID) (permissions.Actor, error) {
	m, err := st.GetMember(ctx, id)
	if err != nil {
		return permissions.Actor{}, err
	}
	return permissions.Actor{ID: m.ID, Role: m.Role}, nil
}

// resolveRunTarget loads the run's ownership and protection plus its
// workspace's steer-others policy for a permission check.
func resolveRunTarget(ctx context.Context, st store.Store, id domain.RunID) (permissions.Target, error) {
	run, err := st.GetRun(ctx, id)
	if err != nil {
		return permissions.Target{}, err
	}
	ws, err := st.GetWorkspace(ctx, run.WorkspaceID)
	if err != nil {
		return permissions.Target{}, err
	}
	return permissions.Target{
		Owner:       run.MemberID,
		Protected:   run.Protected,
		SteerOthers: ws.SteerOthers,
	}, nil
}
