package assistant

import (
	"context"

	"github.com/3xDevOps/Aether/internal/domain"
)

// SignIn stops the member's session and runs start, which begins a sign-in
// for their assistant, under the member's lock: the sign-in replaces the
// assistant container, so it must not race a start or the member's removal.
func (s *Service) SignIn(ctx context.Context, id domain.MemberID, start func(context.Context) error) error {
	m := s.member(id)
	m.op.Lock()
	defer m.op.Unlock()
	if m.removing {
		return ErrRemoving
	}
	if err := s.stop(ctx, m); err != nil {
		return err
	}
	// Removing the member waits on m.op, so a stalled install must not hold
	// it for longer than a start may take.
	ctx, cancel := context.WithTimeout(ctx, startTimeout)
	defer cancel()
	return start(ctx)
}
