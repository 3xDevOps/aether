package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

// ErrTerminalNotRunning reports that an environment save needs an open terminal.
var ErrTerminalNotRunning = errors.New("terminal: environment terminal is not running; open it first")

// SaveEnvironment snapshots the running environment terminal and records its image for the member.
func (s *Scheduler) SaveEnvironment(ctx context.Context, member domain.MemberID) (string, error) {
	lock := s.terminalLock(member)
	lock.Lock()
	defer lock.Unlock()

	sup := s.lookupTerminal(member)
	if sup == nil {
		return "", ErrTerminalNotRunning
	}
	m, err := s.cfg.Store.GetMember(ctx, member)
	if err != nil {
		return "", fmt.Errorf("scheduler: get member to save environment: %w", err)
	}
	tag := fmt.Sprintf("aether/member-%s:%d", member, time.Now().Unix())
	if err := s.cfg.Runtime.Commit(ctx, sup.containerID, tag); err != nil {
		return "", fmt.Errorf("scheduler: save environment: %w", err)
	}
	previous := m.Image
	if err := s.cfg.Store.UpdateMemberImage(ctx, member, tag); err != nil {
		return "", fmt.Errorf("scheduler: save environment: persist image: %w", err)
	}
	if previous != "" && previous != tag {
		if err := s.cfg.Runtime.RemoveImage(ctx, previous); err != nil {
			slog.Warn("scheduler: remove previous member environment image", "member", member, "image", previous, "error", err)
		}
	}
	return tag, nil
}

// ResetEnvironment stops the environment terminal and returns the member to the standard image.
func (s *Scheduler) ResetEnvironment(ctx context.Context, member domain.MemberID) error {
	lock := s.terminalLock(member)
	lock.Lock()
	defer lock.Unlock()

	m, err := s.cfg.Store.GetMember(ctx, member)
	if err != nil {
		return fmt.Errorf("scheduler: get member to reset environment: %w", err)
	}
	if err := s.stopTerminalLocked(ctx, member); err != nil {
		return fmt.Errorf("scheduler: reset environment: %w", err)
	}
	previous := m.Image
	if err := s.cfg.Store.UpdateMemberImage(ctx, member, ""); err != nil {
		return fmt.Errorf("scheduler: reset environment: clear image: %w", err)
	}
	if previous != "" {
		if err := s.cfg.Runtime.RemoveImage(ctx, previous); err != nil {
			slog.Warn("scheduler: remove member environment image", "member", member, "image", previous, "error", err)
		}
	}
	return nil
}
