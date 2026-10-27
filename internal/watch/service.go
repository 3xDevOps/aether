// Package watch keeps a member's watches: durable subscriptions to a run, a
// swarm or a pull request that the server evaluates and announces on its
// own, whether or not a dashboard is open. It is a bus consumer that
// resumes from the last event it applied, reads each affected target as it
// stands now, records each notice once, and delivers it in-app and to the
// owner's Web Push devices. See docs/watches.md.
package watch

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/3xDevOps/Aether/internal/acphost"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/push"
	"github.com/3xDevOps/Aether/internal/store"
)

const (
	// DefaultDebounce is how long a burst of events about the same
	// watches is gathered before they are evaluated once.
	DefaultDebounce = 750 * time.Millisecond
	// DefaultTick is how often expiry, retries, held pushes and the purge
	// are checked.
	DefaultTick = 15 * time.Second
	// Retention is how long a done or expired watch is kept.
	Retention = 30 * 24 * time.Hour
	// DefaultExpiry and MaxExpiry bound a watch's life.
	DefaultExpiry = 14 * 24 * time.Hour
	MaxExpiry     = 90 * 24 * time.Hour
	// maxBackoff caps the wait between failing evaluations.
	maxBackoff = 15 * time.Minute
	// maxPushAttempts bounds the pushes of one notice.
	maxPushAttempts = 5
)

// Store is the persistence the service reads and writes.
type Store interface {
	store.WatchStore
	GetRun(ctx context.Context, id domain.RunID) (*domain.Run, error)
	GetMember(ctx context.Context, id domain.MemberID) (*domain.Member, error)
	GetWorkspace(ctx context.Context, id domain.WorkspaceID) (*domain.Workspace, error)
	GetMission(ctx context.Context, id domain.MissionID) (*domain.Mission, error)
	ListMissionQuestions(ctx context.Context, id domain.MissionID) ([]*domain.MissionQuestion, error)
	RunResumedSince(ctx context.Context, id domain.RunID, at time.Time) (bool, error)
}

// Runs is what the scheduler knows about a live run that its row does not.
type Runs interface {
	PendingInputs(run domain.RunID) []domain.RunInputRequest
	Paused(run domain.RunID) bool
	ACPPending(run domain.RunID) []acphost.Request
}

// Pusher delivers to a member's Web Push devices. Satisfied by
// *push.Service.
type Pusher interface {
	Deliver(ctx context.Context, member domain.MemberID, m push.Message) ([]push.DeviceResult, error)
	Devices(ctx context.Context, member domain.MemberID) (int, error)
	HeldUntil(member domain.MemberID) time.Time
}

// Config wires the service. Store, Bus, Runs and Swarm are required. Push
// nil records every push as no_destination.
type Config struct {
	Store Store
	Bus   events.Bus
	Runs  Runs
	// Swarm is the swarm row of the Needs you table, the same function
	// push.Config.Swarm is.
	Swarm push.Swarm
	Push  Pusher
	// Head is the event log's last sequence, where a service that never
	// saved a cursor starts.
	Head     func(ctx context.Context) (uint64, error)
	Debounce time.Duration
	Tick     time.Duration
	// Now replaces time.Now, for tests.
	Now func() time.Time

	// Recovered closes once the scheduler has reconciled the runs a restart
	// left. Until then a run's open requests are not known yet, and its
	// standing alone would be announced as a need of its own.
	Recovered <-chan struct{}
}

// Service evaluates watches and delivers their notices.
type Service struct {
	store    Store
	bus      events.Bus
	runs     Runs
	swarm    push.Swarm
	push     Pusher
	head     func(ctx context.Context) (uint64, error)
	debounce time.Duration
	tick     time.Duration
	now      func() time.Time
	pr       PRSource

	recovered <-chan struct{}

	kick chan struct{}
	wg   sync.WaitGroup
	stop context.CancelFunc

	// createMu makes finding a member's matching watch and inserting a new
	// one a single step.
	createMu sync.Mutex

	mu     sync.Mutex
	closed bool
	// cursor is the last event sequence whose evaluations are committed.
	cursor uint64
}

// New builds the service; call Start to begin.
func New(cfg Config) (*Service, error) {
	if cfg.Store == nil || cfg.Bus == nil || cfg.Runs == nil || cfg.Swarm == nil {
		return nil, errors.New("watch: Store, Bus, Runs and Swarm are required")
	}
	if cfg.Debounce <= 0 {
		cfg.Debounce = DefaultDebounce
	}
	if cfg.Tick <= 0 {
		cfg.Tick = DefaultTick
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Service{
		store: cfg.Store, bus: cfg.Bus, runs: cfg.Runs, swarm: cfg.Swarm, push: cfg.Push, head: cfg.Head, recovered: cfg.Recovered,
		debounce: cfg.Debounce, tick: cfg.Tick, now: cfg.Now, kick: make(chan struct{}, 1),
	}, nil
}

// UsePRSource enables pr targets and the pr_merged and ci conditions. Call
// it before Start.
func (s *Service) UsePRSource(pr PRSource) { s.pr = pr }

// Start loads the cursor, then in the background evaluates every live
// watch once, so a need that arose while the server was down is announced,
// and follows the bus from the cursor. ctx bounds only the setup.
func (s *Service) Start(ctx context.Context) error {
	cursor, ok, err := s.store.WatchCursor(ctx)
	if err != nil {
		return fmt.Errorf("watch: %w", err)
	}
	if !ok && s.head != nil {
		if cursor, err = s.head(ctx); err != nil {
			return fmt.Errorf("watch: read the event log head: %w", err)
		}
		if err := s.store.SetWatchCursor(ctx, cursor); err != nil {
			return fmt.Errorf("watch: %w", err)
		}
	}
	loopCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	s.mu.Lock()
	s.cursor, s.stop = cursor, cancel
	s.mu.Unlock()
	s.wg.Go(func() {
		if s.awaitRecovery(loopCtx) {
			s.evaluateAll(loopCtx)
			s.consume(loopCtx)
		}
	})
	s.wg.Go(func() {
		if s.awaitRecovery(loopCtx) {
			s.background(loopCtx)
		}
	})
	return nil
}

func (s *Service) awaitRecovery(ctx context.Context) bool {
	if s.recovered == nil {
		return true
	}
	select {
	case <-s.recovered:
		return true
	case <-ctx.Done():
		return false
	}
}

// Close stops the service and waits for it. Idempotent.
func (s *Service) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	stop := s.stop
	s.mu.Unlock()
	if stop != nil {
		stop()
	}
	s.wg.Wait()
	return nil
}

// evaluateAll evaluates every live watch once. Events replay only from
// the cursor, so a watch whose evaluation fails here is scheduled to be
// retried, as a failing read is.
func (s *Service) evaluateAll(ctx context.Context) {
	ids, err := s.store.DueWatchIDs(ctx, s.now(), true)
	for wait := time.Second; err != nil; wait = min(wait*2, time.Minute) {
		slog.Warn("watch: list watches to recover; trying again", "in", wait, "error", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		ids, err = s.store.DueWatchIDs(ctx, s.now(), true)
	}
	for _, id := range ids {
		if err := s.evaluate(ctx, id); err != nil && ctx.Err() == nil {
			slog.Warn("watch: evaluate after start; retrying with backoff", "watch", id, "error", err)
			if rerr := s.retryLater(ctx, id, err); rerr != nil && ctx.Err() == nil {
				slog.Warn("watch: schedule the retry", "watch", id, "error", rerr)
			}
		}
	}
}

// retryLater records failure on watch id and when to evaluate it again.
func (s *Service) retryLater(ctx context.Context, id string, failure error) error {
	w, err := s.store.GetWatch(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if w.State != protocol.WatchActive && w.State != protocol.WatchError {
		return nil
	}
	now := s.now()
	w.Failures++
	next := now.Add(backoff(w.Failures))
	w.State, w.Error, w.NextCheckAt, w.UpdatedAt = protocol.WatchError, failure.Error(), &next, now
	if _, err := s.store.CommitWatchEvaluation(ctx, w, nil); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return nil
		}
		return err
	}
	s.changed(ctx, w, "")
	return nil
}

var watchedTypes = []events.Type{
	events.TypeRunStatus, events.TypeRunInput, events.TypeRunOutcomeSeen, events.TypeRunReview,
	events.TypeRunArchived, events.TypeRunPR, events.TypeTimeline, events.TypeMissionChanged,
	events.TypeIntegrationChanged,
}

// consume follows the bus from the cursor, and from it again whenever the
// subscription fails or drops events.
func (s *Service) consume(ctx context.Context) {
	wait := time.Second
	for ctx.Err() == nil {
		s.mu.Lock()
		cursor := s.cursor
		s.mu.Unlock()
		sub, err := s.bus.Subscribe(ctx, events.SubscribeOptions{
			Filter: events.Filter{Types: watchedTypes}, Replay: true, AfterSeq: cursor, Buffer: 1024,
		})
		if err != nil {
			slog.Warn("watch: subscribe to events; trying again", "after_seq", cursor, "in", wait, "error", err)
		} else {
			healthy := s.drain(ctx, sub)
			_ = sub.Close()
			if healthy {
				wait = time.Second
				continue
			}
			slog.Warn("watch: event stream interrupted; resubscribing from the cursor", "error", sub.Err(), "dropped", sub.Dropped())
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = min(wait*2, time.Minute)
	}
}

// drain applies sub's events until it ends, reporting false when it ended
// short (an error or dropped events) rather than by Close. Events are
// gathered for the debounce, their watches evaluated once, and only then
// is the cursor advanced past them.
func (s *Service) drain(ctx context.Context, sub events.Subscription) bool {
	pending := map[string]bool{}
	var seq uint64
	var timer <-chan time.Time
	flush := func() {
		timer = nil
		for id := range pending {
			if err := s.evaluate(ctx, id); err != nil {
				if ctx.Err() == nil {
					slog.Warn("watch: evaluation not committed; trying again", "watch", id, "error", err)
				}
				continue
			}
			delete(pending, id)
		}
		if len(pending) > 0 {
			timer = time.After(s.debounce * 4)
			return
		}
		if err := s.advance(ctx, seq); err != nil && ctx.Err() == nil {
			slog.Warn("watch: save cursor", "seq", seq, "error", err)
		}
	}
	for {
		select {
		case <-ctx.Done():
			return true
		case <-timer:
			flush()
		case e, ok := <-sub.Events():
			if !ok || sub.Dropped() > 0 {
				flush()
				return !ok && sub.Err() == nil
			}
			ids, err := s.affected(ctx, e)
			if err != nil {
				slog.Warn("watch: find the watches an event affects", "seq", e.Seq, "error", err)
				flush()
				return false
			}
			for _, id := range ids {
				pending[id] = true
			}
			seq = e.Seq
			if timer == nil {
				timer = time.After(s.debounce)
			}
		}
	}
}

func (s *Service) advance(ctx context.Context, seq uint64) error {
	s.mu.Lock()
	if seq <= s.cursor {
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()
	if err := s.store.SetWatchCursor(ctx, seq); err != nil {
		return err
	}
	s.mu.Lock()
	s.cursor = max(s.cursor, seq)
	s.mu.Unlock()
	return nil
}

func (s *Service) affected(ctx context.Context, e events.Event) ([]string, error) {
	switch p := e.Payload.(type) {
	case events.MissionChangedPayload:
		return s.store.EvaluableWatchIDs(ctx, "", p.MissionID)
	case events.IntegrationChangedPayload:
		if p.MissionID == "" {
			return nil, nil
		}
		return s.store.EvaluableWatchIDs(ctx, "", p.MissionID)
	}
	if e.RunID == "" {
		return nil, nil
	}
	return s.store.EvaluableWatchIDs(ctx, e.RunID, "")
}

// background expires watches, retries failing ones and polls pull
// requests when due, sends pushes as they fall due, and purges old watches.
func (s *Service) background(ctx context.Context) {
	ticker := time.NewTicker(s.tick)
	defer ticker.Stop()
	for {
		s.housekeep(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-s.kick:
		}
	}
}

func (s *Service) housekeep(ctx context.Context) {
	now := s.now()
	expired, err := s.store.ExpireWatches(ctx, now)
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("watch: expire watches", "error", err)
		}
	}
	for _, w := range expired {
		s.changed(ctx, w, "")
	}
	due, err := s.store.DueWatchIDs(ctx, now, false)
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("watch: list due watches", "error", err)
		}
	}
	for _, id := range due {
		if err := s.evaluate(ctx, id); err != nil && ctx.Err() == nil {
			slog.Warn("watch: evaluate due watch", "watch", id, "error", err)
		}
	}
	s.deliverDue(ctx)
	if _, err := s.store.PurgeWatches(ctx, now.Add(-Retention)); err != nil {
		if ctx.Err() == nil {
			slog.Warn("watch: purge ended watches", "error", err)
		}
	}
}

// wake has the background loop run now, to send a push just recorded.
func (s *Service) wake() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

// changed tells the watch's owner to read it again.
func (s *Service) changed(ctx context.Context, w *store.Watch, notification string) {
	_, err := s.bus.Publish(ctx, events.Event{
		WorkspaceID: events.ServerScope,
		ActorID:     w.MemberID,
		Payload:     events.WatchChangedPayload{WatchID: w.ID, MemberID: w.MemberID, NotificationID: notification},
	})
	if err != nil && ctx.Err() == nil {
		slog.Warn("watch: publish watch.changed", "watch", w.ID, "error", err)
	}
}
