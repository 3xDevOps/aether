// Package push tells a member, on the devices they subscribed, that a run
// started needing them: the run's owner, or for a swarm's integrator the
// swarm's accountable human. It is a Web Push application server (RFC
// 8030, with RFC 8291 encryption and RFC 8292 VAPID): the browser vendor's
// push service relays each message, and only the subscribed browser can
// read it.
//
// The service is a bus consumer. Every event that can change what a run
// needs makes it read the run again, and a swarm's change every run of the
// swarm, and decide from that, so what it announces is the run's state, not
// the event. A need is announced once, and only while it still stands.
// While its member is using a dashboard the announcement waits, and is sent
// once they have been away from it for DefaultQuiet.
package push

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

// DefaultQuiet is how long a member must have left every dashboard alone
// before a notification is sent to their devices.
const DefaultQuiet = time.Minute

// retry is how long a watcher waits to try again what failed: reading
// its run, or telling the sink.
const retry = time.Minute

var (
	// ErrInvalid marks a subscription a browser could not have produced.
	ErrInvalid = errors.New("push: invalid subscription")
	// ErrDelivery marks a message the push service did not take.
	ErrDelivery = errors.New("push: the push service did not take the notification")
)

// Store is the persistence the service reads and writes.
type Store interface {
	GetRun(ctx context.Context, id domain.RunID) (*domain.Run, error)
	ListMissionRunIDs(ctx context.Context, id domain.MissionID) ([]domain.RunID, error)
	store.PushStore
}

// Runs is what the scheduler knows about a live run that its row does not.
type Runs interface {
	PendingInputs(run domain.RunID) []domain.RunInputRequest
	Paused(run domain.RunID) bool
}

// Swarm is the swarm row of the dashboard's Needs you table that holds for
// a run, nil when none does, and the member it waits on.
type Swarm func(ctx context.Context, run *domain.Run) (*protocol.AttentionItem, domain.MemberID, error)

// Config wires the service. Store, Bus, Runs, Swarm and KeyPath are
// required.
type Config struct {
	Store Store
	Bus   events.Bus
	Runs  Runs
	Swarm Swarm
	// KeyPath is the server's VAPID key file, created on first use.
	KeyPath string
	// Client replaces the public-addresses-only client, for tests.
	Client *http.Client
	// Quiet overrides DefaultQuiet.
	Quiet time.Duration
}

// Need is a need as a Sink hears of it: Key identifies it, Body is the
// notification's line, Member is who it waits on, and Answer is the id of
// the request it waits on, empty for the run's status. Mission is the swarm
// of a swarm's need, empty for the run's own.
type Need struct {
	Key, Body, Answer string
	Member            domain.MemberID
	Mission           domain.MissionID
}

// Sink is a second place a member hears of a run's needs, such as Slack.
// Need is called when a need is announced, after the member's devices;
// Clear whenever the run is read needing nothing. Either is called again
// after retry while it fails and the run stays as it was.
type Sink interface {
	Need(ctx context.Context, run *domain.Run, n Need) error
	Clear(ctx context.Context, run domain.RunID) error
}

type noSink struct{}

func (noSink) Need(context.Context, *domain.Run, Need) error { return nil }
func (noSink) Clear(context.Context, domain.RunID) error     { return nil }

// Service announces needs and manages subscriptions.
type Service struct {
	store  Store
	bus    events.Bus
	runs   Runs
	swarm  Swarm
	key    *vapidKey
	client *http.Client
	quiet  time.Duration
	sink   Sink
	// started is when Start ran. A run standing in a need it entered before
	// then was announced, if at all, by the server before this one.
	started time.Time

	wg   sync.WaitGroup
	stop context.CancelFunc

	mu     sync.Mutex
	sub    events.Subscription
	closed bool
	active map[domain.MemberID]time.Time
	// watched holds, for every run with a watcher, the signal that has it
	// read the run again.
	watched map[domain.RunID]chan struct{}
}

type notification struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	Run   string `json:"run,omitempty"`
}

// New loads the server's VAPID key, creating it on first use; call Start to
// begin consuming events.
func New(cfg Config) (*Service, error) {
	if cfg.Store == nil || cfg.Bus == nil || cfg.Runs == nil || cfg.Swarm == nil || cfg.KeyPath == "" {
		return nil, errors.New("push: Store, Bus, Runs, Swarm and KeyPath are required")
	}
	key, err := loadOrCreateKey(cfg.KeyPath)
	if err != nil {
		return nil, err
	}
	if cfg.Client == nil {
		cfg.Client = newClient()
	}
	if cfg.Quiet <= 0 {
		cfg.Quiet = DefaultQuiet
	}
	return &Service{
		store:   cfg.Store,
		bus:     cfg.Bus,
		runs:    cfg.Runs,
		swarm:   cfg.Swarm,
		key:     key,
		client:  cfg.Client,
		quiet:   cfg.Quiet,
		sink:    noSink{},
		active:  map[domain.MemberID]time.Time{},
		watched: map[domain.RunID]chan struct{}{},
	}, nil
}

// UseSink announces every need to sink too. Call it before Start.
func (s *Service) UseSink(sink Sink) { s.sink = sink }

// Start subscribes to the events that can change a run's need; a pause, a
// resume and a handoff are timeline entries. ctx bounds only the setup; the
// service runs until Close.
func (s *Service) Start(ctx context.Context) error {
	sub, err := s.bus.Subscribe(ctx, events.SubscribeOptions{Filter: events.Filter{Types: []events.Type{
		events.TypeRunStatus, events.TypeRunInput, events.TypeRunOutcomeSeen,
		events.TypeRunReview, events.TypeRunMode, events.TypeTimeline, events.TypeRunDeleted,
		events.TypeRunArchived, events.TypeMissionChanged,
	}}})
	if err != nil {
		return fmt.Errorf("push: subscribe: %w", err)
	}
	loopCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	s.mu.Lock()
	s.sub, s.stop, s.started = sub, cancel, time.Now()
	s.mu.Unlock()
	s.wg.Go(func() { s.consume(loopCtx, sub) })
	return nil
}

// Close stops consuming, abandons sends in flight and waits for them.
// Idempotent.
func (s *Service) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	sub, stop := s.sub, s.stop
	s.mu.Unlock()
	if stop != nil {
		stop()
	}
	if sub != nil {
		_ = sub.Close()
	}
	s.wg.Wait()
	return nil
}

// A dropped event costs at most one announcement, so a slow consumer is not
// replayed: an old need would be announced late.
func (s *Service) consume(ctx context.Context, sub events.Subscription) {
	for {
		select {
		case <-ctx.Done():
			return
		case e, ok := <-sub.Events():
			if !ok {
				return
			}
			if p, mission := e.Payload.(events.MissionChangedPayload); mission {
				s.swarmChanged(ctx, p.MissionID)
			} else {
				s.changed(ctx, e.RunID)
			}
		}
	}
}

// swarmChanged has every run the swarm had read again, so an integrator
// replaced since hears that it no longer needs anyone.
func (s *Service) swarmChanged(ctx context.Context, id domain.MissionID) {
	runs, err := s.store.ListMissionRunIDs(ctx, id)
	if err != nil {
		slog.Warn("push: list a swarm's runs; trying again later", "mission", id, "in", retry, "error", err)
		s.wg.Go(func() {
			select {
			case <-ctx.Done():
			case <-time.After(retry):
				s.swarmChanged(ctx, id)
			}
		})
		return
	}
	for _, run := range runs {
		s.changed(ctx, run)
	}
}

// changed has the run read again, by its watcher or by a new one.
func (s *Service) changed(ctx context.Context, id domain.RunID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	signal, watching := s.watched[id]
	if !watching {
		signal = make(chan struct{}, 1)
		s.watched[id] = signal
		s.wg.Go(func() { s.watch(ctx, id, signal) })
		return
	}
	select {
	case signal <- struct{}{}:
	default:
	}
}

// watch follows one run from the event that brought it up until it needs
// nothing. Every notification for a run replaces the last one on a device,
// so this goroutine alone decides and sends them, one device at a turn: none
// overtakes another, and each is decided from the run as it stands when it
// is sent.
func (s *Service) watch(ctx context.Context, id domain.RunID, signal chan struct{}) {
	announced := map[string]bool{}
	// offered is every need a device was sent, as {need, endpoint}, so one
	// that was interrupted goes on with the devices still without it.
	offered := map[[2]string]bool{}
	// shown is the need the sink last took, so one announced before and
	// back after another is shown again.
	var shown string
	for {
		run, n, err := s.read(ctx, id)
		var wake <-chan time.Time
		switch {
		case err != nil:
			slog.Warn("push: read run; trying again later", "run", id, "in", retry, "error", err)
			wake = time.After(retry)
		case n == nil:
			if err = s.sink.Clear(ctx, id); err != nil {
				slog.Warn("push: the sink did not clear a need; trying again later", "run", id, "in", retry, "error", err)
				wake = time.After(retry)
			} else if s.unwatch(id, signal) {
				return
			}
			clear(announced)
			clear(offered)
			shown = ""
		}
		if n != nil && !n.since.IsZero() && n.since.Before(s.startedAt()) {
			announced[n.key] = true
		}
		if n != nil && (!announced[n.key] || shown != "" && shown != n.key) {
			if wait := time.Until(s.freeAt(n.member)); wait > 0 {
				wake = time.After(wait)
			} else if !announced[n.key] && s.offer(ctx, run, n, offered) {
				continue
			} else if err = s.sink.Need(ctx, run, Need{Key: n.key, Body: n.body, Answer: n.answer, Member: n.member, Mission: n.mission}); err != nil {
				slog.Warn("push: the sink did not take a need; trying again later", "run", id, "in", retry, "error", err)
				wake = time.After(retry)
			} else {
				announced[n.key], shown = true, n.key
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-signal:
		case <-wake:
		}
	}
}

func (s *Service) startedAt() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.started
}

// unwatch ends a run's watcher unless an event is waiting for it.
func (s *Service) unwatch(id domain.RunID, signal chan struct{}) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(signal) > 0 {
		return false
	}
	delete(s.watched, id)
	return true
}

// offer sends n to the next device of its member that has not had it, and
// reports whether there was one.
func (s *Service) offer(ctx context.Context, run *domain.Run, n *need, offered map[[2]string]bool) bool {
	payload, err := json.Marshal(notification{Title: RunTitle(run), Body: n.body, Run: string(run.ID)})
	if err != nil {
		slog.Warn("push: encode notification", "run", run.ID, "error", err)
		return false
	}
	subs, err := s.store.ListPushSubscriptions(ctx, n.member)
	if err != nil {
		slog.Warn("push: list subscriptions", "member", n.member, "error", err)
		return false
	}
	for _, sub := range subs {
		device := [2]string{n.key, sub.Endpoint}
		if offered[device] {
			continue
		}
		offered[device] = true
		if err := s.send(ctx, sub, payload, runTopic(run.ID), n.urgency); err != nil {
			slog.Warn("push: notification not delivered", "member", n.member, "error", err)
		}
		return true
	}
	return false
}

// read is the run as it stands and what it needs, if anything, in the
// dashboard's order: its own requests, then its swarm's row, then its
// status. The need is keyed to the member it waits on: a run handed to
// another member is theirs to hear about.
func (s *Service) read(ctx context.Context, id domain.RunID) (*domain.Run, *need, error) {
	run, err := s.store.GetRun(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	n := needOf(run, s.runs.PendingInputs(id), s.runs.Paused(id))
	if n != nil {
		n.member = run.MemberID
	}
	if n == nil || n.answer == "" {
		item, human, swarmErr := s.swarm(ctx, run)
		if swarmErr != nil {
			return nil, nil, fmt.Errorf("push: read the swarm of run %s: %w", id, swarmErr)
		}
		if item != nil {
			n = swarmNeed(item, human)
		}
	}
	if n != nil {
		n.key = string(n.member) + "|" + n.key
	}
	return run, n, nil
}

func runTopic(run domain.RunID) string {
	digest := sha256.Sum256([]byte(run))
	return b64.EncodeToString(digest[:])[:32]
}

// Notify sends an assistant notice to member's devices; only an urgent one
// wakes them.
func (s *Service) Notify(ctx context.Context, member domain.MemberID, title, body string, run domain.RunID, urgent bool, flush <-chan struct{}, current func(context.Context) bool) error {
	for flushed := false; !flushed; {
		wait := time.Until(s.freeAt(member))
		if wait <= 0 {
			break
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("push: wait for %s to leave the dashboard: %w", member, ctx.Err())
		case <-flush:
			flushed = true
		case <-time.After(wait):
		}
	}
	if current != nil && !current(ctx) {
		return nil
	}
	payload, err := json.Marshal(notification{Title: clip(title, fallbackTitleRunes), Body: clip(body, maxBodyRunes), Run: string(run)})
	if err != nil {
		return fmt.Errorf("push: encode notification: %w", err)
	}
	subs, err := s.store.ListPushSubscriptions(ctx, member)
	if err != nil {
		return fmt.Errorf("push: list subscriptions: %w", err)
	}
	urgency := urgencyNormal
	if urgent {
		urgency = urgencyHigh
	}
	var errs []error
	for _, sub := range subs {
		errs = append(errs, s.send(ctx, sub, payload, runTopic(run), urgency))
	}
	return errors.Join(errs...)
}

// freeAt is when member will have been away from every dashboard for the
// quiet period.
func (s *Service) freeAt(member domain.MemberID) time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active[member].Add(s.quiet)
}

func (s *Service) send(ctx context.Context, sub *store.PushSubscription, payload []byte, topic, urgency string) error {
	to, err := parseTarget(sub.Endpoint, sub.P256DH, sub.Auth)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	err = s.key.send(ctx, s.client, to, payload, topic, urgency)
	if errors.Is(err, errGone) {
		if derr := s.store.DeletePushSubscription(ctx, sub.MemberID, sub.Endpoint); derr != nil && !errors.Is(derr, store.ErrNotFound) {
			return errors.Join(fmt.Errorf("%w: %w", ErrDelivery, err), derr)
		}
	}
	if err != nil {
		return fmt.Errorf("%w: %w", ErrDelivery, err)
	}
	return nil
}

// PublicKey is the key a browser subscribes with, as unpadded base64url.
func (s *Service) PublicKey() string { return s.key.public }

// Subscribed reports whether member has a subscription with endpoint.
func (s *Service) Subscribed(ctx context.Context, member domain.MemberID, endpoint string) (bool, error) {
	sub, err := s.find(ctx, member, endpoint)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	return sub != nil, err
}

// Subscribe stores a browser's subscription for member.
func (s *Service) Subscribe(ctx context.Context, member domain.MemberID, endpoint, p256dh, auth string) error {
	if _, err := parseTarget(endpoint, p256dh, auth); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	err := s.store.PutPushSubscription(ctx, &store.PushSubscription{
		Endpoint: endpoint, MemberID: member, P256DH: p256dh, Auth: auth,
	})
	if errors.Is(err, store.ErrLimit) {
		return fmt.Errorf("push: %d devices already receive your notifications; turn one off first: %w", store.MaxPushSubscriptions, err)
	}
	return err
}

// Unsubscribe removes member's subscription with endpoint.
func (s *Service) Unsubscribe(ctx context.Context, member domain.MemberID, endpoint string) error {
	return s.store.DeletePushSubscription(ctx, member, endpoint)
}

// Test sends a notification to one of member's devices now, and returns
// what the push service said when it did not take it.
func (s *Service) Test(ctx context.Context, member domain.MemberID, endpoint string) error {
	sub, err := s.find(ctx, member, endpoint)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(notification{Title: "Aether", Body: "Notifications work on this device."})
	if err != nil {
		return err
	}
	return s.send(ctx, sub, payload, "", urgencyHigh)
}

// Active records that member is using a dashboard now.
func (s *Service) Active(member domain.MemberID) {
	s.mu.Lock()
	s.active[member] = time.Now()
	s.mu.Unlock()
}

func (s *Service) find(ctx context.Context, member domain.MemberID, endpoint string) (*store.PushSubscription, error) {
	subs, err := s.store.ListPushSubscriptions(ctx, member)
	if err != nil {
		return nil, err
	}
	for _, sub := range subs {
		if sub.Endpoint == endpoint {
			return sub, nil
		}
	}
	return nil, fmt.Errorf("push: this device is not subscribed: %w", store.ErrNotFound)
}
