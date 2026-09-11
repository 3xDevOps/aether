package scheduler

import (
	"context"
	"errors"
	"fmt"
	"path"

	"github.com/3xDevOps/Aether/internal/domain"
)

// SaveTerminalImage persists image bytes in the target account's member home
// and returns the absolute path visible from that target container. An empty
// run ID targets the caller's live environment terminal, whose captured HOME
// is used; a run target must still have a live supervised container.
func (s *Scheduler) SaveTerminalImage(ctx context.Context, actor domain.MemberID, runID domain.RunID, extension string, data []byte) (string, error) {
	if s.cfg.Homes == nil {
		return "", errors.New("scheduler: member homes are not configured")
	}

	account := actor
	var home string
	var stillLive func() bool
	if runID != "" {
		run, err := s.cfg.Store.GetRun(ctx, runID)
		if err != nil {
			return "", fmt.Errorf("scheduler: get image target run: %w", err)
		}
		account = run.AccountMember()
		s.mu.Lock()
		entry := s.runs[runID]
		if entry != nil {
			home = entry.home
			containerID := entry.containerID
			stillLive = func() bool {
				s.mu.Lock()
				defer s.mu.Unlock()
				current := s.runs[runID]
				return current == entry && current.containerID == containerID && current.home == home
			}
		}
		s.mu.Unlock()
		if entry == nil {
			return "", errors.New("scheduler: image target run has no live container")
		}
		if home == "" {
			return "", errors.New("scheduler: image target run has no captured HOME")
		}
	} else {
		var running bool
		s.mu.Lock()
		entry := s.terminals[account]
		if entry != nil {
			home = entry.home
			containerID := entry.containerID
			running = true
			stillLive = func() bool {
				s.mu.Lock()
				defer s.mu.Unlock()
				current := s.terminals[account]
				return current == entry && current.containerID == containerID && current.home == home
			}
		}
		s.mu.Unlock()
		if !running {
			return "", errors.New("scheduler: image target environment terminal is not running")
		}
		if home == "" {
			return "", errors.New("scheduler: image target terminal has no captured HOME")
		}
	}

	relative, err := s.cfg.Homes.SaveImage(account, extension, data)
	if err != nil {
		return "", fmt.Errorf("scheduler: persist terminal image: %w", err)
	}
	if stillLive != nil && !stillLive() {
		return "", errors.New("scheduler: image target container changed while saving")
	}
	return path.Join(home, relative), nil
}
