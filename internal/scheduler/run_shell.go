package scheduler

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sync"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/ptyhost"
	"github.com/3xDevOps/Aether/internal/runtime"
)

var (
	// ErrInvalidRunShellTab identifies a tab name that cannot be used in the
	// run-shell session key or shell client URL.
	ErrInvalidRunShellTab = errors.New("scheduler: invalid run shell tab")
	// ErrRunShellTabLimit identifies the per-run shell tab resource limit.
	ErrRunShellTabLimit = errors.New("scheduler: at most 4 shell tabs per run")
)

var runShellTabName = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)

// EnsureRunShellTab starts an interactive shell process in a running run
// container unless that tab already has a live PTY session.
func (s *Scheduler) EnsureRunShellTab(ctx context.Context, run domain.RunID, tab string, cols, rows uint) error {
	if !runShellTabName.MatchString(tab) {
		return fmt.Errorf("%w: %q must match ^[a-z0-9-]{1,32}$", ErrInvalidRunShellTab, tab)
	}

	// A per-run lock serializes tab creation so the cap cannot be raced
	// past, without one hung exec blocking every other run's shells.
	s.mu.Lock()
	lock := s.runShellLocks[run]
	if lock == nil {
		lock = &sync.Mutex{}
		s.runShellLocks[run] = lock
	}
	entry := s.runs[run]
	if entry == nil || entry.status != domain.RunRunning || entry.paused || entry.containerID == "" {
		s.mu.Unlock()
		return fmt.Errorf("%w: run %s has no live container", ptyhost.ErrNoSession, run)
	}
	containerID := entry.containerID
	s.mu.Unlock()
	lock.Lock()
	defer lock.Unlock()

	key := ptyhost.RunShellSession(run, tab)
	active := s.cfg.PTY.ActiveSessions(string(ptyhost.RunShellSession(run, "")))
	for _, current := range active {
		if current == key {
			return nil
		}
	}
	if len(active) >= 4 {
		return ErrRunShellTabLimit
	}

	bash := []string{"/bin/bash", "-l"}
	att, err := s.cfg.Runtime.ExecTTY(ctx, containerID, bash, s.cfg.WorktreeMount, cols, rows)
	if err != nil {
		var exitErr *runtime.ExecExitError
		if errors.As(err, &exitErr) && (exitErr.Code == 126 || exitErr.Code == 127) {
			att, err = s.cfg.Runtime.ExecTTY(ctx, containerID, []string{"/bin/sh", "-l"}, s.cfg.WorktreeMount, cols, rows)
		}
	}
	if err != nil {
		return fmt.Errorf("scheduler: start run shell: %w", err)
	}
	if err := s.cfg.PTY.StartSession(ctx, key, att); err != nil {
		_ = att.Close()
		return fmt.Errorf("scheduler: start run shell session: %w", err)
	}
	return nil
}
