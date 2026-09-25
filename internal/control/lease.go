// Package control owns the ephemeral authority to steer one run.
package control

import (
	"errors"
	"sync"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

const (
	// DefaultReconnectWindow is the time a disconnected controller may reclaim
	// its lease without a handoff.
	DefaultReconnectWindow = 15 * time.Second
	// DefaultMaxSessionIDBytes bounds the opaque client session identifier held
	// in memory by the controller.
	DefaultMaxSessionIDBytes = 256
)

var (
	// ErrOccupied means another session currently controls the run. Taking
	// over an occupied run requires force=true.
	ErrOccupied = errors.New("control: run is already controlled")
	// ErrStale means that the lease no longer names the current authority.
	ErrStale = errors.New("control: stale lease")
	// ErrInvalid is returned for an empty run or member identifier.
	ErrInvalid = errors.New("control: invalid request")
	// ErrInvalidSession is returned for an empty or oversized session ID.
	ErrInvalidSession = errors.New("control: invalid session ID")
	// ErrGenerationExhausted means the run has consumed every representable
	// generation and cannot install a new authority.
	ErrGenerationExhausted = errors.New("control: generation exhausted")
)

// Config configures a Service. A zero Config uses the production defaults.
type Config struct {
	// Now supplies the clock. It is called while the service lock is held.
	Now func() time.Time
	// ReconnectWindow is the deadline applied when a controller disconnects.
	ReconnectWindow time.Duration
	// MaxSessionIDBytes is the maximum byte length of a client session ID.
	MaxSessionIDBytes int
}

// Snapshot is a wire-safe description of the current controller. SessionID is
// opaque client state; it is never interpreted as a host path or credential.
type Snapshot struct {
	RunID      domain.RunID
	MemberID   domain.MemberID
	SessionID  string
	Generation uint64
	Connected  bool
	AcquiredAt time.Time
	ExpiresAt  time.Time
}

type lease struct {
	memberID   domain.MemberID
	sessionID  string
	generation uint64
	connected  bool
	acquiredAt time.Time
	expiresAt  time.Time
}

type runState struct {
	mu         sync.Mutex
	// surfaceGate linearizes run-wide revocation against surface admission.
	surfaceGate sync.RWMutex
	generation uint64
	current    *lease
}

// Service is a concurrency-safe in-memory controller lease table. Runtime
// control is deliberately ephemeral; each run's generation high-water mark is
// retained so release, fencing, and expiry cannot make an old write current.
type Service struct {
	mu sync.Mutex

	now             func() time.Time
	reconnectWindow time.Duration
	maxSessionBytes int
	runs            map[domain.RunID]*runState
	surfaces        map[surfaceKey]*surfaceState
}

// New creates an in-memory controller lease service.
func New(cfg Config) *Service {
	if cfg.Now == nil {
		cfg.Now = func() time.Time { return time.Now().UTC() }
	}
	if cfg.ReconnectWindow <= 0 {
		cfg.ReconnectWindow = DefaultReconnectWindow
	}
	if cfg.MaxSessionIDBytes <= 0 {
		cfg.MaxSessionIDBytes = DefaultMaxSessionIDBytes
	}
	return &Service{
		now:             cfg.Now,
		reconnectWindow: cfg.ReconnectWindow,
		maxSessionBytes: cfg.MaxSessionIDBytes,
		runs:            make(map[domain.RunID]*runState),
		surfaces:        make(map[surfaceKey]*surfaceState),
	}
}

// Acquire gives session uncontended control, or returns ErrOccupied unless
// force is set. A disconnected lease may resume within its reconnect window.
// A connected lease, including an older transport for the same session, must
// be explicitly taken over so two transports never share one generation.
// A forced takeover returns the displaced controller for notification.
func (s *Service) Acquire(run, member, session string, force bool) (Snapshot, *Snapshot, error) {
	return s.acquireAuthorized(run, member, session, force, 0, nil)
}

// AcquireAuthorized performs authorization and lease installation as one
// run-scoped linearization point. expectedGeneration is an optional
// reconnect/takeover precondition; zero means no precondition. authorize runs
// while this run's admission lock is held, after occupancy has been checked
// but before a replacement lease is installed. It must be bounded and must
// not call back into this Service.
func (s *Service) AcquireAuthorized(run, member, session string, force bool, expectedGeneration uint64, authorize func() error) (Snapshot, *Snapshot, error) {
	return s.acquireAuthorized(run, member, session, force, expectedGeneration, authorize)
}

func (s *Service) acquireAuthorized(run, member, session string, force bool, expectedGeneration uint64, authorize func() error) (Snapshot, *Snapshot, error) {
	if err := s.validateRunMember(run, member); err != nil {
		return Snapshot{}, nil, err
	}
	if err := s.validateSession(session); err != nil {
		return Snapshot{}, nil, err
	}

	runID := domain.RunID(run)
	state := s.stateFor(runID, true)
	state.mu.Lock()
	defer state.mu.Unlock()
	now := s.now()
	s.expireLocked(state, now)
	if expectedGeneration != 0 &&
		(state.current == nil || state.generation != expectedGeneration) {
		return Snapshot{}, nil, ErrStale
	}

	if current := state.current; current != nil {
		if current.memberID == domain.MemberID(member) &&
			current.sessionID == session &&
			!current.connected {
			if authorize != nil {
				if err := authorize(); err != nil {
					return Snapshot{}, nil, err
				}
			}
			current.connected = true
			current.expiresAt = time.Time{}
			return s.snapshotLocked(runID, current), nil, nil
		}
		if !force {
			return Snapshot{}, nil, ErrOccupied
		}
		displaced := s.snapshotLocked(runID, current)
		if authorize != nil {
			if err := authorize(); err != nil {
				return Snapshot{}, nil, err
			}
		}
		generation, genErr := s.nextGenerationLocked(state)
		if genErr != nil {
			return Snapshot{}, nil, genErr
		}
		current = &lease{
			memberID:   domain.MemberID(member),
			sessionID:  session,
			generation: generation,
			connected:  true,
			acquiredAt: now,
		}
		state.current = current
		result := s.snapshotLocked(runID, current)
		return result, &displaced, nil
	}

	if authorize != nil {
		if err := authorize(); err != nil {
			return Snapshot{}, nil, err
		}
	}
	generation, genErr := s.nextGenerationLocked(state)
	if genErr != nil {
		return Snapshot{}, nil, genErr
	}
	current := &lease{
		memberID:   domain.MemberID(member),
		sessionID:  session,
		generation: generation,
		connected:  true,
		acquiredAt: now,
	}
	state.current = current
	return s.snapshotLocked(runID, current), nil, nil
}

// Admit serializes one run-scoped write acceptance with every acquire,
// disconnect, release, and fence. The callback runs while this run's
// authorization lock is held; callers must keep it bounded and must not call
// back into this Service.
func (s *Service) Admit(run string, fn func() error) error {
	if err := s.validateRun(run); err != nil {
		return err
	}
	if fn == nil {
		return ErrInvalid
	}
	return s.AdmitSnapshot(run, func(Snapshot, bool) error {
		return fn()
	})
}

// AdmitSnapshot is Admit with the current controller snapshot supplied while
// the run admission lock is held. The boolean is false when no controller is
// present. The callback must not call back into this Service.
func (s *Service) AdmitSnapshot(run string, fn func(Snapshot, bool) error) error {
	if err := s.validateRun(run); err != nil {
		return err
	}
	if fn == nil {
		return ErrInvalid
	}
	state := s.stateFor(domain.RunID(run), true)
	state.mu.Lock()
	defer state.mu.Unlock()
	s.expireLocked(state, s.now())
	if state.current == nil {
		return fn(Snapshot{}, false)
	}
	return fn(s.snapshotLocked(domain.RunID(run), state.current), true)
}

// AdmitMember is Admit with a connected lease check in the same critical
// section as the accepted write. A fence cannot land between validation and
// the callback.
func (s *Service) AdmitMember(run string, member domain.MemberID, session string, generation uint64, fn func() error) error {
	if err := s.validateRunMember(run, string(member)); err != nil {
		return err
	}
	if err := s.validateSession(session); err != nil {
		return err
	}
	if fn == nil {
		return ErrInvalid
	}
	state := s.stateFor(domain.RunID(run), false)
	if state == nil {
		return ErrStale
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	s.expireLocked(state, s.now())
	current := state.current
	if current == nil || !current.connected || current.memberID != member ||
		current.generation != generation || current.sessionID != session {
		return ErrStale
	}
	return fn()
}

// AdmitRevoke commits a run-scoped revocation and fences its current lease
// before releasing the run lock. The durable mutation supplied by fn and the
// authority boundary therefore form one in-process linearization point. The
// returned snapshot is the lease displaced inside that boundary.
func (s *Service) AdmitRevoke(run string, fn func() error) (*Snapshot, error) {
	if err := s.validateRun(run); err != nil {
		return nil, err
	}
	if fn == nil {
		return nil, ErrInvalid
	}
	state := s.stateFor(domain.RunID(run), true)
	state.surfaceGate.Lock()
	defer state.surfaceGate.Unlock()
	state.mu.Lock()
	defer state.mu.Unlock()
	if err := fn(); err != nil {
		return nil, err
	}
	s.fenceSurfacesLocked(domain.RunID(run))
	return s.fenceLocked(domain.RunID(run), state), nil
}

// Validate authorizes one write against the current connected lease.
// Generations and session IDs are both required; presence or member identity
// is never inferred from an otherwise valid request.
func (s *Service) Validate(run, session string, generation uint64) error {
	if err := s.validateRun(run); err != nil {
		return err
	}
	if err := s.validateSession(session); err != nil {
		return err
	}

	state := s.stateFor(domain.RunID(run), false)
	if state == nil {
		return ErrStale
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	s.expireLocked(state, s.now())
	current := state.current
	if current == nil || !current.connected || current.generation != generation || current.sessionID != session {
		return ErrStale
	}
	return nil
}

// ValidateMember authorizes one moderation decision against the current
// connected controller lease. It checks member, session, and generation while
// holding the same lock that serializes acquire, release, and fencing.
func (s *Service) ValidateMember(run string, member domain.MemberID, session string, generation uint64) error {
	if err := s.validateRunMember(run, string(member)); err != nil {
		return err
	}
	if err := s.validateSession(session); err != nil {
		return err
	}

	state := s.stateFor(domain.RunID(run), false)
	if state == nil {
		return ErrStale
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	s.expireLocked(state, s.now())
	current := state.current
	if current == nil || !current.connected || current.memberID != member ||
		current.generation != generation || current.sessionID != session {
		return ErrStale
	}
	return nil
}

// Disconnect marks the matching connected lease unavailable until its
// reconnect deadline. Stale disconnect notifications are ignored.
func (s *Service) Disconnect(run, session string, generation uint64) {
	if s.validateRun(run) != nil || s.validateSession(session) != nil {
		return
	}

	state := s.stateFor(domain.RunID(run), false)
	if state == nil {
		return
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	now := s.now()
	s.expireLocked(state, now)
	current := state.current
	if current == nil || current.generation != generation || current.sessionID != session {
		return
	}
	if current.connected {
		current.connected = false
		current.expiresAt = now.Add(s.reconnectWindow)
	}
}

// Release relinquishes the matching current lease and fences its generation.
// A disconnected session may release during its reconnect window. The
// authenticated member, session, and explicit generation must all match.
func (s *Service) Release(run string, member domain.MemberID, session string, generation uint64) error {
	return s.releaseAdmitted(run, member, session, generation, nil)
}

// ReleaseAdmitted releases a lease only after admit succeeds inside the same
// run-scoped authority boundary. admit must be bounded and must not call back
// into this Service. It may acquire downstream locks that already follow the
// control-before-resource lock order.
func (s *Service) ReleaseAdmitted(run string, member domain.MemberID, session string, generation uint64, admit func() error) error {
	if admit == nil {
		return ErrInvalid
	}
	return s.releaseAdmitted(run, member, session, generation, admit)
}

func (s *Service) releaseAdmitted(run string, member domain.MemberID, session string, generation uint64, admit func() error) error {
	if err := s.validateRunMember(run, string(member)); err != nil {
		return err
	}
	if err := s.validateSession(session); err != nil {
		return err
	}
	if generation == 0 {
		return ErrStale
	}

	state := s.stateFor(domain.RunID(run), false)
	if state == nil {
		return ErrStale
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	s.expireLocked(state, s.now())
	current := state.current
	if current == nil || current.memberID != member ||
		current.generation != generation || current.sessionID != session {
		return ErrStale
	}
	advance := current.generation != ^uint64(0)
	if advance && state.generation == ^uint64(0) {
		return ErrGenerationExhausted
	}
	if admit != nil {
		if err := admit(); err != nil {
			return err
		}
	}
	if advance {
		state.generation++
	}
	state.current = nil
	return nil
}

// Fence revokes any current lease and advances the run generation. It returns
// the displaced controller when one was present.
func (s *Service) Fence(run string) *Snapshot {
	if s.validateRun(run) != nil {
		return nil
	}
	state := s.stateFor(domain.RunID(run), false)
	if state == nil {
		return nil
	}
	state.surfaceGate.Lock()
	defer state.surfaceGate.Unlock()
	state.mu.Lock()
	defer state.mu.Unlock()
	s.fenceSurfacesLocked(domain.RunID(run))
	return s.fenceLocked(domain.RunID(run), state)
}

func (s *Service) fenceLocked(runID domain.RunID, state *runState) *Snapshot {
	s.expireLocked(state, s.now())
	var displaced *Snapshot
	if current := state.current; current != nil {
		copy := s.snapshotLocked(runID, current)
		displaced = &copy
		state.current = nil
	}
	// Revocation remains safe at the maximum: Acquire refuses to install a
	// replacement once the high-water mark is exhausted.
	_, _ = s.nextGenerationLocked(state)
	return displaced
}

// Status returns the active controller, including a disconnected lease within
// its reconnect window. The boolean reports whether an authority is present.
func (s *Service) Status(run string) (Snapshot, bool) {
	if s.validateRun(run) != nil {
		return Snapshot{}, false
	}

	state := s.stateFor(domain.RunID(run), false)
	if state == nil {
		return Snapshot{}, false
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	s.expireLocked(state, s.now())
	if state.current == nil {
		return Snapshot{}, false
	}
	return s.snapshotLocked(domain.RunID(run), state.current), true
}

func (s *Service) validateRunMember(run, member string) error {
	if err := s.validateRun(run); err != nil {
		return err
	}
	if member == "" {
		return ErrInvalid
	}
	return nil
}

func (s *Service) validateRun(run string) error {
	if run == "" {
		return ErrInvalid
	}
	return nil
}

func (s *Service) validateSession(session string) error {
	if len(session) == 0 || len(session) > s.maxSessionBytes {
		return ErrInvalidSession
	}
	return nil
}

func (s *Service) stateLocked(run domain.RunID) *runState {
	state := s.runs[run]
	if state == nil {
		state = &runState{}
		s.runs[run] = state
	}
	return state
}

// stateFor holds the table mutex only long enough to obtain the stable state
// pointer. All authority transitions and admission callbacks use state.mu,
// so a blocked write for one run cannot stall unrelated runs.
func (s *Service) stateFor(run domain.RunID, create bool) *runState {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !create {
		return s.runs[run]
	}
	return s.stateLocked(run)
}

// expireLocked lazily removes a missed reconnect deadline. The next acquire
// advances the generation before installing new authority; this keeps one
// generation boundary for the expiry-to-reacquire transition.
func (s *Service) expireLocked(state *runState, now time.Time) {
	if state.current == nil || state.current.connected || state.current.expiresAt.After(now) {
		return
	}
	state.current = nil
}

func (s *Service) nextGenerationLocked(state *runState) (uint64, error) {
	if state.generation == ^uint64(0) {
		return 0, ErrGenerationExhausted
	}
	state.generation++
	return state.generation, nil
}

func (s *Service) snapshotLocked(run domain.RunID, current *lease) Snapshot {
	return Snapshot{
		RunID:      run,
		MemberID:   current.memberID,
		SessionID:  current.sessionID,
		Generation: current.generation,
		Connected:  current.connected,
		AcquiredAt: current.acquiredAt,
		ExpiresAt:  current.expiresAt,
	}
}
