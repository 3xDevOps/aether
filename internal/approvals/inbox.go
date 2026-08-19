// Package approvals is the shared approval inbox and the presence roster.
//
// The inbox is a session-wide queue of permission requests. Its source is
// the agent adapters: a run that pauses for a plan review or a permission
// prompt surfaces as a run.agent pause record, which this service turns
// into a stored request. Any member holding the steer capability decides
// it; the decision is attributed to that member and stamped into the
// timeline as a session.approval event. Auto mode is the default, so the
// queue is normally empty.
//
// The roster is presence: members are online while their heartbeats keep
// arriving and watching while they hold an attach, and they fall offline
// when the heartbeat goes stale. It is in-memory on purpose - presence is
// true only of the running server.
package approvals

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/store"
)

// DefaultTTL is how long a member stays online after their last
// heartbeat. Clients heartbeat well inside it.
const DefaultTTL = 90 * time.Second

// ErrRunMismatch is returned when a decision names a run the request does
// not belong to. The capability check is made against the named run, so a
// mismatch is a denial risk, not a typo to be forgiven.
var ErrRunMismatch = errors.New("approvals: request belongs to a different run")

// Config wires the service. Store and Bus are required.
type Config struct {
	Store store.ApprovalStore
	Bus   events.Bus
	// TTL overrides DefaultTTL; the expiry sweep runs at a third of it.
	TTL time.Duration
	// now overrides the clock in tests.
	now func() time.Time
}

// Service is the approval inbox plus the presence roster. It consumes the
// event bus for the requests it raises and for attach-derived watching.
type Service struct {
	store  store.ApprovalStore
	bus    events.Bus
	roster *roster
	ttl    time.Duration
	now    func() time.Time

	wg   sync.WaitGroup
	stop context.CancelFunc

	mu      sync.Mutex
	sub     events.Subscription
	closed  bool
	lastSeq uint64
}

// New builds the service; call Start to begin consuming events.
func New(cfg Config) (*Service, error) {
	if cfg.Store == nil || cfg.Bus == nil {
		return nil, errors.New("approvals: Store and Bus are required")
	}
	if cfg.TTL <= 0 {
		cfg.TTL = DefaultTTL
	}
	if cfg.now == nil {
		cfg.now = func() time.Time { return time.Now().UTC() }
	}
	return &Service{
		store:  cfg.Store,
		bus:    cfg.Bus,
		roster: newRoster(cfg.TTL, cfg.now),
		ttl:    cfg.TTL,
		now:    cfg.now,
	}, nil
}

// Start subscribes to the agent and presence streams and begins the
// presence expiry sweep. ctx bounds only the setup; the service runs
// until Close.
func (s *Service) Start(ctx context.Context) error {
	sub, err := s.subscribe(ctx, false, 0)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.sub = sub
	s.mu.Unlock()

	loopCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	s.stop = cancel
	s.wg.Add(2)
	go func() {
		defer s.wg.Done()
		s.consume(loopCtx)
	}()
	go func() {
		defer s.wg.Done()
		s.sweep(loopCtx)
	}()
	return nil
}

// Close stops consuming and waits for the goroutines to return.
// Idempotent.
func (s *Service) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	sub := s.sub
	s.mu.Unlock()
	if s.stop != nil {
		s.stop()
	}
	if sub != nil {
		_ = sub.Close()
	}
	s.wg.Wait()
	return nil
}

func (s *Service) subscribe(ctx context.Context, replay bool, afterSeq uint64) (events.Subscription, error) {
	opts := events.SubscribeOptions{
		Filter: events.Filter{Types: []events.Type{events.TypeAgentEvent, events.TypePresence}},
		Replay: replay,
	}
	if replay {
		opts.AfterSeq = afterSeq
	}
	sub, err := s.bus.Subscribe(ctx, opts)
	if err != nil {
		return nil, fmt.Errorf("approvals: subscribe: %w", err)
	}
	return sub, nil
}

// consume dispatches bus events until the service is closed. A subscriber
// that falls behind loses the oldest buffered events, which could drop an
// approval pause, so a detected drop resubscribes with replay from the
// cursor the gap opened at instead of ignoring it. The events the replay
// redelivers are handled again; raise is idempotent, so that costs nothing.
func (s *Service) consume(ctx context.Context) {
	for {
		sub := s.subscription()
		if sub == nil {
			return
		}
		gap := false
		// The lost events sit between the last event handled before the
		// drop and the one it was noticed on, so the replay starts from
		// the cursor as it stood before this event, not after it.
		from := s.cursor()
		for e := range sub.Events() {
			s.handle(ctx, e)
			if sub.Dropped() > 0 {
				gap = true
				break
			}
			from = s.cursor()
		}
		if s.isClosed() || ctx.Err() != nil {
			return
		}
		if err := sub.Err(); err != nil {
			// The subscription ended on a terminal error (a replay the
			// event log cannot serve). Resubscribing from the same cursor
			// would fail the same way, so stop instead of spinning.
			slog.Warn("approvals: subscription failed; stopping consumer", "error", err)
			return
		}
		if gap {
			_ = sub.Close()
		}
		next, err := s.subscribe(ctx, true, from)
		if err != nil {
			// Without an event log there is nothing to replay; a plain
			// resubscription still keeps presence and future requests live.
			if next, err = s.subscribe(ctx, false, 0); err != nil {
				if !errors.Is(err, events.ErrBusClosed) {
					slog.Warn("approvals: resubscribe failed; stopping consumer", "error", err)
				}
				return
			}
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			_ = next.Close()
			return
		}
		s.sub = next
		s.mu.Unlock()
	}
}

func (s *Service) handle(ctx context.Context, e events.Event) {
	switch p := e.Payload.(type) {
	case events.AgentEventPayload:
		if p.Kind == events.AgentPause {
			s.raise(ctx, e, p)
		}
	case events.PresencePayload:
		if e.ActorID == "" {
			break
		}
		switch p.State {
		case events.PresenceWatching:
			s.roster.watch(e.ActorID, e.SessionID, e.RunID)
		case events.PresenceOnline:
			// The SSH server publishes this when an attach ends; the
			// member is still connected, just no longer watching.
			s.roster.unwatch(e.ActorID, e.SessionID, e.RunID)
		}
	}
	s.mu.Lock()
	if e.Seq > s.lastSeq {
		s.lastSeq = e.Seq
	}
	s.mu.Unlock()
}

// raise stores the request a paused run is asking for and announces it.
// The pause's tool-use id is the request's identity within the run, so a
// pause delivered twice - a gap replay, a restart - resolves to the one
// stored request rather than a second entry in the inbox.
func (s *Service) raise(ctx context.Context, e events.Event, p events.AgentEventPayload) {
	action := p.Tool
	if action == "" {
		action = string(events.AgentPause)
	}
	a := &store.Approval{
		SessionID: e.SessionID,
		RunID:     e.RunID,
		SourceID:  p.ToolUseID,
		Action:    action,
		Detail:    p.Detail,
	}
	if err := s.store.CreateApproval(ctx, a); err != nil {
		slog.Warn("approvals: store request failed", "run", e.RunID, "error", err)
		return
	}
	s.publish(ctx, a, e.ActorID)
}

func (s *Service) publish(ctx context.Context, a *store.Approval, actor domain.MemberID) {
	if _, err := s.bus.Publish(ctx, events.Event{
		SessionID: a.SessionID,
		RunID:     a.RunID,
		ActorID:   actor,
		Payload: events.ApprovalPayload{
			RequestID: a.ID,
			Action:    a.Action,
			Decision:  events.ApprovalDecision(a.Decision),
		},
	}); err != nil {
		slog.Warn("approvals: publish failed", "request", a.ID, "error", err)
	}
}

// sweep drops members whose heartbeat went stale, publishing one offline
// event each.
func (s *Service) sweep(ctx context.Context) {
	interval := max(s.ttl/3, time.Millisecond)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			for _, p := range s.roster.expire() {
				s.publishPresence(ctx, p.Member, p.Session, events.PresenceOffline)
			}
		}
	}
}

// publishPresence announces a member-level transition (online, offline);
// the run-level watching transitions are the SSH server's attach events.
func (s *Service) publishPresence(ctx context.Context, member domain.MemberID, session domain.SessionID, state events.PresenceState) {
	if session == "" {
		return
	}
	if _, err := s.bus.Publish(ctx, events.Event{
		SessionID: session,
		ActorID:   member,
		Payload:   events.PresencePayload{State: state},
	}); err != nil {
		slog.Warn("approvals: publish presence failed", "member", member, "error", err)
	}
}

func (s *Service) subscription() events.Subscription {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	return s.sub
}

// cursor is the sequence number of the last event the loop handled.
func (s *Service) cursor() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastSeq
}

func (s *Service) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

// List returns a session's inbox, pending requests only unless all is set.
func (s *Service) List(ctx context.Context, session domain.SessionID, all bool) ([]*store.Approval, error) {
	decision := store.ApprovalRequested
	if all {
		decision = ""
	}
	list, err := s.store.ListApprovals(ctx, session, decision)
	if err != nil {
		return nil, fmt.Errorf("approvals: list: %w", err)
	}
	return list, nil
}

// Decide approves or denies a pending request on behalf of by. run is the
// run the caller's capability was checked against and must be the
// request's own run.
func (s *Service) Decide(ctx context.Context, id string, run domain.RunID, approve bool, by domain.MemberID) (*store.Approval, error) {
	a, err := s.store.GetApproval(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("approvals: decide %s: %w", id, err)
	}
	if a.RunID != run {
		return nil, ErrRunMismatch
	}
	decision := events.ApprovalDenied
	if approve {
		decision = events.ApprovalApproved
	}
	if derr := s.store.DecideApproval(ctx, id, string(decision), by, s.now()); derr != nil {
		return nil, fmt.Errorf("approvals: decide %s: %w", id, derr)
	}
	if a, err = s.store.GetApproval(ctx, id); err != nil {
		return nil, fmt.Errorf("approvals: decide %s: %w", id, err)
	}
	s.publish(ctx, a, by)
	return a, nil
}

// Heartbeat refreshes a member's presence, publishing the online
// transition the first time they appear.
func (s *Service) Heartbeat(ctx context.Context, member domain.MemberID, session domain.SessionID) error {
	if member == "" || session == "" {
		return errors.New("approvals: heartbeat needs a member and a session")
	}
	if s.roster.beat(member, session) {
		s.publishPresence(ctx, member, session, events.PresenceOnline)
	}
	return nil
}

// Roster lists present members, narrowed to a session and to the watchers
// of one run when either is given.
func (s *Service) Roster(session domain.SessionID, run domain.RunID) []Presence {
	return s.roster.snapshot(session, run)
}

// TTL is how long a member stays online without heartbeating; clients use
// it to pick their heartbeat interval.
func (s *Service) TTL() time.Duration { return s.ttl }
