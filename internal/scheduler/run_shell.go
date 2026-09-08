package scheduler

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sync"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/ptyhost"
	"github.com/3xDevOps/Aether/internal/runtime"
	"github.com/3xDevOps/Aether/internal/store"
)

var (
	// ErrInvalidRunShellTab identifies a tab name that cannot be used in the
	// run-shell session key or shell client URL.
	ErrInvalidRunShellTab = errors.New("scheduler: invalid run shell tab")
	// ErrRunShellTabLimit identifies the per-run shell tab resource limit.
	ErrRunShellTabLimit = errors.New("scheduler: at most 4 shell tabs per run")
)

var runShellTabName = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)

const runShellRecoveryWait = 5 * time.Second

// EnsureRunShellTab starts an interactive shell process in a running or
// stalled-but-live run container unless that tab already has a live PTY
// session.
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
	s.mu.Unlock()
	lock.Lock()
	defer lock.Unlock()

	var containerID runtime.ID
	deadline := time.NewTimer(runShellRecoveryWait)
	defer deadline.Stop()
	retry := time.NewTicker(50 * time.Millisecond)
	defer retry.Stop()
	for {
		s.mu.Lock()
		entry := s.runs[run]
		switch {
		case entry == nil:
			s.mu.Unlock()
			stored, err := s.cfg.Store.GetRun(ctx, run)
			if err != nil {
				if errors.Is(err, store.ErrNotFound) {
					return fmt.Errorf("%w: run %s has no live container", ptyhost.ErrNoSession, run)
				}
				return fmt.Errorf("scheduler: find run shell %s: %w", run, err)
			}
			if stored.Status != domain.RunRunning && stored.Status != domain.RunNeedsAttention {
				return fmt.Errorf("%w: run %s has no live container", ptyhost.ErrNoSession, run)
			}
			sc, sidecarErr := s.readSidecar(run)
			if sidecarErr == nil && !sc.Paused && !sc.ExitObserved && sc.ContainerID != "" {
				containerID = runtime.ID(sc.ContainerID)
				goto live
			}
		case (entry.status != domain.RunRunning && entry.status != domain.RunNeedsAttention) ||
			entry.paused || entry.containerID == "":
			s.mu.Unlock()
			return fmt.Errorf("%w: run %s has no live container", ptyhost.ErrNoSession, run)
		default:
			containerID = entry.containerID
			s.mu.Unlock()
			goto live
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("%w: run %s has no live container", ptyhost.ErrNoSession, run)
		case <-retry.C:
		}
	}

live:

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
