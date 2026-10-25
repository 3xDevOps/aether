// Package approvals is the presence roster.
//
// The roster is presence: members are online while their heartbeats keep
// arriving and watching while they hold an attach, and they fall offline when
// the heartbeat goes stale or their last SSH connection closes. It is
// in-memory on purpose - presence is true only of the running server.
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
)

// DefaultTTL is how long a member stays online after their last
// heartbeat. Clients heartbeat well inside it.
const DefaultTTL = 45 * time.Second

// Config wires the service. Bus is required.
type Config struct {
	Bus events.Bus
	// TTL overrides DefaultTTL; the expiry sweep runs at a third of it.
	TTL time.Duration
	// now overrides the clock in tests.
	now func() time.Time
}

// Service is the presence roster. It consumes the event bus for
// attach-derived watching.
type Service struct {
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
	if cfg.Bus == nil {
		return nil, errors.New("approvals: Bus is required")
	}
	if cfg.TTL <= 0 {
		cfg.TTL = DefaultTTL
	}
	if cfg.now == nil {
		cfg.now = func() time.Time { return time.Now().UTC() }
	}
	return &Service{
		bus:    cfg.Bus,
		roster: newRoster(cfg.TTL, cfg.now),
		ttl:    cfg.TTL,
		now:    cfg.now,
	}, nil
}

// Start subscribes to the presence stream and begins the
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
		Filter: events.Filter{Types: []events.Type{events.TypePresence}},
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
// that falls behind loses the oldest buffered events, so a detected drop
// resubscribes with replay from the cursor the gap opened at instead of
// ignoring it.
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
			// resubscription still keeps presence live.
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
	if p, ok := e.Payload.(events.PresencePayload); ok && e.ActorID != "" {
		switch p.State {
		case events.PresenceWatching:
			s.roster.watch(e.ActorID, e.WorkspaceID, e.RunID)
		case events.PresenceOnline:
			// The SSH server publishes this when an attach ends; the
			// member is still connected, just no longer watching.
			for _, gone := range s.roster.unwatch(e.ActorID, e.WorkspaceID, e.RunID) {
				s.publishPresence(ctx, gone.Member, gone.Workspace, events.PresenceOffline)
			}
		}
	}
	s.mu.Lock()
	if e.Seq > s.lastSeq {
		s.lastSeq = e.Seq
	}
	s.mu.Unlock()
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
				s.publishPresence(ctx, p.Member, p.Workspace, events.PresenceOffline)
			}
		}
	}
}

// publishPresence announces a member-level transition (online, offline);
// the run-level watching transitions are the SSH server's attach events.
func (s *Service) publishPresence(ctx context.Context, member domain.MemberID, workspace domain.WorkspaceID, state events.PresenceState) {
	if workspace == "" {
		return
	}
	if _, err := s.bus.Publish(ctx, events.Event{
		WorkspaceID: workspace,
		ActorID:     member,
		Payload:     events.PresencePayload{State: state},
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

// ConnectionOpened records an authenticated SSH connection for member.
func (s *Service) ConnectionOpened(member domain.MemberID) {
	s.roster.connectionOpened(member)
}

// ConnectionClosed records the end of an authenticated SSH connection.
// When it was the member's last connection, publish their offline rows now.
func (s *Service) ConnectionClosed(member domain.MemberID) {
	for _, p := range s.roster.connectionClosed(member) {
		s.publishPresence(context.Background(), p.Member, p.Workspace, events.PresenceOffline)
	}
}

// Heartbeat refreshes a member's presence, publishing the online
// transition the first time they appear.
func (s *Service) Heartbeat(ctx context.Context, member domain.MemberID, workspace domain.WorkspaceID) error {
	if member == "" || workspace == "" {
		return errors.New("approvals: heartbeat needs a member and a workspace")
	}
	if s.roster.beat(member, workspace) {
		s.publishPresence(ctx, member, workspace, events.PresenceOnline)
	}
	return nil
}

// Roster lists present members, narrowed to a workspace and to the
// watchers of one run when either is given.
func (s *Service) Roster(workspace domain.WorkspaceID, run domain.RunID) []Presence {
	return s.roster.snapshot(workspace, run)
}

// TTL is how long a member stays online without heartbeating; clients use
// it to pick their heartbeat interval.
func (s *Service) TTL() time.Duration { return s.ttl }
