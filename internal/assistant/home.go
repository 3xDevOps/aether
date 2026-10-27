package assistant

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/3xDevOps/Aether/internal/domain"
)

func writeInstructions(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("assistant: create the instructions directory: %w", err)
	}
	for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(Instructions), 0o644); err != nil {
			return fmt.Errorf("assistant: write %s: %w", name, err)
		}
	}
	return nil
}

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
