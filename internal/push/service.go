// Package push tells a member, on the devices they subscribed, that a run
// started needing them: the run's owner. It is a Web Push application server (RFC
// 8030, with RFC 8291 encryption and RFC 8292 VAPID): the browser vendor's
// push service relays each message, and only the subscribed browser can
// read it.
//
// The service is a bus consumer. Every event that can change what a run
// needs makes it read the run again, and decide from that, so what it
// announces is the run's state, not the event. A need is announced once, and only while it still stands.
// While its member is using a dashboard the announcement waits, and is sent
// once they have been away from it for DefaultQuiet.
//
// It also tells every admin, the same way, when a workspace's spend rises
// past its budget's warning or its cap.
package push

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
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
	GetWorkspace(ctx context.Context, id domain.WorkspaceID) (*domain.Workspace, error)
	ListMembers(ctx context.Context) ([]*domain.Member, error)
	store.PushStore
}

// Runs is what the scheduler knows about a live run that its row does not.
type Runs interface {
	PendingInputs(run domain.RunID) []domain.RunInputRequest
	Paused(run domain.RunID) bool
}

// Config wires the service. Store, Bus, Runs, Budgets and KeyPath are
// required.
type Config struct {
	Store   Store
	Bus     events.Bus
	Runs    Runs
	Budgets Budgets
	// KeyPath is the server's VAPID key file, created on first use.
	KeyPath string
	// Client replaces the public-addresses-only client, for tests.
	Client *http.Client
	// Quiet overrides DefaultQuiet.
	Quiet time.Duration
	// Recovered closes once the scheduler has reconciled the runs a restart
	// found; digests wait for it, since a session not yet recovered reads
	// as needing nothing. Nil starts them at once.
	Recovered <-chan struct{}
	// Now replaces time.Now when the quiet period, levels, digests and
	// bursts read it, for tests. The waits stay on real timers, which
	// re-read Now when they fire, so a clock that jumps does not wake a
	// waiting notice early.
	Now func() time.Time
}

// Need is a need as a Sink hears of it: Key identifies it, Body is the
// notification's line, Member is who it waits on, and Answer is the id of
// the request it waits on, empty for the run's status.
type Need struct {
	Key, Body, Answer string
	Member            domain.MemberID
}

// Sink is a second place a member hears of a run's needs, such as Slack.
// Need is called when a need is announced, after the member's devices;
// Clear whenever the run is read needing nothing. Either is called again
// after retry while it fails and the run stays as it was. Notice is a
// member's news of a workspace, after their devices, called again after
// retry while it fails and the news still stands.
type Sink interface {
	Need(ctx context.Context, run *domain.Run, n Need) error
	Clear(ctx context.Context, run domain.RunID) error
	Notice(ctx context.Context, member domain.MemberID, n Notice) error
}

type noSink struct{}

func (noSink) Need(context.Context, *domain.Run, Need) error         { return nil }
func (noSink) Clear(context.Context, domain.RunID) error             { return nil }
func (noSink) Notice(context.Context, domain.MemberID, Notice) error { return nil }

// Service announces needs and manages subscriptions.
type Service struct {
	store   Store
	bus     events.Bus
	runs    Runs
	budgets Budgets
	key     *vapidKey
	client  *http.Client
	quiet   time.Duration
	now     func() time.Time
	sink    Sink
	sources map[string]DigestSource
	cards   Cards
	// recovered is Config.Recovered.
	recovered <-chan struct{}
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
	// notices holds, for every admin with a workspace's budget notice
	// to send, what hands its watcher the state to announce.
	notices map[budgetKey]chan budgetDue
	bursts  map[domain.MemberID][]*burst
	// cardPushes has a lock of its own: reading cards and pushing them
	// must not hold mu.
	cardPushes cardPushes
	// digestRetry and digestAttempts hold, for a member whose digest no
	// device took, when to try again and how often it was tried.
	digestRetry    map[domain.MemberID]time.Time
	digestAttempts map[domain.MemberID]int
}

type notification struct {
	Title     string `json:"title"`
	Body      string `json:"body"`
	Run       string `json:"run,omitempty"`
	Workspace string `json:"workspace,omitempty"`
	// Clear replaces the run's notification quietly: its need ended.
	Clear bool `json:"clear,omitempty"`
	// Notice marks news, which a clear leaves shown: an assistant's, a
	// digest or a burst's summary.
	Notice bool `json:"notice,omitempty"`
	// Quiet replaces a notification without alerting again.
	Quiet bool `json:"quiet,omitempty"`
	// Card is the assistant card a notification asks to decide, which the
	// device approves or declines with its own sign-in.
	Card string `json:"card,omitempty"`
}

// clearedBody is what a device shows in place of a need that ended, and
// clearedFinishedBody in place of a finished session's notice.
const (
	clearedBody         = "No longer needs you"
	clearedFinishedBody = "No longer awaiting review"
)

// New loads the server's VAPID key, creating it on first use; call Start to
// begin consuming events.
func New(cfg Config) (*Service, error) {
	if cfg.Store == nil || cfg.Bus == nil || cfg.Runs == nil || cfg.Budgets == nil || cfg.KeyPath == "" {
		return nil, errors.New("push: Store, Bus, Runs, Budgets and KeyPath are required")
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
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Service{
		store:   cfg.Store,
		bus:     cfg.Bus,
		runs:    cfg.Runs,
		budgets: cfg.Budgets,
		key:     key,
		client:  cfg.Client,
		quiet:   cfg.Quiet,
		now:     cfg.Now,

		recovered: cfg.Recovered,
		sink:      noSink{},
		sources:   map[string]DigestSource{},
		active:    map[domain.MemberID]time.Time{},
		watched:   map[domain.RunID]chan struct{}{},
		notices:   map[budgetKey]chan budgetDue{},
		bursts:    map[domain.MemberID][]*burst{},

		cardPushes: cardPushes{cards: map[domain.MemberID]map[string]bool{}, held: map[domain.MemberID]bool{}},

		digestRetry:    map[domain.MemberID]time.Time{},
		digestAttempts: map[domain.MemberID]int{},
	}, nil
}

// UseSink announces every need to sink too. Call it before Start.
func (s *Service) UseSink(sink Sink) { s.sink = sink }

// Start subscribes to the events that can change a run's need, and to
// workspace budgets; a pause, a resume and a handoff are timeline entries.
// ctx bounds only the setup; the service runs until Close.
func (s *Service) Start(ctx context.Context) error {
	sub, err := s.bus.Subscribe(ctx, events.SubscribeOptions{Filter: events.Filter{Types: []events.Type{
		events.TypeRunStatus, events.TypeRunInput, events.TypeRunOutcomeSeen,
		events.TypeRunReview, events.TypeRunMode, events.TypeTimeline, events.TypeRunDeleted,
		events.TypeRunArchived, events.TypeBudget, events.TypeAssistantCards,
	}}})
	if err != nil {
		return fmt.Errorf("push: subscribe: %w", err)
	}
	loopCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	s.mu.Lock()
	s.sub, s.stop, s.started = sub, cancel, time.Now()
	s.mu.Unlock()
	s.wg.Go(func() { s.consume(loopCtx, sub) })
	s.wg.Go(func() { s.digests(loopCtx) })
	s.wg.Go(func() { s.restoreCards(loopCtx) })
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
			switch p := e.Payload.(type) {
			case events.BudgetPayload:
				s.budget(ctx, e, p)
			case events.AssistantCardsPayload:
				s.wg.Go(func() { s.cardsChanged(ctx, p.MemberID) })
			default:
				s.changed(ctx, e.RunID)
			}
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
	// that was interrupted goes on with the devices still without it. Each
	// maps to the member whose device may show it, empty when the push
	// service surely did not take it, so only such a device is cleared.
	offered := map[[2]string]domain.MemberID{}
	// shown is the need the sink last took, so one announced before and
	// back after another is shown again.
	var shown string
	for {
		run, n, err := s.read(ctx, id)
		var wake <-chan time.Time
		switch {
		case err != nil:
			slog.Warn("push: read session; trying again later", "run", id, "in", retry, "error", err)
			wake = time.After(retry)
		case n == nil:
			// Devices and the sink are cleared independently, each tried
			// again while it fails.
			devicesErr := s.withdraw(ctx, run, id, offered, "")
			if devicesErr != nil {
				slog.Warn("push: a device was not told a need ended; trying again later", "run", id, "in", retry, "error", devicesErr)
			}
			// A summary is updated only once this need's own clear went
			// out, so a clear retried later never lands over it.
			if devicesErr == nil {
				s.unjoin(ctx, string(id), "")
			}
			if err = s.sink.Clear(ctx, id); err != nil {
				slog.Warn("push: the sink did not clear a need; trying again later", "run", id, "in", retry, "error", err)
			}
			if devicesErr != nil || err != nil {
				wake = time.After(retry)
			} else if s.unwatch(id, signal) {
				return
			}
			clear(announced)
			// A device still to be told keeps its entry for the next try.
			for device, member := range offered {
				if member == "" {
					delete(offered, device)
				}
			}
			shown = ""
		}
		if n != nil {
			// A handed-off run's need is its new owner's alone. A former
			// owner whose devices were told so hears of it again if it
			// comes back to them.
			moved := false
			for _, member := range offered {
				moved = moved || member != "" && member != n.member
			}
			if err = s.withdraw(ctx, run, id, offered, n.member); err != nil {
				slog.Warn("push: a former owner's device was not told a need moved; trying again later", "run", id, "in", retry, "error", err)
				wake = time.After(retry)
			}
			for key := range announced {
				if former, _, _ := strings.Cut(key, "|"); former != string(n.member) {
					delete(announced, key)
					// The former owner's summary no longer shows the
					// run's need; other members' watches keep theirs.
					s.unjoin(ctx, string(id), domain.MemberID(former))
				}
			}
			// Clearing can take a while: announce what the run needs then.
			if moved && err == nil {
				continue
			}
		}
		if n != nil && !n.since.IsZero() && n.since.Before(s.startedAt()) {
			announced[n.key] = true
		}
		if n != nil && (!announced[n.key] || shown != "" && shown != n.key) {
			if wait := s.freeAt(n.member).Sub(s.now()); wait > 0 {
				wake = time.After(wait)
			} else if !announced[n.key] && s.offer(ctx, run, n, offered) {
				continue
			} else if err = s.sink.Need(ctx, run, Need{Key: n.key, Body: n.body, Answer: n.answer, Member: n.member}); err != nil {
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

// withdraw sends every device that took a need of the run, except those of
// keep, a quiet replacement under the run's tag and topic, so a need that
// ended stops reading as one, or is never shown if it was still queued. It
// forgets each device it cleared or that is no longer subscribed, and
// returns what kept the others for another try. run is nil when it was
// deleted.
func (s *Service) withdraw(ctx context.Context, run *domain.Run, id domain.RunID, offered map[[2]string]domain.MemberID, keep domain.MemberID) error {
	devices := map[domain.MemberID]map[string]bool{}
	body := clearedFinishedBody
	for device, member := range offered {
		if member == "" || member == keep {
			continue
		}
		if !strings.HasPrefix(device[0], string(member)+"|finished:") {
			body = clearedBody
		}
		if devices[member] == nil {
			devices[member] = map[string]bool{}
		}
		devices[member][device[1]] = true
	}
	if len(devices) == 0 {
		return nil
	}
	message := notification{Body: body, Run: string(id), Clear: true}
	if run != nil {
		message.Title = RunTitle(run)
	}
	payload, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("push: encode a cleared notification: %w", err)
	}
	forget := func(member domain.MemberID, endpoint string) {
		for device, took := range offered {
			if took == member && device[1] == endpoint {
				delete(offered, device)
			}
		}
	}
	var errs []error
	for member, endpoints := range devices {
		subs, err := s.store.ListPushSubscriptions(ctx, member)
		if err != nil {
			errs = append(errs, fmt.Errorf("push: list the subscriptions of %s to clear a notification: %w", member, err))
			continue
		}
		subscribed := map[string]*store.PushSubscription{}
		for _, sub := range subs {
			subscribed[sub.Endpoint] = sub
		}
		for endpoint := range endpoints {
			sub := subscribed[endpoint]
			if sub == nil {
				forget(member, endpoint)
				continue
			}
			if err := s.send(ctx, sub, payload, topicOf(string(id)), urgencyNormal); err != nil && !errors.Is(err, errGone) {
				errs = append(errs, fmt.Errorf("push: clear a notification on a device of %s: %w", member, err))
				continue
			}
			forget(member, endpoint)
		}
	}
	return errors.Join(errs...)
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
// reports whether there was one. Before the first device it routes n by
// the member's level: queued for their digest, or joined to their last
// push as its summary, n has no device to offer it to.
func (s *Service) offer(ctx context.Context, run *domain.Run, n *need, offered map[[2]string]domain.MemberID) bool {
	started := false
	for device := range offered {
		started = started || device[0] == n.key
	}
	if !started && s.routed(ctx, run, n) {
		return false
	}
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
		if _, sent := offered[device]; sent {
			continue
		}
		offered[device] = n.member
		if err := s.send(ctx, sub, payload, topicOf(string(run.ID)), n.urgency); err != nil {
			slog.Warn("push: notification not delivered", "member", n.member, "error", err)
			if !errors.As(err, new(unsure)) {
				offered[device] = ""
			}
		}
		return true
	}
	return false
}

// routed queues n for its member's digest, or sends it as the summary of
// their last push, and reports whether it did either. One it could not
// queue is pushed at once.
func (s *Service) routed(ctx context.Context, run *domain.Run, n *need) bool {
	kind := needKind(run, n)
	if kind != KindNeed {
		// The run stopped asking: a question it had in a summary goes.
		s.unjoin(ctx, string(run.ID), "")
	}
	prefs, err := s.store.GetNotifyPrefs(ctx, n.member)
	if err != nil {
		slog.Warn("push: read notification settings; pushing at once", "member", n.member, "error", err)
		prefs.Level = protocol.NotifyAll
	}
	// Routed as of when the need began and as of now, the later of the
	// two: one that began in quiet hours stays theirs though the
	// member's dashboard held it past their end, and one held into them
	// waits for them to end.
	now := s.now()
	route := RouteOf(prefs, kind, now)
	if !n.since.IsZero() && n.since.Before(now) {
		route = max(route, RouteOf(prefs, kind, n.since))
	}
	if route != RouteNow {
		if err = s.queueNeed(ctx, run, n, kind, route == RouteOvernight); err == nil {
			return true
		}
		slog.Warn("push: queue a notification for the digest; pushing at once", "run", run.ID, "error", err)
	}
	sum, err := s.join(n.member, string(run.ID), string(run.ID), topicOf(string(run.ID)), RunTitle(run), n.body, kind == KindNeed)
	if err != nil || sum == nil {
		return false
	}
	if results, err := s.deliverPayload(ctx, n.member, sum.payload, sum.topic, sum.urgency); failed(results, err) {
		slog.Warn("push: summary not delivered", "member", n.member, "error", errors.Join(err, deviceErrors(results)))
	}
	return true
}

// read is the run as it stands and what it needs, if anything, in the
// dashboard's order: its own requests, then its status. The need is keyed to the member it waits on: a run handed to
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
	if n != nil {
		n.key = string(n.member) + "|" + n.key
	}
	return run, n, nil
}

func topicOf(id string) string {
	digest := sha256.Sum256([]byte(id))
	return b64.EncodeToString(digest[:])[:32]
}

// Notify sends an assistant notice to member's devices; only an urgent one
// wakes them. A notice with no current check is news, which a later clear of
// the run's need leaves shown; one with a check is a need, which it replaces.
func (s *Service) Notify(ctx context.Context, member domain.MemberID, title, body string, run domain.RunID, urgent bool, flush <-chan struct{}, current func(context.Context) bool) error {
	for flushed := false; !flushed; {
		wait := s.freeAt(member).Sub(s.now())
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
	payload, err := json.Marshal(notification{Title: clip(title, fallbackTitleRunes), Body: clip(body, maxBodyRunes), Run: string(run), Notice: current == nil})
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
		errs = append(errs, s.send(ctx, sub, payload, topicOf(string(run)), urgency))
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
	s.active[member] = s.now()
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
