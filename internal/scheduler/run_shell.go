package scheduler

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sync"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/ptyhost"
)

var (
	ErrInvalidRunShellTab = errors.New("scheduler: invalid run shell tab")
	ErrRunShellTabLimit   = errors.New("scheduler: shell limit reached")
)

// maxRunShells bounds the processes and PTY state one starter holds in a run.
// The agent and the run's people are counted apart, so neither can use up
// the other's.
const maxRunShells = 4

var runShellTabName = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)

// EnsureRunShellTab adopts a dashboard shell. An ended or recovered execution
// is never restarted by attaching; replacement requires an explicit start.
func (s *Scheduler) EnsureRunShellTab(ctx context.Context, run domain.RunID, tab string, cols, rows uint) error {
	reservation, err := s.EnsureRunShellTabReserved(ctx, run, tab, cols, rows)
	if err != nil {
		return err
	}
	reservation.Adopt()
	return nil
}

// EnsureRunShellTabReserved shares the development-terminal registry, limit,
// process identity, and PTY key. Only an unadopted creation can be rolled back.
func (s *Scheduler) EnsureRunShellTabReserved(ctx context.Context, run domain.RunID, tab string, cols, rows uint) (ptyhost.ShellTabReservation, error) {
	if !runShellTabName.MatchString(tab) {
		return nil, fmt.Errorf("%w: %q must match ^[a-z0-9-]{1,32}$", ErrInvalidRunShellTab, tab)
	}
	lock := s.lockForShell(run)
	lock.Lock()
	defer lock.Unlock()
	live, err := s.ResolveLiveRun(ctx, run, false)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ptyhost.ErrNoSession, err)
	}
	set, err := s.loadRunTerminalsLocked(ctx, run)
	if err != nil {
		return nil, err
	}
	if prepareErr := s.prepareRunTerminalsLocked(set, live.ContainerID); prepareErr != nil {
		return nil, prepareErr
	}
	key := ptyhost.RunShellSession(run, tab)
	terminal := set.Terminals[tab]
	if terminal == nil {
		terminal, err = s.startRunTerminalLocked(ctx, run, set, live, protocol.DevTerminalStartParams{Name: tab, Cols: cols, Rows: rows}, false)
		if err != nil {
			return nil, err
		}
		s.runShellReservationMu.Lock()
		if s.runShellReservations == nil {
			s.runShellReservations = make(map[string]*shellTabState)
		}
		s.runShellReservations[string(key)] = &shellTabState{}
		s.runShellReservationMu.Unlock()
	}
	if terminal.Generation == 0 || s.cfg.PTY.SessionGeneration(key) != terminal.Generation {
		return nil, fmt.Errorf("%w: terminal has no reattachable PTY; use explicit stop/start", ptyhost.ErrSessionReplaced)
	}
	if err := s.refreshRunTerminalLocked(ctx, run, set, terminal); err != nil {
		return nil, err
	}
	if terminal.State.Exited || terminal.Unavailable != "" {
		return nil, fmt.Errorf("%w: terminal ended or unavailable; use explicit start to replace a proved exited process", ptyhost.ErrSessionEnded)
	}
	s.runShellReservationMu.Lock()
	state := s.runShellReservations[string(key)]
	if state != nil {
		state.pending++
	}
	s.runShellReservationMu.Unlock()
	return &shellTabReservation{s: s, run: run, key: string(key), generation: terminal.Generation, state: state}, nil
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

func (r *shellTabReservation) Generation() uint64 { return r.generation }

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
	if r.s.runShellReservations[r.key] != r.state || r.state.adopted {
		r.s.runShellReservationMu.Unlock()
		return nil
	}
	r.state.pending--
	if r.state.pending > 0 {
		r.s.runShellReservationMu.Unlock()
		return nil
	}
	delete(r.s.runShellReservations, r.key)
	r.s.runShellReservationMu.Unlock()
	set, err := r.s.loadRunTerminalsLocked(ctx, r.run)
	if err != nil {
		return err
	}
	for id, terminal := range set.Terminals {
		if string(ptyhost.RunShellSession(r.run, id)) != r.key || terminal.Generation != r.generation {
			continue
		}
		if err := r.s.stopRunTerminalLocked(ctx, r.run, set, terminal, r.s.cfg.StopGrace); err != nil {
			return err
		}
		_ = r.s.cfg.PTY.StopSession(ctx, ptyhost.SessionKey(r.key))
		delete(set.Terminals, id)
		return r.s.persistRunTerminalsLocked(r.run, set)
	}
	return nil
}

func (s *Scheduler) lockForShell(run domain.RunID) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.runShellLocks == nil {
		s.runShellLocks = make(map[domain.RunID]*sync.Mutex)
	}
	lock := s.runShellLocks[run]
	if lock == nil {
		lock = &sync.Mutex{}
		s.runShellLocks[run] = lock
	}
	return lock
}

// StopRunShellTab is lifecycle-only. Detaching a viewer must not call it.
func (s *Scheduler) StopRunShellTab(ctx context.Context, run domain.RunID, tab string) error {
	if !runShellTabName.MatchString(tab) {
		return fmt.Errorf("%w: %q", ErrInvalidRunShellTab, tab)
	}
	lock := s.lockForShell(run)
	lock.Lock()
	defer lock.Unlock()
	set, err := s.loadRunTerminalsLocked(ctx, run)
	if err != nil {
		return err
	}
	terminal := set.Terminals[tab]
	if terminal == nil {
		return ptyhost.ErrNoSession
	}
	return s.stopRunTerminalLocked(ctx, run, set, terminal, s.cfg.StopGrace)
}
