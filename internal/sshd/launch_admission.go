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
	Actor   *domain.Member
	Account *domain.Member
}

// AuthorizeLaunch resolves and checks every mutable fact required before a
// run can be admitted. Callers that launch a run must hold the shared
// authorization mutex across this check and the scheduler's durable launch
// boundary.
func AuthorizeLaunch(ctx context.Context, st store.Store, actorID domain.MemberID, requestedAccount string) (LaunchAdmission, error) {
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
	if err = permissions.Check(permissions.Launch, permissions.Actor{ID: actor.ID, Role: actor.Role}, permissions.Target{}); err != nil {
		return LaunchAdmission{}, err
	}
	accountID, err := fallback.ResolveLaunchAccount(ctx, st, actor.ID, requestedAccount)
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
	return LaunchAdmission{Actor: actor, Account: account}, nil
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
