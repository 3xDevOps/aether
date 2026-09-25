package control

import (
	"errors"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

type PrincipalKind string

const (
	PrincipalMember PrincipalKind = "member"
	PrincipalRunAgent PrincipalKind = "run_agent"
	SurfaceTerminal SurfaceKind = "terminal"
	SurfaceBrowser SurfaceKind = "browser"
	MaxSurfaceIDBytes = 256
)

var ErrAgentTakeover = errors.New("control: agents cannot force surface takeover")

// Principal is derived from authentication, never from request JSON. A run
// agent has a RunID and no MemberID; a human has a MemberID and no RunID.
type Principal struct {
	Kind PrincipalKind
	MemberID domain.MemberID
	RunID domain.RunID
}

type SurfaceKind string

// Surface identifies a live resource incarnation, not an output or screen
// revision. The primary harness is deliberately not a development surface.
type Surface struct {
	Kind SurfaceKind
	ID string
	Incarnation string
}

type SurfaceSnapshot struct {
	RunID domain.RunID
	Surface Surface
	Principal Principal
	SessionID string
	Generation uint64
	Connected bool
	AcquiredAt time.Time
	ExpiresAt time.Time
}

type surfaceKey struct {
	run domain.RunID
	surface Surface
}

type surfaceState struct {
	runState
	principal Principal
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
		if p.MemberID != "" && p.RunID == "" { return nil }
	case PrincipalRunAgent:
		if p.MemberID == "" && p.RunID == domain.RunID(run) && p.RunID != "" { return nil }
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
// and mission holds remain the caller's responsibility; this API never changes
// a primary lease or a durable worker hold.
func (s *Service) AcquireSurface(run string, surface Surface, principal Principal, session string, force bool, expectedGeneration uint64, authorize func() error) (SurfaceSnapshot, *SurfaceSnapshot, error) {
	if err := s.validateSurface(run, surface); err != nil { return SurfaceSnapshot{}, nil, err }
	if err := s.validatePrincipal(run, principal); err != nil { return SurfaceSnapshot{}, nil, err }
	if err := s.validateSession(session); err != nil { return SurfaceSnapshot{}, nil, err }
	if force && principal.Kind == PrincipalRunAgent { return SurfaceSnapshot{}, nil, ErrAgentTakeover }
	if authorize == nil { return SurfaceSnapshot{}, nil, ErrInvalid }
	runState := s.stateFor(domain.RunID(run), true)
	runState.surfaceGate.RLock()
	defer runState.surfaceGate.RUnlock()
	state := s.surfaceStateFor(run, surface, true)
	state.mu.Lock()
	defer state.mu.Unlock()
	now := s.now()
	s.expireLocked(&state.runState, now)
	if expectedGeneration != 0 && (state.current == nil || state.generation != expectedGeneration) { return SurfaceSnapshot{}, nil, ErrStale }
	var displaced *SurfaceSnapshot
	if c := state.current; c != nil {
		if state.principal == principal && c.sessionID == session && !c.connected {
			if err := authorize(); err != nil { return SurfaceSnapshot{}, nil, err }
			c.connected = true
			c.expiresAt = time.Time{}
			return surfaceSnapshot(run, surface, state), nil, nil
		}
		if !force { return SurfaceSnapshot{}, nil, ErrOccupied }
		copy := surfaceSnapshot(run, surface, state)
		displaced = &copy
	}
	if err := authorize(); err != nil { return SurfaceSnapshot{}, nil, err }
	generation, err := s.nextGenerationLocked(&state.runState)
	if err != nil { return SurfaceSnapshot{}, nil, err }
	state.principal = principal
	state.current = &lease{sessionID: session, generation: generation, connected: true, acquiredAt: now}
	return surfaceSnapshot(run, surface, state), displaced, nil
}

// withSurface holds only this surface's admission lock (plus the shared run
// revocation gate), so unrelated surfaces may admit writes concurrently.
func (s *Service) withSurface(run string, surface Surface, fn func(*surfaceState) error) error {
	if err := s.validateSurface(run, surface); err != nil { return err }
	runState := s.stateFor(domain.RunID(run), false)
	if runState == nil { return ErrStale }
	runState.surfaceGate.RLock()
	defer runState.surfaceGate.RUnlock()
	state := s.surfaceStateFor(run, surface, false)
	if state == nil { return ErrStale }
	state.mu.Lock()
	defer state.mu.Unlock()
	s.expireLocked(&state.runState, s.now())
	return fn(state)
}

func (s *Service) matchSurface(state *surfaceState, principal Principal, session string, generation uint64, connected bool) bool {
	c := state.current
	return c != nil && state.principal == principal && c.sessionID == session && c.generation == generation && (!connected || c.connected)
}

// AdmitSurface checks the lease and accepts a mutation at the same boundary as
// takeover/revocation. fn must recheck current authorization before the effect.
func (s *Service) AdmitSurface(run string, surface Surface, principal Principal, session string, generation uint64, fn func() error) error {
	if s.validatePrincipal(run, principal) != nil || fn == nil { return ErrInvalid }
	if err := s.validateSession(session); err != nil { return err }
	return s.withSurface(run, surface, func(state *surfaceState) error {
		if !s.matchSurface(state, principal, session, generation, true) { return ErrStale }
		return fn()
	})
}

// ReleaseSurface releases only this incarnation's matching lease. admit may
// revalidate current authority; it cannot clear a durable run hold.
func (s *Service) ReleaseSurface(run string, surface Surface, principal Principal, session string, generation uint64, admit func() error) error {
	if s.validatePrincipal(run, principal) != nil || admit == nil { return ErrInvalid }
	if err := s.validateSession(session); err != nil { return err }
	return s.withSurface(run, surface, func(state *surfaceState) error {
		if !s.matchSurface(state, principal, session, generation, false) { return ErrStale }
		if err := admit(); err != nil { return err }
		s.fenceSurfaceLocked(run, surface, state)
		return nil
	})
}

func (s *Service) DisconnectSurface(run string, surface Surface, principal Principal, session string, generation uint64) {
	_ = s.withSurface(run, surface, func(state *surfaceState) error {
		if s.matchSurface(state, principal, session, generation, true) {
			state.current.connected = false
			state.current.expiresAt = s.now().Add(s.reconnectWindow)
		}
		return nil
	})
}

func (s *Service) SurfaceStatus(run string, surface Surface) (SurfaceSnapshot, bool) {
	var result SurfaceSnapshot
	present := false
	_ = s.withSurface(run, surface, func(state *surfaceState) error {
		if state.current != nil { result, present = surfaceSnapshot(run, surface, state), true }
		return nil
	})
	return result, present
}

func (s *Service) fenceSurfaceLocked(run string, surface Surface, state *surfaceState) *SurfaceSnapshot {
	var displaced *SurfaceSnapshot
	if state.current != nil {
		copy := surfaceSnapshot(run, surface, state)
		displaced = &copy
		state.current = nil
	}
	_, _ = s.nextGenerationLocked(&state.runState)
	return displaced
}

// RevokeSurface commits an authority change and fences only this surface. The
// callback is run even for an unoccupied surface, but never without its gate.
func (s *Service) RevokeSurface(run string, surface Surface, revoke func() error) (*SurfaceSnapshot, error) {
	if err := s.validateSurface(run, surface); err != nil { return nil, err }
	if revoke == nil { return nil, ErrInvalid }
	runState := s.stateFor(domain.RunID(run), true)
	runState.surfaceGate.RLock()
	defer runState.surfaceGate.RUnlock()
	state := s.surfaceStateFor(run, surface, true)
	state.mu.Lock()
	defer state.mu.Unlock()
	s.expireLocked(&state.runState, s.now())
	if err := revoke(); err != nil { return nil, err }
	return s.fenceSurfaceLocked(run, surface, state), nil
}

// fenceSurfacesLocked requires the run's exclusive surfaceGate. Called by the
// existing run revocation APIs so protection and permission changes cannot
// leave a development writer admitted after the run-wide boundary.
func (s *Service) fenceSurfacesLocked(run domain.RunID) []SurfaceSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	var displaced []SurfaceSnapshot
	for key, state := range s.surfaces {
		if key.run != run { continue }
		s.expireLocked(&state.runState, s.now())
		if old := s.fenceSurfaceLocked(string(run), key.surface, state); old != nil { displaced = append(displaced, *old) }
	}
	return displaced
}

// RevokeRunSurfaces revokes every development controller without releasing the
// primary lease or touching mission holds. Use AdmitRevoke for run protection.
// Returned snapshots let the broker notify each displaced live stream.
func (s *Service) RevokeRunSurfaces(run string, revoke func() error) ([]SurfaceSnapshot, error) {
	if s.validateRun(run) != nil || revoke == nil { return nil, ErrInvalid }
	state := s.stateFor(domain.RunID(run), true)
	state.surfaceGate.Lock()
	defer state.surfaceGate.Unlock()
	if err := revoke(); err != nil { return nil, err }
	return s.fenceSurfacesLocked(domain.RunID(run)), nil
}
