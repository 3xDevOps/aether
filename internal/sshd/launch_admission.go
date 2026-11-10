package sshd

import (
	"context"
	"fmt"
	"sync"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/fallback"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/store"
)

// LaunchAdmission is the fresh identity and account decision used by both
// human run.launch and child run dispatch. Account sharing is deliberately
// resolved at admission time; a run owner never becomes an account owner.
type LaunchAdmission struct {
	Account *domain.Member
}

// AuthorizeLaunch resolves and checks every mutable fact required before a
// run in workspaces, its primary and extra workspaces, can be admitted.
// Callers that launch a run must hold the shared authorization mutex across
// this check and the scheduler's durable launch boundary.
func AuthorizeLaunch(ctx context.Context, st store.Store, actorID domain.MemberID, requestedAccount string, workspaces []domain.WorkspaceID) (LaunchAdmission, error) {
	if st == nil {
		return LaunchAdmission{}, fmt.Errorf("sshd: launch admission requires a store")
	}
	actor, err := st.GetMember(ctx, actorID)
	if err != nil {
		return LaunchAdmission{}, err
	}
	if actor.Pending {
		return LaunchAdmission{}, fmt.Errorf("%w: membership pending admin approval", permissions.ErrDenied)
	}
	if err = permissions.Check(permissions.Launch, permissions.ActorOf(actor), permissions.Target{}); err != nil {
		return LaunchAdmission{}, err
	}
	if err = checkLaunchIn(permissions.ActorOf(actor), workspaces); err != nil {
		return LaunchAdmission{}, err
	}
	accountID, err := fallback.ResolveLaunchAccountIn(ctx, st, actor.ID, requestedAccount, workspaces)
	if err != nil {
		return LaunchAdmission{}, err
	}
	account, err := st.GetMember(ctx, accountID)
	if err != nil {
		return LaunchAdmission{}, err
	}
	if account.Pending {
		return LaunchAdmission{}, fmt.Errorf("%w: account owner is pending admin approval", permissions.ErrDenied)
	}
	return LaunchAdmission{Account: account}, nil
}

// checkLaunchIn is Launch in every one of workspaces: a run reads and
// writes the repository of each.
func checkLaunchIn(actor permissions.Actor, workspaces []domain.WorkspaceID) error {
	for _, ws := range workspaces {
		if err := permissions.Check(permissions.Launch, actor, permissions.Target{Workspace: ws}); err != nil {
			return err
		}
	}
	return nil
}

// runWorkspaces is the run's primary workspace followed by its extra ones.
func runWorkspaces(run *domain.Run) []domain.WorkspaceID {
	out := []domain.WorkspaceID{run.WorkspaceID}
	for _, repo := range run.Extras {
		out = append(out, repo.WorkspaceID)
	}
	return out
}

// lockAuthorization runs the admission callback under the process-wide
// credential authorization mutex. The pointer is allocated by sshd.New and
// shared with the child run service.
func (s *Server) lockAuthorization(fn func() error) error {
	if fn == nil {
		return fmt.Errorf("sshd: nil launch admission callback")
	}
	if s.authorizationMu == nil {
		s.authorizationMu = &sync.Mutex{}
	}
	s.authorizationMu.Lock()
	defer s.authorizationMu.Unlock()
	return fn()
}
