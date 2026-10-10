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

	mu      sync.Mutex
	sub     events.Subscription
	closed  bool
	active  map[domain.MemberID]time.Time
	waiting map[domain.RunID]*standing
}

// standing is a run that needs its owner: the need it was last seen with,
// when announcing that is due, and every need announced since the run last
// needed nothing.
type standing struct {
	key       string
	due       time.Time
	announced map[string]bool
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
		waiting: map[domain.RunID]*standing{},
	}, nil
}

// Start subscribes to the events that can change a run's need. ctx bounds
// only the setup; the service runs until Close.
func (s *Service) Start(ctx context.Context) error {
	sub, err := s.bus.Subscribe(ctx, events.SubscribeOptions{Filter: events.Filter{Types: []events.Type{
		events.TypeRunStatus, events.TypeRunInput, events.TypeApproval,
		events.TypeRunOutcomeSeen, events.TypeRunDeleted,
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

// consume owns s.waiting. A dropped event costs at most one announcement,
// so a slow consumer is not replayed: an old need would be announced late.
func (s *Service) consume(ctx context.Context, sub events.Subscription) {
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case e, ok := <-sub.Events():
			if !ok {
				return
			}
			s.evaluate(ctx, e.RunID, time.Now())
		case now := <-timer.C:
			for id, st := range s.waiting {
				if !st.announced[st.key] && !st.due.After(now) {
					s.evaluate(ctx, id, now)
				}
			}
		}
		next := time.Hour
		for _, st := range s.waiting {
			if !st.announced[st.key] {
				next = min(next, max(time.Until(st.due), 0))
			}
		}
		timer.Reset(next)
	}
}

// evaluate reads the run and announces its need when one is new and due.
func (s *Service) evaluate(ctx context.Context, id domain.RunID, now time.Time) {
	run, n, err := s.read(ctx, id)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			slog.Warn("push: read run", "run", id, "error", err)
		}
		delete(s.waiting, id)
		return
	}
	if n == nil {
		delete(s.waiting, id)
		return
	}
	st := s.waiting[id]
	if st == nil {
		st = &standing{announced: map[string]bool{}}
		s.waiting[id] = st
	}
	if st.key != n.key {
		st.key, st.due = n.key, now
	}
	if st.announced[n.key] || st.due.After(now) {
		return
	}
	if free := s.freeAt(run.MemberID); free.After(now) {
		st.due = free
		return
	}
	st.announced[n.key] = true
	payload, err := json.Marshal(notification{Title: runTitle(run), Body: n.body, Run: string(run.ID)})
	if err != nil {
		slog.Warn("push: encode notification", "run", id, "error", err)
		return
	}
	digest := sha256.Sum256([]byte(run.ID))
	topic := b64.EncodeToString(digest[:])[:32]
	member := run.MemberID
	s.wg.Go(func() { s.deliver(ctx, member, payload, topic) })
}

// read is the run as it stands and what it needs of its owner, if anything.
func (s *Service) read(ctx context.Context, id domain.RunID) (*domain.Run, *need, error) {
	run, err := s.store.GetRun(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	requested, err := s.store.ListApprovals(ctx, run.WorkspaceID, store.ApprovalRequested)
	if err != nil {
		return nil, nil, err
	}
	var approval *store.Approval
	for _, a := range requested {
		if a.RunID == id {
			approval = a
			break
		}
	}
	return run, needOf(run, approval, s.runs.PendingInputs(id), s.runs.Paused(id)), nil
}

// freeAt is when member will have been away from every dashboard for the
// quiet period.
func (s *Service) freeAt(member domain.MemberID) time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active[member].Add(s.quiet)
}

// deliver sends payload to every device of member, forgetting the ones the
// push service reports gone.
func (s *Service) deliver(ctx context.Context, member domain.MemberID, payload []byte, topic string) {
	subs, err := s.store.ListPushSubscriptions(ctx, member)
	if err != nil {
		slog.Warn("push: list subscriptions", "member", member, "error", err)
		return
	}
	for _, sub := range subs {
		if err := s.send(ctx, sub, payload, topic); err != nil {
			slog.Warn("push: notification not delivered", "member", member, "error", err)
		}
	}
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
