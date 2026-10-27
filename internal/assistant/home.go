package assistant

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/scheduler"
)

// keptState is what discarding the agent's state leaves in the assistant
// home: the installed agent and adapter. The next container's plan writes
// the onboarding state and login mountpoints again.
var keptState = map[string]bool{".local": true}

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
// agent kept in the assistant home: sessions, transcripts, history, todos,
// file history, shell snapshots and logs. The conversation's item log and
// stored session are left to the caller.
func (s *Service) DiscardAgentState(ctx context.Context, id domain.MemberID) error {
	m := s.member(id)
	m.op.Lock()
	defer m.op.Unlock()
	if err := s.stop(ctx, m); err != nil {
		return err
	}
	return s.discardAgentState(ctx, id)
}

// discardAgentState destroys the assistant container first: deleting a login
// mountpoint under a running container would unmount the login there.
func (s *Service) discardAgentState(ctx context.Context, id domain.MemberID) error {
	if err := s.cfg.Host.RemoveAssistantContainer(ctx, id); err != nil {
		return fmt.Errorf("assistant: %w", err)
	}
	if err := wipeHome(scheduler.AssistantHomeDir(s.cfg.Dir, id)); err != nil {
		return fmt.Errorf("assistant: discard the agent's state: %w", err)
	}
	return nil
}

func wipeHome(home string) error {
	entries, err := os.ReadDir(home)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		if keptState[e.Name()] {
			continue
		}
		if err := os.RemoveAll(filepath.Join(home, e.Name())); err != nil {
			return err
		}
	}
	return nil
}
