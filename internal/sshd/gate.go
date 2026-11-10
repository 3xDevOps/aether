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
	return func(ctx context.Context, member domain.MemberID, key ptyhost.SessionKey) error {
		run, ok := key.Run()
		if !ok {
			return nil
		}
		return checkSteer(ctx, st, member, run)
	}
}

// checkSteer is the Steer check against the member's current role and the
// run's current owner, protection, and workspace policy. The write gate,
// the sync bridge's re-validation, and the live attach re-check all call
// it, so the three revoke on exactly the same conditions.
func checkSteer(ctx context.Context, st store.Store, member domain.MemberID, run domain.RunID) error {
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

func checkRunView(ctx context.Context, st store.Store, member domain.MemberID, run domain.RunID) error {
	r, err := st.GetRun(ctx, run)
	if err != nil {
		return err
	}
	return checkIn(ctx, st, member, permissions.View, r.WorkspaceID)
}

// checkPush authorizes git receive-pack in workspace ws: pushing writes to
// the workspace repository, so it needs Push there. Access is read at exec
// time, so a demoted or revoked member cannot push on an already-open
// connection.
func (s *Server) checkPush(ctx context.Context, member domain.MemberID, ws domain.WorkspaceID) error {
	return checkIn(ctx, s.cfg.Store, member, permissions.Push, ws)
}

func resolveActor(ctx context.Context, st store.Store, id domain.MemberID) (permissions.Actor, error) {
	m, err := st.GetMember(ctx, id)
	if err != nil {
		return permissions.Actor{}, err
	}
	return permissions.ActorOf(m), nil
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
		Workspace:   run.WorkspaceID,
		Owner:       run.MemberID,
		Protected:   run.Protected,
		SteerOthers: ws.SteerOthers,
	}, nil
}

// checkIn is Check of cap in workspace ws for member. A workspace member
// cannot reach is permissions.ErrNoAccess, which rpcError reports exactly
// as a workspace that does not exist.
func checkIn(ctx context.Context, st store.Store, member domain.MemberID, cap permissions.Capability, ws domain.WorkspaceID) error {
	actor, err := resolveActor(ctx, st, member)
	if err != nil {
		return err
	}
	return permissions.Check(cap, actor, permissions.Target{Workspace: ws})
}
