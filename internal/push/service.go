// Package push tells a run's owner, on the devices they subscribed, that
// the run started needing them. It is a Web Push application server (RFC
// 8030, with RFC 8291 encryption and RFC 8292 VAPID): the browser vendor's
// push service relays each message, and only the subscribed browser can
// read it.
//
// The service is a bus consumer. Every event that can change whether a run
// needs its owner makes it read the run again and decide from that, so what
// it announces is the run's state, not the event. A need is announced once,
// and only while it still stands. While the owner is using a dashboard the
// announcement waits, and is sent once they have been away from it for
// DefaultQuiet.
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
	"github.com/3xDevOps/Aether/internal/store"
)

// DefaultQuiet is how long a member must have left every dashboard alone
// before a notification is sent to their devices.
const DefaultQuiet = time.Minute

var (
	// ErrInvalid marks a subscription a browser could not have produced.
	ErrInvalid = errors.New("push: invalid subscription")
	// ErrDelivery marks a message the push service did not take.
	ErrDelivery = errors.New("push: the push service did not take the notification")
)

// Store is the persistence the service reads and writes.
type Store interface {
	GetRun(ctx context.Context, id domain.RunID) (*domain.Run, error)
	ListApprovals(ctx context.Context, workspace domain.WorkspaceID, decision string) ([]*store.Approval, error)
	store.PushStore
}

// Runs is what the scheduler knows about a live run that its row does not.
type Runs interface {
	PendingInputs(run domain.RunID) []domain.RunInputRequest
	Paused(run domain.RunID) bool
}

// Config wires the service. Store, Bus, Runs and KeyPath are required.
type Config struct {
	Store Store
	Bus   events.Bus
	Runs  Runs
	// KeyPath is the server's VAPID key file, created on first use.
	KeyPath string
	// Client replaces the public-addresses-only client, for tests.
	Client *http.Client
	// Quiet overrides DefaultQuiet.
	Quiet time.Duration
}

// Service announces needs and manages subscriptions.
type Service struct {
	store  Store
	bus    events.Bus
	runs   Runs
	key    *vapidKey
	client *http.Client
	quiet  time.Duration

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
	if cfg.Store == nil || cfg.Bus == nil || cfg.Runs == nil || cfg.KeyPath == "" {
		return nil, errors.New("push: Store, Bus, Runs and KeyPath are required")
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
		key:     key,
		client:  cfg.Client,
		quiet:   cfg.Quiet,
		active:  map[domain.MemberID]time.Time{},
		watched: map[domain.RunID]chan struct{}{},
	}, nil
}

// Start subscribes to the events that can change a run's need; a pause, a
// resume and a handoff are timeline entries. ctx bounds only the setup; the
// service runs until Close.
func (s *Service) Start(ctx context.Context) error {
	sub, err := s.bus.Subscribe(ctx, events.SubscribeOptions{Filter: events.Filter{Types: []events.Type{
		events.TypeRunStatus, events.TypeRunInput, events.TypeApproval, events.TypeRunOutcomeSeen,
		events.TypeRunMode, events.TypeTimeline, events.TypeRunDeleted,
	}}})
	if err != nil {
		return fmt.Errorf("push: subscribe: %w", err)
	}
	loopCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	s.mu.Lock()
	s.sub, s.stop = sub, cancel
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
			s.changed(ctx, e.RunID)
		}
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
	for {
		run, n := s.read(ctx, id)
		if n == nil {
			if s.unwatch(id, signal) {
				return
			}
			clear(announced)
			clear(offered)
		}
		var free <-chan time.Time
		if n != nil && !announced[n.key] {
			if wait := time.Until(s.freeAt(run.MemberID)); wait > 0 {
				free = time.After(wait)
			} else if s.offer(ctx, run, n, offered) {
				continue
			} else {
				announced[n.key] = true
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-signal:
		case <-free:
		}
	}
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

// offer sends n to the next device of the run's owner that has not had it,
// and reports whether there was one.
func (s *Service) offer(ctx context.Context, run *domain.Run, n *need, offered map[[2]string]bool) bool {
	payload, err := json.Marshal(notification{Title: RunTitle(run), Body: n.body, Run: string(run.ID)})
	if err != nil {
		slog.Warn("push: encode notification", "run", run.ID, "error", err)
		return false
	}
	subs, err := s.store.ListPushSubscriptions(ctx, run.MemberID)
	if err != nil {
		slog.Warn("push: list subscriptions", "member", run.MemberID, "error", err)
		return false
	}
	for _, sub := range subs {
		device := [2]string{n.key, sub.Endpoint}
		if offered[device] {
			continue
		}
		offered[device] = true
		if err := s.send(ctx, sub, payload, runTopic(run.ID)); err != nil {
			slog.Warn("push: notification not delivered", "member", run.MemberID, "error", err)
		}
		return true
	}
	return false
}

// read is the run as it stands and what it needs of its owner, if anything.
// The need is keyed to that owner: a run handed to another member is theirs
// to hear about.
func (s *Service) read(ctx context.Context, id domain.RunID) (*domain.Run, *need) {
	run, err := s.store.GetRun(ctx, id)
	var requested []*store.Approval
	if err == nil {
		requested, err = s.store.ListApprovals(ctx, run.WorkspaceID, store.ApprovalRequested)
	}
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			slog.Warn("push: read run", "run", id, "error", err)
		}
		return nil, nil
	}
	var approval *store.Approval
	for _, a := range requested {
		if a.RunID == id {
			approval = a
			break
		}
	}
	n := needOf(run, approval, s.runs.PendingInputs(id), s.runs.Paused(id))
	if n != nil {
		n.key = string(run.MemberID) + "|" + n.key
	}
	return run, n
}

func runTopic(run domain.RunID) string {
	digest := sha256.Sum256([]byte(run))
	return b64.EncodeToString(digest[:])[:32]
}

func (s *Service) Notify(ctx context.Context, member domain.MemberID, title, body string, run domain.RunID, flush <-chan struct{}, current func(context.Context) bool) error {
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
	var errs []error
	for _, sub := range subs {
		errs = append(errs, s.send(ctx, sub, payload, runTopic(run)))
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

func (s *Service) send(ctx context.Context, sub *store.PushSubscription, payload []byte, topic string) error {
	to, err := parseTarget(sub.Endpoint, sub.P256DH, sub.Auth)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	err = s.key.send(ctx, s.client, to, payload, topic)
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
	return s.send(ctx, sub, payload, "")
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
