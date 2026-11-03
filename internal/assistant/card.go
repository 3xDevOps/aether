package assistant

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
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
	// Grant is the connector grant whose connector started the turn.
	Grant string `json:"grant,omitempty"`
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
// client sent under grant.
func ConnectorOrigin(client, connectorID, grant string) Origin {
	return Origin{AssistantOrigin: protocol.AssistantOrigin{Door: protocol.AssistantDoorConnector, Client: client, ConnectorID: connectorID}, Grant: grant}
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

// Card is one of a member's approval cards and the origin of the turn that
// raised it.
type Card struct {
	protocol.AssistantConfirmation
	Origin Origin
}

// confirmations holds the calls waiting for one member's cards. changed
// runs after a card changes and must not block.
type confirmations struct {
	now     func() time.Time
	ttl     time.Duration
	parkTTL time.Duration
	changed func()

	mu      sync.Mutex
	waiting map[string]chan error
}

func newConfirmations(changed func()) *confirmations {
	return &confirmations{now: time.Now, ttl: confirmTTL, parkTTL: parkTTL, changed: changed, waiting: make(map[string]chan error)}
}

func (r *confirmations) wait(id string) chan error {
	ch := make(chan error, 1)
	r.mu.Lock()
	r.waiting[id] = ch
	r.mu.Unlock()
	return ch
}

// answer hands the decision on card id to the call waiting for it, and
// reports whether one was.
func (r *confirmations) answer(id string, err error) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	ch, ok := r.waiting[id]
	if ok {
		delete(r.waiting, id)
		ch <- err
	}
	return ok
}

// leave stops waiting for card id. answered is set when the decision came
// first, which leave then returns.
func (r *confirmations) leave(id string, ch chan error) (answered bool, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.waiting[id] == ch {
		delete(r.waiting, id)
		return false, nil
	}
	return true, <-ch
}

// raise stores c as a pending card of a turn from origin, filling its id,
// times and state. Unless the card parks, the returned channel receives the
// member's decision.
func (s *Service) raise(ctx context.Context, m *member, c *protocol.AssistantConfirmation, origin Origin) (chan error, error) {
	r := m.confirms
	c.ID, c.CreatedAt = rand.Text(), r.now().UTC()
	c.ExpiresAt = c.CreatedAt.Add(r.ttl)
	if origin.parks() {
		c.ExpiresAt = c.CreatedAt.Add(r.parkTTL)
	}
	c.State, c.Origin = protocol.AssistantCardPending, origin.AssistantOrigin
	row, err := cardRow(m.id, *c, origin)
	if err != nil {
		return nil, err
	}
	var answer chan error
	if !origin.parks() {
		answer = r.wait(c.ID)
	}
	if err = s.cfg.Store.AddAssistantCard(context.WithoutCancel(ctx), row); err != nil {
		if answer != nil {
			_, _ = r.leave(c.ID, answer)
		}
		return nil, fmt.Errorf("store the card: %w", err)
	}
	s.expireAt(m, c.ID, c.ExpiresAt)
	r.changed()
	return answer, nil
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
	out.Params, out.State, out.Shown = json.RawMessage(row.Params), row.State, row.ShownAt
	out.Origin.Door = cmp.Or(out.Origin.Door, protocol.AssistantDoorDashboard)
	out.AssistantConfirmation.Origin = out.Origin.AssistantOrigin
	return out, nil
}

// openCards are the member's cards still listed: pending, approved or
// running.
func (s *Service) openCards(ctx context.Context, id domain.MemberID) ([]protocol.AssistantConfirmation, error) {
	rows, err := s.cfg.Store.AssistantCards(ctx, id, protocol.AssistantCardPending, protocol.AssistantCardApproved, protocol.AssistantCardRunning)
	if err != nil {
		return nil, fmt.Errorf("assistant: list the cards: %w", err)
	}
	out := make([]protocol.AssistantConfirmation, 0, len(rows))
	for _, row := range rows {
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
// later one, or one on a card past its expiry, gets ErrNoConfirmation.
func (s *Service) Confirm(ctx context.Context, member domain.MemberID, id string, approve bool) error {
	m := s.member(member)
	row, err := s.cfg.Store.AssistantCard(ctx, member, id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return fmt.Errorf("%w: %s", ErrNoConfirmation, id)
	case err != nil:
		return fmt.Errorf("assistant: read card %s: %w", id, err)
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
	moved, err := s.cfg.Store.MoveAssistantCard(ctx, m.id, id, []string{protocol.AssistantCardPending}, store.AssistantCardMove{State: state})
	if err != nil {
		return fmt.Errorf("assistant: decide card %s: %w", id, err)
	}
	if !moved {
		return fmt.Errorf("%w: %s", ErrNoConfirmation, id)
	}
	m.notify()
	card, err := s.Card(context.WithoutCancel(ctx), m.id, id)
	if err != nil {
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
			if _, err = s.cfg.Store.MoveAssistantCard(context.WithoutCancel(ctx), m.id, id, []string{protocol.AssistantCardApproved},
				store.AssistantCardMove{State: protocol.AssistantCardFailed, Error: cause.Error()}); err != nil {
				cause = errors.Join(cause, fmt.Errorf("record the card as failed: %w", err))
			}
			m.notify()
		}
	}
	if approved && countErr == nil {
		if !m.confirms.answer(id, nil) {
			s.spawn(func() { s.runParked(m, id, nil) })
		}
		return nil
	}
	var nerr error
	if state != protocol.AssistantCardWithdrawn {
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

// expireAt expires card id at its expiry, if it is still pending then.
func (s *Service) expireAt(m *member, id string, at time.Time) {
	time.AfterFunc(time.Until(at), func() {
		s.spawn(func() {
			if err := s.decide(s.ctx, m, id, protocol.AssistantCardExpired); err != nil && !errors.Is(err, ErrNoConfirmation) && s.ctx.Err() == nil {
				slog.Warn("assistant: expire the card", "member", m.id, "card", id, "error", err)
			}
		})
	})
}

// withdrawAll withdraws the member's undecided cards and the approved ones
// nothing has started: a reset discarded the conversation they belong to.
func (s *Service) withdrawAll(ctx context.Context, m *member) error {
	rows, err := s.cfg.Store.AssistantCards(ctx, m.id, protocol.AssistantCardPending, protocol.AssistantCardApproved)
	if err != nil {
		return fmt.Errorf("assistant: list the cards to withdraw: %w", err)
	}
	for _, row := range rows {
		if row.State == protocol.AssistantCardApproved {
			if _, err = s.cfg.Store.MoveAssistantCard(ctx, m.id, row.ID, []string{protocol.AssistantCardApproved},
				store.AssistantCardMove{State: protocol.AssistantCardWithdrawn}); err != nil {
				return fmt.Errorf("assistant: withdraw card %s: %w", row.ID, err)
			}
			m.confirms.answer(row.ID, errSessionEnded)
			continue
		}
		if err = s.decide(ctx, m, row.ID, protocol.AssistantCardWithdrawn); err != nil && !errors.Is(err, ErrNoConfirmation) {
			return err
		}
	}
	m.notify()
	return nil
}

// claim takes the run of approved card c for action a, which records it.
// Only one claim of a card succeeds, whichever path makes it.
func (s *Service) claim(ctx context.Context, m *member, c protocol.AssistantConfirmation, a protocol.AssistantAction) (bool, error) {
	ok, err := s.cfg.Store.MoveAssistantCard(ctx, m.id, c.ID, []string{protocol.AssistantCardApproved},
		store.AssistantCardMove{State: protocol.AssistantCardRunning, ActionID: a.ID})
	if err != nil {
		return false, fmt.Errorf("claim card %s: %w", c.ID, err)
	}
	m.notify()
	return ok, nil
}

// finishCard records the outcome of the call a card's action a made.
func (s *Service) finishCard(m *member, a protocol.AssistantAction) {
	state := map[string]string{protocol.AssistantActionSucceeded: protocol.AssistantCardSucceeded, protocol.AssistantActionFailed: protocol.AssistantCardFailed}[a.Outcome]
	if a.ConfirmationID == "" || state == "" {
		return
	}
	result, _ := json.Marshal(a)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := s.cfg.Store.MoveAssistantCard(ctx, m.id, a.ConfirmationID, []string{protocol.AssistantCardRunning},
		store.AssistantCardMove{State: state, Result: string(result), Error: a.Error}); err != nil {
		slog.Warn("assistant: record the card's outcome", "member", m.id, "card", a.ConfirmationID, "error", err)
	}
	m.notify()
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
	if err != nil {
		slog.Warn("assistant: run the approved card", "member", m.id, "card", id, "error", err)
		return
	}
	c, conv := card.AssistantConfirmation, m.conversation()
	a := newAction(c)
	a.ConfirmationID, a.DecidedAt = c.ID, time.Now().UTC()
	if ok, cerr := s.claim(ctx, m, c, a); cerr != nil || !ok {
		if cerr != nil {
			slog.Warn("assistant: run the approved card", "member", m.id, "card", id, "error", cerr)
		}
		return
	}
	srv := s.toolServer(m)
	fresh := protocol.AssistantConfirmation{Method: c.Method, Params: c.Params, Defaults: c.Defaults, Reason: c.Reason}
	rpcErr := srv.Summarize(ctx, &fresh)
	if rpcErr == nil && fresh.Summary != c.Summary {
		rpcErr = &protocol.Error{Code: protocol.CodeConflict, Message: fmt.Sprintf("the card changed after you approved it and now reads %q; nothing was run", fresh.Summary)}
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

// Resume picks the members' cards up after a restart: a call that was
// running is failed, never run again; an approved card nothing started
// runs once runs are recovered; a pending card expires on time.
func (s *Service) Resume(ctx context.Context) error {
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
			card, err := cardOf(row)
			if err != nil {
				return err
			}
			a := newAction(card.AssistantConfirmation)
			a.ID = cmp.Or(row.ActionID, a.ID)
			a.ConfirmationID, a.Outcome, a.Error, a.FinishedAt = row.ID, protocol.AssistantActionFailed, restartedWhileRunning, time.Now().UTC()
			if err = s.noteOutcome(m.conversation(), m, a, restartedWhileRunning); err != nil {
				return fmt.Errorf("assistant: record card %s as failed: %w", row.ID, err)
			}
		}
	}
	return nil
}

// spawn runs f in the background unless the service is closing; Close
// waits for it.
func (s *Service) spawn(f func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return
	}
	s.wg.Go(f)
}
