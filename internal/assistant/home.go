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
