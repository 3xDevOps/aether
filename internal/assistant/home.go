package assistant

import (
	"context"
	"fmt"

	"github.com/3xDevOps/Aether/internal/domain"
)

// DiscardAgentState stops the member's session and deletes everything the
// agent kept in the assistant home, as a new conversation does. The item log
// and the stored session are the caller's.
func (s *Service) DiscardAgentState(ctx context.Context, id domain.MemberID) error {
	m := s.member(id)
	m.op.Lock()
	defer m.op.Unlock()
	if err := s.stop(ctx, m); err != nil {
		return err
	}
	if err := s.cfg.Host.DiscardAssistantState(ctx, id); err != nil {
		return fmt.Errorf("assistant: %w", err)
	}
	return nil
}

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
