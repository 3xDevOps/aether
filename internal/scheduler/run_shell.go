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
	reservation, err := s.EnsureRunShellTabReserved(ctx, run, tab, cols, rows)
	if err != nil {
		return err
	}
	reservation.Adopt()
	return nil
}

// EnsureRunShellTabReserved admits one shell-tab attach and returns an
// ownership token. A caller must Adopt after its attach is accepted or
// Rollback when authorization/attachment is refused. Concurrent reservations
// share the same pending shell state; the shell is stopped only when every
// pending creator rolls back without an adoption.
func (s *Scheduler) EnsureRunShellTabReserved(ctx context.Context, run domain.RunID, tab string, cols, rows uint) (ptyhost.ShellTabReservation, error) {
	if !runShellTabName.MatchString(tab) {
		return nil, fmt.Errorf("%w: %q must match ^[a-z0-9-]{1,32}$", ErrInvalidRunShellTab, tab)
	}

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
					return nil, fmt.Errorf("%w: run %s has no live container", ptyhost.ErrNoSession, run)
				}
				return nil, fmt.Errorf("scheduler: find run shell %s: %w", run, err)
			}
			if stored.Status != domain.RunRunning && stored.Status != domain.RunNeedsAttention {
				return nil, fmt.Errorf("%w: run %s has no live container", ptyhost.ErrNoSession, run)
			}
			sc, sidecarErr := s.readSidecar(run)
			if sidecarErr == nil && !sc.Paused && !sc.ExitObserved && sc.ContainerID != "" {
				containerID = runtime.ID(sc.ContainerID)
				goto live
			}
		case (entry.status != domain.RunRunning && entry.status != domain.RunNeedsAttention) ||
			entry.paused || entry.containerID == "":
			s.mu.Unlock()
			return nil, fmt.Errorf("%w: run %s has no live container", ptyhost.ErrNoSession, run)
		default:
			containerID = entry.containerID
			s.mu.Unlock()
			goto live
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline.C:
			return nil, fmt.Errorf("%w: run %s has no live container", ptyhost.ErrNoSession, run)
		case <-retry.C:
		}
	}

live:
	key := ptyhost.RunShellSession(run, tab)
	active := s.cfg.PTY.ActiveSessions(string(ptyhost.RunShellSession(run, "")))
	for _, current := range active {
		if current == key {
			s.runShellReservationMu.Lock()
			state := s.runShellReservations[string(key)]
			if state != nil {
				state.pending++
			}
			s.runShellReservationMu.Unlock()
			generation := s.cfg.PTY.SessionGeneration(key)
			if generation == 0 {
				return nil, fmt.Errorf("%w: shell %s was replaced during reservation", ptyhost.ErrSessionReplaced, key)
			}
			if state != nil {
				return &shellTabReservation{s: s, run: run, key: string(key), generation: generation, state: state}, nil
			}
			return &shellTabReservation{s: s, run: run, generation: generation}, nil
		}
	}
	if len(active) >= 4 {
		return nil, ErrRunShellTabLimit
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
		return nil, fmt.Errorf("scheduler: start run shell: %w", err)
	}
	if err := s.cfg.PTY.StartSession(ctx, key, att); err != nil {
		_ = att.Close()
		return nil, fmt.Errorf("scheduler: start run shell session: %w", err)
	}
	generation := s.cfg.PTY.SessionGeneration(key)
	if generation == 0 {
		_ = s.cfg.PTY.StopSession(context.WithoutCancel(ctx), key)
		return nil, fmt.Errorf("%w: shell %s has no generation", ptyhost.ErrSessionReplaced, key)
	}
	state := &shellTabState{pending: 1}
	s.runShellReservationMu.Lock()
	s.runShellReservations[string(key)] = state
	s.runShellReservationMu.Unlock()
	return &shellTabReservation{s: s, run: run, key: string(key), generation: generation, state: state}, nil
}

type shellTabState struct {
	pending int
	adopted bool
}

type shellTabReservation struct {
	s          *Scheduler
	run        domain.RunID
	key        string
	generation uint64
	state      *shellTabState
	mu         sync.Mutex
	done       bool
}

func (r *shellTabReservation) Generation() uint64 {
	return r.generation
}

func (r *shellTabReservation) Adopt() {
	r.mu.Lock()
	if r.done {
		r.mu.Unlock()
		return
	}
	r.done = true
	r.mu.Unlock()
	if r.state == nil {
		return
	}
	lock := r.s.lockForShell(r.run)
	lock.Lock()
	defer lock.Unlock()
	r.s.runShellReservationMu.Lock()
	defer r.s.runShellReservationMu.Unlock()
	if r.s.runShellReservations[r.key] != r.state {
		return
	}
	delete(r.s.runShellReservations, r.key)
	r.state.adopted = true
}

func (r *shellTabReservation) Rollback(ctx context.Context) error {
	r.mu.Lock()
	if r.done {
		r.mu.Unlock()
		return nil
	}
	r.done = true
	r.mu.Unlock()
	if r.state == nil {
		return nil
	}
	lock := r.s.lockForShell(r.run)
	lock.Lock()
	defer lock.Unlock()
	r.s.runShellReservationMu.Lock()
	if r.s.runShellReservations[r.key] != r.state {
		r.s.runShellReservationMu.Unlock()
		return nil
	}
	if r.state.adopted {
		r.s.runShellReservationMu.Unlock()
		return nil
	}
	if r.state.pending > 0 {
		r.state.pending--
	}
	if r.state.pending > 0 {
		r.s.runShellReservationMu.Unlock()
		return nil
	}
	delete(r.s.runShellReservations, r.key)
	r.s.runShellReservationMu.Unlock()
	return r.s.cfg.PTY.StopSession(ctx, ptyhost.SessionKey(r.key))
}

func (s *Scheduler) lockForShell(run domain.RunID) *sync.Mutex {
	s.mu.Lock()
	lock := s.runShellLocks[run]
	if lock == nil {
		lock = &sync.Mutex{}
		s.runShellLocks[run] = lock
	}
	s.mu.Unlock()
	return lock
}

// StopRunShellTab removes one persistent shell tab. It is reserved for
// lifecycle cleanup; attach admission uses an ownership token instead.
func (s *Scheduler) StopRunShellTab(ctx context.Context, run domain.RunID, tab string) error {
	if !runShellTabName.MatchString(tab) {
		return fmt.Errorf("%w: %q must match ^[a-z0-9-]{1,32}$", ErrInvalidRunShellTab, tab)
	}
	return s.cfg.PTY.StopSession(ctx, ptyhost.RunShellSession(run, tab))
}
