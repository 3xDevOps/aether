package control

import (
	"errors"
	"log/slog"
	"slices"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

type PrincipalKind string

const (
	PrincipalMember   PrincipalKind = "member"
	PrincipalRunAgent PrincipalKind = "run_agent"
	SurfaceTerminal   SurfaceKind   = "terminal"
	SurfaceBrowser    SurfaceKind   = "browser"
	MaxSurfaceIDBytes               = 256
)

var ErrAgentTakeover = errors.New("control: agents cannot force surface takeover")

// Principal is derived from authentication, never from request JSON. A run
// agent has a RunID and no MemberID; a human has a MemberID and no RunID.
type Principal struct {
	Kind     PrincipalKind
	MemberID domain.MemberID
	RunID    domain.RunID
}

type SurfaceKind string

// Surface identifies a live resource incarnation, not an output or screen
// revision. The primary harness is deliberately not a development surface.
type Surface struct {
	Kind        SurfaceKind
	ID          string
	Incarnation string
}

type SurfaceSnapshot struct {
	RunID      domain.RunID
	Surface    Surface
	Principal  Principal
	SessionID  string
	Generation uint64
	Connected  bool
	AcquiredAt time.Time
	ExpiresAt  time.Time
}

type surfaceKey struct {
	run     domain.RunID
	surface Surface
}

type surfaceState struct {
	runState
	principal     Principal
	cleanup       func() error
	cleanupNeeded bool
	cleanupErr    error
}

func (s *Service) validateSurface(run string, surface Surface) error {
	if s.validateRun(run) != nil || (surface.Kind != SurfaceTerminal && surface.Kind != SurfaceBrowser) ||
		len(surface.ID) == 0 || len(surface.ID) > MaxSurfaceIDBytes ||
		len(surface.Incarnation) == 0 || len(surface.Incarnation) > MaxSurfaceIDBytes {
		return ErrInvalid
	}
	return nil
}

func (s *Service) validatePrincipal(run string, p Principal) error {
	switch p.Kind {
	case PrincipalMember:
		if p.MemberID != "" && p.RunID == "" {
			return nil
		}
	case PrincipalRunAgent:
		if p.MemberID == "" && p.RunID == domain.RunID(run) && p.RunID != "" {
			return nil
		}
	}
	return ErrInvalid
}

// surfaceStateFor must be called with the run's surfaceGate held. Table
// entries retain their generation high-water marks after release and expiry.
func (s *Service) surfaceStateFor(run string, surface Surface, create bool) *surfaceState {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := surfaceKey{domain.RunID(run), surface}
	state := s.surfaces[key]
	if state == nil && create {
		state = &surfaceState{}
		s.surfaces[key] = state
	}
	return state
}

// BindSurfaceCleanup installs the resource's bounded input cleanup. It runs
// under the same surface gate as physical writes, never under the table mutex.
// It must not call Service or acquire an upstream resource/mutation lock.
// First binding treats retained physical state as unknown until an authorized
// acquire cleans it; rebinding does not touch the current writer's input.
func (s *Service) BindSurfaceCleanup(run string, surface Surface, cleanup func() error) error {
	if s.validateSurface(run, surface) != nil || cleanup == nil {
		return ErrInvalid
	}
	runState := s.stateFor(domain.RunID(run), true)
	runState.surfaceGate.RLock()
	defer runState.surfaceGate.RUnlock()
	state := s.surfaceStateFor(run, surface, true)
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.cleanup == nil {
		state.cleanup = cleanup
		state.cleanupNeeded = true
	}
	return nil
}

func (s *Service) cleanSurfaceLocked(state *surfaceState) error {
	if state.cleanup != nil && state.cleanupNeeded {
		state.cleanupErr = state.cleanup()
		if state.cleanupErr != nil {
			slog.Warn("control: surface input cleanup failed; replacement admission remains fenced", "error", state.cleanupErr)
		}
		if state.cleanupErr == nil {
			state.cleanupNeeded = false
		}
	}
	return state.cleanupErr
}

func (s *Service) expireSurfaceLocked(run string, surface Surface, state *surfaceState, now time.Time) {
	if state.current != nil && !state.current.connected && !state.current.expiresAt.After(now) {
		state.current = nil
		_ = s.cleanSurfaceLocked(state)
		_ = s.recordSurfaceStateLocked(run, surface, state)
	}
}

func surfaceSnapshot(run string, surface Surface, state *surfaceState) SurfaceSnapshot {
	c := state.current
	return SurfaceSnapshot{RunID: domain.RunID(run), Surface: surface, Principal: state.principal,
		SessionID: c.sessionID, Generation: c.generation, Connected: c.connected,
		AcquiredAt: c.acquiredAt, ExpiresAt: c.expiresAt}
}

// AcquireSurface installs one writer. Authorization is evaluated under the
// admission lock before reconnect or installation. Connected transports never
// share a generation. Only a human may explicitly displace another session.
// Callbacks must be bounded and must not call back into Service. Authorization
// remains the caller's responsibility; this API never changes a primary
// lease.
func (s *Service) AcquireSurface(run string, surface Surface, principal Principal, session string, force bool, expectedGeneration uint64, authorize func() error) (SurfaceSnapshot, *SurfaceSnapshot, error) {
	if err := s.validateSurface(run, surface); err != nil {
		return SurfaceSnapshot{}, nil, err
	}
	if err := s.validatePrincipal(run, principal); err != nil {
		return SurfaceSnapshot{}, nil, err
	}
	if err := s.validateSession(session); err != nil {
		return SurfaceSnapshot{}, nil, err
	}
	if force && principal.Kind == PrincipalRunAgent {
		return SurfaceSnapshot{}, nil, ErrAgentTakeover
	}
	if authorize == nil {
		return SurfaceSnapshot{}, nil, ErrInvalid
	}
	runState := s.stateFor(domain.RunID(run), true)
	runState.surfaceGate.RLock()
	defer runState.surfaceGate.RUnlock()
	state := s.surfaceStateFor(run, surface, true)
	state.mu.Lock()
	defer state.mu.Unlock()
	now := s.now()
	s.expireSurfaceLocked(run, surface, state, now)
	if expectedGeneration != 0 && (state.current == nil || state.generation != expectedGeneration) {
		return SurfaceSnapshot{}, nil, ErrStale
	}
	var displaced *SurfaceSnapshot
	if c := state.current; c != nil {
		if state.principal == principal && c.sessionID == session && !c.connected {
			if err := authorize(); err != nil {
				return SurfaceSnapshot{}, nil, err
			}
			if state.generation == ^uint64(0) {
				return SurfaceSnapshot{}, nil, ErrGenerationExhausted
			}
			if err := s.cleanSurfaceLocked(state); err != nil {
				state.current = nil
				return SurfaceSnapshot{}, nil, errors.Join(err, s.recordSurfaceStateLocked(run, surface, state))
			}
			if err := s.recordSurfaceLocked(run, surface, state.generation+1, principal, c); err != nil {
				return SurfaceSnapshot{}, nil, err
			}
			generation, err := s.nextGenerationLocked(&state.runState)
			if err != nil {
				return SurfaceSnapshot{}, nil, err
			}
			c.generation = generation
			c.connected = true
			c.restored = false
			c.expiresAt = time.Time{}
			return surfaceSnapshot(run, surface, state), nil, nil
		}
		if !force {
			return SurfaceSnapshot{}, nil, ErrOccupied
		}
		copy := surfaceSnapshot(run, surface, state)
		displaced = &copy
	}
	if err := authorize(); err != nil {
		return SurfaceSnapshot{}, nil, err
	}
	if state.generation == ^uint64(0) {
		return SurfaceSnapshot{}, nil, ErrGenerationExhausted
	}
	if err := s.cleanSurfaceLocked(state); err != nil {
		// Cleanup may have partially reached the resource. Neither the old
		// writer nor a replacement can write until cleanup is confirmed.
		state.current = nil
		return SurfaceSnapshot{}, nil, errors.Join(err, s.recordSurfaceStateLocked(run, surface, state))
	}
	next := &lease{sessionID: session, generation: state.generation + 1, connected: true, acquiredAt: now}
	if err := s.recordSurfaceLocked(run, surface, next.generation, principal, next); err != nil {
		return SurfaceSnapshot{}, nil, err
	}
	if _, err := s.nextGenerationLocked(&state.runState); err != nil {
		return SurfaceSnapshot{}, nil, err
	}
	state.principal = principal
	state.current = next
	return surfaceSnapshot(run, surface, state), displaced, nil
}

// withSurface holds only this surface's admission lock (plus the shared run
// revocation gate), so unrelated surfaces may admit writes concurrently.
func (s *Service) withSurface(run string, surface Surface, fn func(*surfaceState) error) error {
	if err := s.validateSurface(run, surface); err != nil {
		return err
	}
	runState := s.stateFor(domain.RunID(run), false)
	if runState == nil {
		return ErrStale
	}
	runState.surfaceGate.RLock()
	defer runState.surfaceGate.RUnlock()
	state := s.surfaceStateFor(run, surface, false)
	if state == nil {
		return ErrStale
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	s.expireSurfaceLocked(run, surface, state, s.now())
	return fn(state)
}

func (s *Service) matchSurface(state *surfaceState, principal Principal, session string, generation uint64, connected bool) bool {
	c := state.current
	return c != nil && state.principal == principal && c.sessionID == session && c.generation == generation && (!connected || c.connected)
}

// AdmitSurface checks the lease and accepts a mutation at the same boundary as
// takeover/revocation. fn must recheck current authorization before the effect.
func (s *Service) AdmitSurface(run string, surface Surface, principal Principal, session string, generation uint64, fn func() error) error {
	if s.validatePrincipal(run, principal) != nil || fn == nil {
		return ErrInvalid
	}
	if err := s.validateSession(session); err != nil {
		return err
	}
	return s.withSurface(run, surface, func(state *surfaceState) error {
		c := state.current
		readmit := c != nil && c.restored && s.matchSurface(state, principal, session, generation, false)
		if !readmit && !s.matchSurface(state, principal, session, generation, true) {
			return ErrStale
		}
		state.cleanupNeeded = state.cleanup != nil
		err := fn()
		if err == nil && readmit {
			c.connected, c.restored, c.expiresAt = true, false, time.Time{}
		}
		return err
	})
}

// ReleaseSurface releases only this incarnation's matching lease. admit may
// revalidate current authority; it cannot clear a durable run hold.
func (s *Service) ReleaseSurface(run string, surface Surface, principal Principal, session string, generation uint64, admit func() error) error {
	if s.validatePrincipal(run, principal) != nil || admit == nil {
		return ErrInvalid
	}
	if err := s.validateSession(session); err != nil {
		return err
	}
	return s.withSurface(run, surface, func(state *surfaceState) error {
		if !s.matchSurface(state, principal, session, generation, false) {
			return ErrStale
		}
		if err := admit(); err != nil {
			return err
		}
		_, err := s.fenceSurfaceLocked(run, surface, state)
		return errors.Join(state.cleanupErr, err)
	})
}

func (s *Service) DisconnectSurface(run string, surface Surface, principal Principal, session string, generation uint64) {
	if s.validatePrincipal(run, principal) != nil || s.validateSession(session) != nil {
		return
	}
	_ = s.withSurface(run, surface, func(state *surfaceState) error {
		if s.matchSurface(state, principal, session, generation, true) {
			state.current.connected = false
			state.current.expiresAt = s.now().Add(s.reconnectWindow)
			_ = s.cleanSurfaceLocked(state)
		}
		return nil
	})
}

func (s *Service) SurfaceStatus(run string, surface Surface) (SurfaceSnapshot, bool) {
	var result SurfaceSnapshot
	present := false
	_ = s.withSurface(run, surface, func(state *surfaceState) error {
		if state.current != nil {
			result, present = surfaceSnapshot(run, surface, state), true
		}
		return nil
	})
	return result, present
}

func (s *Service) fenceSurfaceLocked(run string, surface Surface, state *surfaceState) (*SurfaceSnapshot, error) {
	var displaced *SurfaceSnapshot
	if state.current != nil {
		copy := surfaceSnapshot(run, surface, state)
		displaced = &copy
		state.current = nil
	}
	_, _ = s.nextGenerationLocked(&state.runState)
	_ = s.cleanSurfaceLocked(state)
	return displaced, s.recordSurfaceStateLocked(run, surface, state)
}

// RevokeSurface commits an authority change and fences only this surface. The
// callback is run even for an unoccupied surface, but never without its gate.
func (s *Service) RevokeSurface(run string, surface Surface, revoke func() error) (*SurfaceSnapshot, error) {
	if err := s.validateSurface(run, surface); err != nil {
		return nil, err
	}
	if revoke == nil {
		return nil, ErrInvalid
	}
	runState := s.stateFor(domain.RunID(run), true)
	runState.surfaceGate.RLock()
	defer runState.surfaceGate.RUnlock()
	state := s.surfaceStateFor(run, surface, true)
	state.mu.Lock()
	defer state.mu.Unlock()
	s.expireSurfaceLocked(run, surface, state, s.now())
	if err := revoke(); err != nil {
		return nil, err
	}
	displaced, err := s.fenceSurfaceLocked(run, surface, state)
	return displaced, errors.Join(state.cleanupErr, err)
}

// fenceSurfacesLocked requires the run's exclusive surfaceGate. Called by the
// existing run revocation APIs so protection and permission changes cannot
// leave a development writer admitted after the run-wide boundary.
func (s *Service) fenceSurfacesLocked(run domain.RunID) []SurfaceSnapshot {
	s.mu.Lock()
	type entry struct {
		surface Surface
		state   *surfaceState
	}
	var states []entry
	for key, state := range s.surfaces {
		if key.run == run {
			states = append(states, entry{key.surface, state})
		}
	}
	s.mu.Unlock()
	var displaced []SurfaceSnapshot
	for _, entry := range states {
		// The revocation is committed; a failed record is already logged.
		if old, _ := s.fenceSurfaceLocked(string(run), entry.surface, entry.state); old != nil {
			displaced = append(displaced, *old)
		}
	}
	return displaced
}

// RevokeRunSurfaces revokes every development controller without releasing the
// primary lease. Use AdmitRevoke for run protection.
// Returned snapshots let the broker notify each displaced live stream.
func (s *Service) RevokeRunSurfaces(run string, revoke func() error) ([]SurfaceSnapshot, error) {
	if s.validateRun(run) != nil || revoke == nil {
		return nil, ErrInvalid
	}
	state := s.stateFor(domain.RunID(run), true)
	state.surfaceGate.Lock()
	defer state.surfaceGate.Unlock()
	if err := revoke(); err != nil {
		return nil, err
	}
	return s.fenceSurfacesLocked(domain.RunID(run)), nil
}

// HeldByMember reports whether a member in members holds run's primary
// lease or one of its surfaces, connected or within its reconnect window.
func (s *Service) HeldByMember(run string, members ...domain.MemberID) bool {
	if holder, ok := s.Status(run); ok && slices.Contains(members, holder.MemberID) {
		return true
	}
	s.mu.Lock()
	var surfaces []Surface
	for key := range s.surfaces {
		if key.run == domain.RunID(run) {
			surfaces = append(surfaces, key.surface)
		}
	}
	s.mu.Unlock()
	for _, surface := range surfaces {
		if snap, ok := s.SurfaceStatus(run, surface); ok && slices.Contains(members, snap.Principal.MemberID) {
			return true
		}
	}
	return false
}
