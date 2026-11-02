package control

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

// SurfaceLease is the durable form of one surface's lease table entry.
// Generation is the surface's generation high-water mark; a held surface's
// lease carries that generation.
type SurfaceLease struct {
	Generation uint64         `json:"generation"`
	Holder     *SurfaceHolder `json:"holder,omitempty"`
}

// SurfaceHolder names a lease's holder. A run agent's principal is the run
// the lease belongs to, so only a member carries an identifier.
type SurfaceHolder struct {
	Kind       PrincipalKind   `json:"kind"`
	MemberID   domain.MemberID `json:"member_id,omitempty"`
	SessionID  string          `json:"session_id"`
	AcquiredAt time.Time       `json:"acquired_at"`
}

func (s *Service) recordSurfaceLocked(run string, surface Surface, generation uint64, principal Principal, c *lease) error {
	if s.persistSurface == nil {
		return nil
	}
	record := SurfaceLease{Generation: generation}
	if c != nil {
		record.Holder = &SurfaceHolder{Kind: principal.Kind, MemberID: principal.MemberID, SessionID: c.sessionID, AcquiredAt: c.acquiredAt}
	}
	if err := s.persistSurface(domain.RunID(run), surface, record); err != nil {
		return fmt.Errorf("control: record the surface lease: %w", err)
	}
	return nil
}

// recordSurfaceStateLocked records a change already in effect in memory,
// which stays authoritative for this process if the record fails.
func (s *Service) recordSurfaceStateLocked(run string, surface Surface, state *surfaceState) error {
	err := s.recordSurfaceLocked(run, surface, state.generation, state.principal, state.current)
	if err != nil {
		slog.Warn("control: a restart may restore a surface lease this process ended", "run", run, "surface", surface.ID, "error", err)
	}
	return err
}

// RestoreSurface reinstates a lease recorded by an earlier server process at
// its recorded generation; everyone but its holder is fenced as before. A run
// agent holds its lease across calls rather than over a transport, so its
// lease comes back connected. A member's transport ended with that process,
// so a member's lease comes back disconnected with a fresh reconnect window,
// within which the member is admitted again at the same generation or may
// reacquire. A surface this process has already used is not restored. It
// never waits on an admission lock, so a caller may hold locks that
// PersistSurface takes.
func (s *Service) RestoreSurface(run string, surface Surface, record SurfaceLease) error {
	if err := s.validateSurface(run, surface); err != nil {
		return err
	}
	if record.Generation == 0 {
		return ErrInvalid
	}
	state := &surfaceState{}
	state.generation = record.Generation
	if h := record.Holder; h != nil {
		state.principal = Principal{Kind: h.Kind, MemberID: h.MemberID}
		if h.Kind == PrincipalRunAgent {
			state.principal.RunID = domain.RunID(run)
		}
		if err := s.validatePrincipal(run, state.principal); err != nil {
			return err
		}
		if err := s.validateSession(h.SessionID); err != nil {
			return err
		}
		state.current = &lease{sessionID: h.SessionID, generation: record.Generation, acquiredAt: h.AcquiredAt, connected: true}
		if h.Kind == PrincipalMember {
			state.current.connected, state.current.restored = false, true
			state.current.expiresAt = s.now().Add(s.reconnectWindow)
		}
	}
	runState := s.stateFor(domain.RunID(run), true)
	// A run-wide revocation holds the gate exclusively while it records.
	if !runState.surfaceGate.TryRLock() {
		return ErrAdmissionBusy
	}
	defer runState.surfaceGate.RUnlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	key := surfaceKey{domain.RunID(run), surface}
	if s.surfaces[key] != nil {
		return ErrOccupied
	}
	s.surfaces[key] = state
	return nil
}
