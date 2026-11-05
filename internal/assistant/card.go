package assistant

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

const (
	// confirmTTL is how long a card raised in a turn from an Aether door
	// stays open; its call waits for it that long.
	confirmTTL = 10 * time.Minute
	// parkTTL is how long a card raised in a connector turn stays open; its
	// call does not wait.
	parkTTL = 24 * time.Hour

	restartedWhileRunning = "the server restarted while it ran; check before retrying"
)

var (
	// ErrNoConfirmation means no pending confirmation has the id: it was
	// answered, expired or withdrawn by a reset.
	ErrNoConfirmation = errors.New("assistant: no such pending confirmation")

	errDeclined     = errors.New("the member declined this action")
	errSuperseded   = errors.New("the same call was asked again; its card answers the newer call")
	errTurnEnded    = errors.New("the turn ended before the member answered; the card stays open, and approving it runs the call on the server")
	errExpired      = errors.New("the member did not answer before the card expired")
	errSessionEnded = errors.New("a reset discarded the conversation before the member answered")
)

// parkedText is what a parked call returns to the model, with the card's id.
const parkedText = "Waiting for approval as card %s; the server runs it when the member approves and writes the outcome into this conversation. Do not call it again."

// Origin is where a turn's prompt came from, stamped by the server from the
// transport that carried it. Door makes an Aether client's origin and
// ConnectorOrigin a connector's; no RPC param reaches ConnectorOrigin.
type Origin struct {
	protocol.AssistantOrigin
	// Grant is the connector grant whose connector started the turn, and
	// Scopes the scopes it holds.
	Grant  string   `json:"grant,omitempty"`
	Scopes []string `json:"scopes,omitempty"`
}

// Door is the origin of a prompt from an Aether client that names itself
// door, for display; an unknown name is the dashboard.
func Door(door string) Origin {
	if !slices.Contains([]string{protocol.AssistantDoorPhone, protocol.AssistantDoorDesktop}, door) {
		door = protocol.AssistantDoorDashboard
	}
	return Origin{AssistantOrigin: protocol.AssistantOrigin{Door: door}}
}

// ConnectorOrigin is the origin of a prompt that connector connectorID of
// client sent under grant, which holds scopes.
func ConnectorOrigin(client, connectorID, grant string, scopes []string) Origin {
	return Origin{AssistantOrigin: protocol.AssistantOrigin{Door: protocol.AssistantDoorConnector, Client: client, ConnectorID: connectorID},
		Grant: grant, Scopes: slices.Clone(scopes)}
}

func (o Origin) String() string {
	if o.Door == protocol.AssistantDoorConnector {
		return "connector:" + o.Client + " " + o.ConnectorID
	}
	return o.Door
}

// parks reports whether a card raised in a turn from o returns at once:
// nobody watches a connector turn in the panel.
func (o Origin) parks() bool { return o.Door == protocol.AssistantDoorConnector }

// Covers reports whether a turn from o may pass method without a card:
// any Aether door, or a connector whose grant holds one of the scopes
// method needs, which scopes names (no scope for a method no connector may
// call).
func (o Origin) Covers(method string, scopes func(method string) []string) bool {
	if o.Door != protocol.AssistantDoorConnector {
		return true
	}
	return scopes != nil && slices.ContainsFunc(scopes(method), func(s string) bool { return slices.Contains(o.Scopes, s) })
}

// Card is one of a member's approval cards and the origin of the turn that
// raised it.
type Card struct {
	protocol.AssistantConfirmation
	Origin Origin
	// Generation is the conversation the card belongs to.
	Generation int64
}

// FullText is the card as a connector shows it to be decided by echo: the
// summary, then every field the call sends, whole, one per line in name
// order, and a template launch's task as its params render it.
func (c Card) FullText() string {
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(c.Params, &fields)
	var b strings.Builder
	b.WriteString(c.Summary)
	for _, name := range slices.Sorted(maps.Keys(fields)) {
		value := string(fields[name])
		var text string
		if json.Unmarshal(fields[name], &text) == nil {
			value = text
		}
		fmt.Fprintf(&b, "\n%s: %s", name, value)
	}
	if c.Task != "" {
		fmt.Fprintf(&b, "\nrendered task: %s", c.Task)
	}
	return b.String()
}

// confirmations holds the calls waiting for one member's cards. changed
// runs after a card changes and must not block.
type confirmations struct {
	now     func() time.Time
	ttl     time.Duration
	parkTTL time.Duration
	changed func()

	mu      sync.Mutex
	waiting map[string]waiter
}

// waiter is a call waiting for a card, made in the session's turn.
type waiter struct {
	ch   chan error
	turn int64
}

func newConfirmations(changed func()) *confirmations {
	return &confirmations{now: time.Now, ttl: confirmTTL, parkTTL: parkTTL, changed: changed, waiting: make(map[string]waiter)}
}

// wait registers a call made in turn waiting for card id; one already
// waiting for it stops with errSuperseded.
func (r *confirmations) wait(id string, turn int64) chan error {
	ch := make(chan error, 1)
	r.mu.Lock()
	defer r.mu.Unlock()
	if old, ok := r.waiting[id]; ok {
		old.ch <- errSuperseded
	}
	r.waiting[id] = waiter{ch, turn}
	return ch
}

// answer hands the decision on card id to the call waiting for it, and
// reports whether one was.
func (r *confirmations) answer(id string, err error) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	w, ok := r.waiting[id]
	if ok {
		delete(r.waiting, id)
		w.ch <- err
	}
	return ok
}

// release ends with errTurnEnded the waits of calls made in turn upTo or
// before, leaving the cards pending: those turns are over. A later turn's
// waits stay.
func (r *confirmations) release(upTo int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, w := range r.waiting {
		if w.turn <= upTo {
			delete(r.waiting, id)
			w.ch <- errTurnEnded
		}
	}
}

// turnNow is the number of the live session's current turn, 0 with none.
func (m *member) turnNow() int64 {
	if live := m.live(); live != nil {
		return live.acp.Turn()
	}
	return 0
}

// leave stops waiting for card id. answered is set when the decision came
// first, which leave then returns.
func (r *confirmations) leave(id string, ch chan error) (answered bool, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.waiting[id].ch == ch {
		delete(r.waiting, id)
		return false, nil
	}
	return true, <-ch
}

// raise stores c as a pending card of a turn from origin in the member's
// conversation, filling its id, times and state, and returns that
// conversation's generation. A pending card of the conversation with the
// same call is c's card instead, so approving the call runs it once. Unless
// the card parks, the returned channel receives the member's decision.
func (s *Service) raise(ctx context.Context, m *member, c *protocol.AssistantConfirmation, origin Origin) (chan error, int64, error) {
	r := m.confirms
	// Two equal calls at once must not both find no card and make two.
	m.raising.Lock()
	defer m.raising.Unlock()
	conv, err := s.stored(ctx, m.id)
	if err != nil {
		return nil, 0, err
	}
	open, err := s.cfg.Store.AssistantCards(ctx, m.id, protocol.AssistantCardPending)
	if err != nil {
		return nil, 0, fmt.Errorf("list the open cards: %w", err)
	}
	for _, row := range open {
		held, decodeErr := cardOf(row)
		// A card that would read differently now, acts on a newer version
		// of its row, or another connector's turn raised, is not this
		// call's.
		if decodeErr != nil || row.Generation != conv.Generation || row.Method != c.Method || row.Params != string(c.Params) ||
			held.Summary != c.Summary || !row.ShownAt.Equal(c.Shown) || held.Origin.Door != origin.Door || held.Origin.Grant != origin.Grant {
			continue
		}
		c.ID, c.CreatedAt, c.ExpiresAt, c.State = row.ID, row.CreatedAt, row.ExpiresAt, row.State
		if origin.parks() {
			return nil, conv.Generation, nil
		}
		answer := r.wait(c.ID, m.turnNow())
		// A decision made before the wait was registered went elsewhere.
		if now, readErr := s.cfg.Store.AssistantCard(ctx, m.id, c.ID); readErr != nil || now.State != protocol.AssistantCardPending {
			if answered, decision := r.leave(c.ID, answer); answered {
				answer <- decision
				return answer, conv.Generation, nil
			}
			if readErr != nil {
				return nil, 0, fmt.Errorf("read card %s: %w", c.ID, readErr)
			}
			return nil, 0, fmt.Errorf("card %s was decided while this call asked again: it is %s; the server records the outcome in the conversation", c.ID, now.State)
		}
		return answer, conv.Generation, nil
	}
	c.ID, c.CreatedAt = rand.Text(), r.now().UTC()
	c.ExpiresAt = c.CreatedAt.Add(r.ttl)
	if origin.parks() {
		c.ExpiresAt = c.CreatedAt.Add(r.parkTTL)
	}
	c.State, c.Origin = protocol.AssistantCardPending, origin.AssistantOrigin
	row, err := cardRow(m.id, *c, origin)
	if err != nil {
		return nil, 0, err
	}
	row.Generation = conv.Generation
	var answer chan error
	if !origin.parks() {
		answer = r.wait(c.ID, m.turnNow())
	}
	if err = s.cfg.Store.AddAssistantCard(context.WithoutCancel(ctx), row); err != nil {
		if answer != nil {
			_, _ = r.leave(c.ID, answer)
		}
		return nil, 0, fmt.Errorf("store the card: %w", err)
	}
	s.expireAt(m, c.ID, c.ExpiresAt)
	r.changed()
	return answer, conv.Generation, nil
}

func cardRow(member domain.MemberID, c protocol.AssistantConfirmation, origin Origin) (*store.AssistantCard, error) {
	card, err := json.Marshal(c)
	if err != nil {
		return nil, fmt.Errorf("encode the card: %w", err)
	}
	o, err := json.Marshal(origin)
	if err != nil {
		return nil, fmt.Errorf("encode the card's origin: %w", err)
	}
	return &store.AssistantCard{ID: c.ID, MemberID: member, Method: c.Method, Params: string(c.Params), Card: string(card), Origin: string(o),
		State: c.State, ShownAt: c.Shown, CreatedAt: c.CreatedAt, ExpiresAt: c.ExpiresAt}, nil
}

func cardOf(row store.AssistantCard) (Card, error) {
	var out Card
	if err := json.Unmarshal([]byte(row.Card), &out.AssistantConfirmation); err != nil {
		return Card{}, fmt.Errorf("assistant: decode card %s: %w", row.ID, err)
	}
	if err := json.Unmarshal([]byte(row.Origin), &out.Origin); err != nil {
		return Card{}, fmt.Errorf("assistant: decode the origin of card %s: %w", row.ID, err)
	}
	out.Params, out.State, out.Shown, out.Generation = json.RawMessage(row.Params), row.State, row.ShownAt, row.Generation
	out.DecidedAt = row.DecidedAt
	out.Origin.Door = cmp.Or(out.Origin.Door, protocol.AssistantDoorDashboard)
	out.AssistantConfirmation.Origin = out.Origin.AssistantOrigin
	return out, nil
}

// openCards are the cards of the member's conversation still listed:
// pending, approved or running.
func (s *Service) openCards(ctx context.Context, id domain.MemberID) ([]protocol.AssistantConfirmation, error) {
	conv, err := s.stored(ctx, id)
	if err != nil {
		return nil, err
	}
	rows, err := s.cfg.Store.AssistantCards(ctx, id, protocol.AssistantCardPending, protocol.AssistantCardApproved, protocol.AssistantCardRunning)
	if err != nil {
		return nil, fmt.Errorf("assistant: list the cards: %w", err)
	}
	out := make([]protocol.AssistantConfirmation, 0, len(rows))
	for _, row := range rows {
		if row.Generation != conv.Generation {
			continue
		}
		c, err := cardOf(row)
		if err != nil {
			return nil, err
		}
		out = append(out, c.AssistantConfirmation)
	}
	return out, nil
}

// Card returns one of the member's cards, in any state.
func (s *Service) Card(ctx context.Context, member domain.MemberID, id string) (Card, error) {
	row, err := s.cfg.Store.AssistantCard(ctx, member, id)
	if errors.Is(err, store.ErrNotFound) {
		return Card{}, fmt.Errorf("%w: %s", ErrNoConfirmation, id)
	}
	if err != nil {
		return Card{}, fmt.Errorf("assistant: read card %s: %w", id, err)
	}
	return cardOf(*row)
}

// Confirm decides the member's pending card id. The first decision wins; a
// later one, or one on a card past its expiry or of an earlier
// conversation, gets ErrNoConfirmation.
func (s *Service) Confirm(ctx context.Context, member domain.MemberID, id string, approve bool) error {
	m := s.member(member)
	row, err := s.cfg.Store.AssistantCard(ctx, member, id)
	if errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("%w: %s", ErrNoConfirmation, id)
	}
	if err != nil {
		return fmt.Errorf("assistant: read card %s: %w", id, err)
	}
	current, err := s.current(ctx, m, row.Generation)
	switch {
	case err != nil:
		return err
	case !current:
		if err = s.withdraw(ctx, m, id); err != nil {
			return err
		}
		return fmt.Errorf("%w: %s", ErrNoConfirmation, id)
	case row.State == protocol.AssistantCardPending && !m.confirms.now().Before(row.ExpiresAt):
		if err = s.decide(ctx, m, id, protocol.AssistantCardExpired); err != nil && !errors.Is(err, ErrNoConfirmation) {
			return err
		}
		return fmt.Errorf("%w: %s", ErrNoConfirmation, id)
	}
	state := protocol.AssistantCardDeclined
	if approve {
		state = protocol.AssistantCardApproved
	}
	return s.decide(ctx, m, id, state)
}

// decide moves card id from pending to state, then hands an approval to
// the call waiting for the card, or else runs the call itself, and records
// any other decision in the conversation.
func (s *Service) decide(ctx context.Context, m *member, id, state string) error {
	moved, err := s.cfg.Store.MoveAssistantCard(ctx, m.id, id, []string{protocol.AssistantCardPending}, store.AssistantCardMove{State: state, Decided: true})
	if err != nil {
		return fmt.Errorf("assistant: decide card %s: %w", id, err)
	}
	if !moved {
		return fmt.Errorf("%w: %s", ErrNoConfirmation, id)
	}
	m.notify()
	card, err := s.Card(context.WithoutCancel(ctx), m.id, id)
	if err != nil {
		// A card that cannot be read cannot run or be recorded.
		if state == protocol.AssistantCardApproved {
			err = errors.Join(err, s.fail(m, id, protocol.AssistantCardApproved, err.Error()))
		}
		m.confirms.answer(id, err)
		return err
	}
	c := card.AssistantConfirmation
	approved := state == protocol.AssistantCardApproved
	cause := map[string]error{protocol.AssistantCardDeclined: errDeclined, protocol.AssistantCardExpired: errExpired,
		protocol.AssistantCardWithdrawn: errSessionEnded}[state]
	countErr := s.countApproval(context.WithoutCancel(ctx), m, c, approved)
	if countErr != nil {
		countErr = fmt.Errorf("count the approval: %w", countErr)
		cause = errors.Join(cause, countErr)
		if approved {
			cause = errors.Join(cause, s.fail(m, id, protocol.AssistantCardApproved, cause.Error()))
		}
	}
	if approved && countErr == nil {
		if !m.confirms.answer(id, nil) {
			s.spawn(func() { s.runParked(m, id, nil) })
		}
		return nil
	}
	var nerr error
	if current, cerr := s.current(context.WithoutCancel(ctx), m, card.Generation); cerr != nil {
		nerr = cerr
	} else if state != protocol.AssistantCardWithdrawn && current {
		a := newAction(c)
		a.ConfirmationID, a.DecidedAt = c.ID, time.Now().UTC()
		a.Outcome, a.FinishedAt = refusal(cause), a.DecidedAt
		if a.Outcome == protocol.AssistantActionFailed {
			a.Error = cause.Error()
		}
		nerr = s.noteOutcome(m.conversation(), m, a, cause.Error())
	}
	m.confirms.answer(id, cause)
	if nerr != nil {
		nerr = fmt.Errorf("recording it failed: %w", nerr)
	}
	if err = errors.Join(countErr, nerr); err != nil {
		return fmt.Errorf("assistant: card %s: %w", id, err)
	}
	return nil
}

// expireAt expires card id at its expiry, if it is still pending then, and
// tries again a minute later when the store refuses.
func (s *Service) expireAt(m *member, id string, at time.Time) {
	time.AfterFunc(time.Until(at), func() {
		s.spawn(func() {
			err := s.decide(s.ctx, m, id, protocol.AssistantCardExpired)
			if err == nil || errors.Is(err, ErrNoConfirmation) || s.ctx.Err() != nil {
				return
			}
			slog.Warn("assistant: expire the card; trying again in a minute", "member", m.id, "card", id, "error", err)
			s.expireAt(m, id, time.Now().Add(time.Minute))
		})
	})
}

// current reports whether generation is the member's conversation.
func (s *Service) current(ctx context.Context, m *member, generation int64) (bool, error) {
	conv, err := s.stored(ctx, m.id)
	if err != nil {
		return false, err
	}
	return conv.Generation == generation, nil
}

// withdraw withdraws card id unless something started it, and ends the
// wait of its call.
func (s *Service) withdraw(ctx context.Context, m *member, id string) error {
	if _, err := s.cfg.Store.MoveAssistantCard(ctx, m.id, id, []string{protocol.AssistantCardPending, protocol.AssistantCardApproved},
		store.AssistantCardMove{State: protocol.AssistantCardWithdrawn}); err != nil {
		return fmt.Errorf("assistant: withdraw card %s: %w", id, err)
	}
	m.confirms.answer(id, errSessionEnded)
	m.notify()
	return nil
}

// withdrawAll withdraws the member's cards nothing has started: a reset
// discarded the conversation they belong to. A card decided or raised while
// the reset runs belongs to the old generation, which claim and Confirm
// refuse.
func (s *Service) withdrawAll(ctx context.Context, m *member) error {
	rows, err := s.cfg.Store.AssistantCards(ctx, m.id, protocol.AssistantCardPending, protocol.AssistantCardApproved)
	if err != nil {
		return fmt.Errorf("assistant: list the cards to withdraw: %w", err)
	}
	for _, row := range rows {
		if err = s.withdraw(ctx, m, row.ID); err != nil {
			return err
		}
	}
	return nil
}

// claim takes the run of approved card c for action a, which records it.
// Only one claim of a card succeeds, whichever path makes it, and none of a
// card of an earlier conversation, which it withdraws.
func (s *Service) claim(ctx context.Context, m *member, c Card, a protocol.AssistantAction) (bool, error) {
	current, err := s.current(ctx, m, c.Generation)
	if err != nil {
		return false, fmt.Errorf("claim card %s: %w", c.ID, err)
	}
	if !current {
		return false, s.withdraw(ctx, m, c.ID)
	}
	ok, err := s.cfg.Store.MoveAssistantCard(ctx, m.id, c.ID, []string{protocol.AssistantCardApproved},
		store.AssistantCardMove{State: protocol.AssistantCardRunning, ActionID: a.ID})
	if err != nil {
		return false, fmt.Errorf("claim card %s: %w", c.ID, err)
	}
	m.notify()
	return ok, nil
}

// fail records card id, in state from, as failed with why.
func (s *Service) fail(m *member, id, from, why string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := s.cfg.Store.MoveAssistantCard(ctx, m.id, id, []string{from},
		store.AssistantCardMove{State: protocol.AssistantCardFailed, Error: why}); err != nil {
		return fmt.Errorf("record card %s as failed: %w", id, err)
	}
	m.notify()
	return nil
}

// finishCard records the outcome of the call a card's action a made.
func (s *Service) finishCard(m *member, a protocol.AssistantAction) error {
	state := map[string]string{protocol.AssistantActionSucceeded: protocol.AssistantCardSucceeded, protocol.AssistantActionFailed: protocol.AssistantCardFailed}[a.Outcome]
	if a.ConfirmationID == "" || state == "" {
		return nil
	}
	result, err := json.Marshal(a)
	if err != nil {
		return fmt.Errorf("encode the outcome of card %s: %w", a.ConfirmationID, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err = s.cfg.Store.MoveAssistantCard(ctx, m.id, a.ConfirmationID, []string{protocol.AssistantCardRunning},
		store.AssistantCardMove{State: state, Result: string(result), Error: a.Error}); err != nil {
		return fmt.Errorf("record the outcome on card %s: %w", a.ConfirmationID, err)
	}
	m.notify()
	return nil
}

// runParked runs approved card id, whose call no longer waits for it,
// through the member's own caller with its stored params. It first builds
// the card again from those params and fails, running nothing, when the
// card would read differently now. ready, when set, is waited for first.
func (s *Service) runParked(m *member, id string, ready <-chan struct{}) {
	if ready != nil {
		select {
		case <-ready:
		case <-s.ctx.Done():
			return
		}
	}
	ctx := s.ctx
	card, err := s.Card(ctx, m.id, id)
	if err != nil && s.ctx.Err() != nil {
		// Closing: the next start runs the approved card.
		return
	}
	if err != nil {
		// An approved card nothing can read would read Approved forever.
		slog.Warn("assistant: run the approved card", "member", m.id, "card", id, "error", errors.Join(err, s.fail(m, id, protocol.AssistantCardApproved, err.Error())))
		return
	}
	c, conv := card.AssistantConfirmation, m.conversation()
	a := newAction(c)
	a.ConfirmationID, a.DecidedAt = c.ID, time.Now().UTC()
	if ok, cerr := s.claim(ctx, m, card, a); cerr != nil || !ok {
		if cerr != nil {
			slog.Warn("assistant: run the approved card", "member", m.id, "card", id, "error", errors.Join(cerr, s.fail(m, id, protocol.AssistantCardApproved, cerr.Error())))
		}
		return
	}
	srv := s.toolServer(m)
	fresh := protocol.AssistantConfirmation{Method: c.Method, Params: c.Params, Defaults: c.Defaults, Reason: c.Reason}
	rpcErr := srv.Summarize(ctx, &fresh)
	if rpcErr == nil && fresh.Summary != c.Summary {
		rpcErr = &protocol.Error{Code: protocol.CodeConflict, Message: fmt.Sprintf("The card changed after you approved it, so nothing was run.\nYou approved: %s\nIt now reads: %s", c.Summary, fresh.Summary)}
	}
	if rpcErr == nil {
		a.Outcome, a.Approval = protocol.AssistantActionApproved, "card"
		if err = s.noteAction(ctx, conv, m, a, c.Method+" runs now: you approved its card after the call that asked stopped waiting."); err != nil {
			rpcErr = &protocol.Error{Code: protocol.CodeInternal, Message: "record the approval: " + err.Error()}
		}
	}
	if rpcErr != nil {
		a.Outcome, a.Approval, a.Error, a.FinishedAt = protocol.AssistantActionFailed, "", rpcErr.Message, time.Now().UTC()
		if nerr := s.noteOutcome(conv, m, a, rpcErr.Message); nerr != nil {
			slog.Warn("assistant: record the approved card's failure", "member", m.id, "card", id, "error", nerr)
		}
		return
	}
	m.actions.hold(conv, a, c.Params, c.Shown)
	_, _ = srv.Call(ctx, c.Method, c.Params)
}

// Resume picks the members' queues and cards up after a restart: a turn or
// a card's call that was running is marked interrupted or failed, never run
// again; waiting prompts and approved cards nothing started run once runs
// are recovered; a pending card expires on time.
func (s *Service) Resume(ctx context.Context) error {
	if err := s.resumeQueue(ctx); err != nil {
		return err
	}
	rows, err := s.cfg.Store.AssistantCards(ctx, "", protocol.AssistantCardPending, protocol.AssistantCardApproved, protocol.AssistantCardRunning)
	if err != nil {
		return fmt.Errorf("assistant: list the open cards: %w", err)
	}
	for _, row := range rows {
		m := s.member(row.MemberID)
		switch row.State {
		case protocol.AssistantCardPending:
			s.expireAt(m, row.ID, row.ExpiresAt)
		case protocol.AssistantCardApproved:
			s.spawn(func() { s.runParked(m, row.ID, s.cfg.Runs.RecoveryReady()) })
		case protocol.AssistantCardRunning:
			// One card that cannot be recorded must not stop the server.
			card, err := cardOf(row)
			if err != nil {
				slog.Warn("assistant: fail the card a restart cut off", "member", row.MemberID, "card", row.ID,
					"error", errors.Join(err, s.fail(m, row.ID, protocol.AssistantCardRunning, restartedWhileRunning)))
				continue
			}
			current, cerr := s.current(ctx, m, row.Generation)
			if cerr != nil {
				// Left running, the next start tries again; one member's store
				// read must not stop the server.
				slog.Warn("assistant: read the conversation of a card a restart cut off; the next start retries", "member", row.MemberID, "card", row.ID, "error", cerr)
				continue
			}
			if !current {
				// A card of a discarded conversation fails without a record in
				// the new one.
				if ferr := s.fail(m, row.ID, protocol.AssistantCardRunning, restartedWhileRunning); ferr != nil {
					slog.Warn("assistant: fail the card a restart cut off", "member", row.MemberID, "card", row.ID, "error", ferr)
				}
				continue
			}
			a := newAction(card.AssistantConfirmation)
			a.ID = cmp.Or(row.ActionID, a.ID)
			a.ConfirmationID, a.Outcome, a.Error, a.FinishedAt = row.ID, protocol.AssistantActionFailed, restartedWhileRunning, time.Now().UTC()
			if err = s.noteOutcome(m.conversation(), m, a, restartedWhileRunning); err != nil {
				slog.Warn("assistant: fail the card a restart cut off", "member", row.MemberID, "card", row.ID, "error", err)
			}
		}
	}
	return nil
}

// spawn runs f in the background unless the service is closing, and
// reports whether it does; Close waits for it.
func (s *Service) spawn(f func()) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return false
	}
	s.wg.Go(f)
	return true
}
