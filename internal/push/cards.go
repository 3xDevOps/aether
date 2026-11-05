package push

import (
	"context"
	"errors"
	"log/slog"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

const (
	cardSource = "card"
	cardTitle  = "Assistant asks for approval"
	// cardDecidedTitle heads the quiet replacement of a card's push once
	// it is decided anywhere.
	cardDecidedTitle = "Assistant card"
	// CardLink opens the dashboard with the assistant panel, where the
	// card and its Always options are.
	CardLink = "/?assistant=1"
)

// Card is a pending assistant card as its push shows it. Private means
// Summary only says what kind of action it is: the card's own text holds a
// saved preference.
type Card struct {
	ID        string
	Summary   string
	CreatedAt time.Time
	Private   bool
}

// Cards is what the assistant knows about a member's approval cards.
// CardState is a card's state and its summary as a push may show it.
type Cards interface {
	PendingCards(ctx context.Context, member domain.MemberID) ([]Card, error)
	CardState(ctx context.Context, member domain.MemberID, id string) (state, summary string, err error)
}

// announced is a card a push told a member of: pushed when it has a
// notification of its own, and the summary that showed.
type announced struct {
	pushed  bool
	summary string
}

var outcomes = map[string]string{
	protocol.AssistantCardApproved: "Approved", protocol.AssistantCardRunning: "Approved",
	protocol.AssistantCardSucceeded: "Approved", protocol.AssistantCardFailed: "Approved",
	protocol.AssistantCardDeclined: "Declined", protocol.AssistantCardExpired: "Expired",
}

func cardLink(id string) string { return CardLink + "&card=" + url.QueryEscape(id) }

// cardBody is what a card's push says: its summary, cut at a word to fit
// one push with a pointer to the rest, and what a private one needs.
func cardBody(c Card) string {
	if c.Private {
		return c.Summary + ". Open to review."
	}
	if utf8.RuneCountInString(c.Summary) <= maxBodyRunes {
		return c.Summary
	}
	const rest = "… Open to read the rest."
	cut := []rune(c.Summary)[:maxBodyRunes-utf8.RuneCountInString(rest)]
	if i := strings.LastIndex(string(cut), " "); i > 0 {
		return strings.TrimRight(string(cut)[:i], " ,;:") + rest
	}
	return string(cut) + rest
}

// cardPushes is, per member, the cards announced, and a lock that keeps
// that member's card pushes in order without holding up other members'.
// held marks a member whose cards are read again on a timer. mu guards
// the maps themselves.
type cardPushes struct {
	mu      sync.Mutex
	cards   map[domain.MemberID]map[string]announced
	held    map[domain.MemberID]bool
	members map[domain.MemberID]*sync.Mutex
}

// UseCards announces members' assistant cards to their devices. Call it
// before Start.
func (s *Service) UseCards(cards Cards) { s.cards = cards }

func cardTopic(id string) string { return "card:" + id }

// restoreCards takes up the cards a server before this one pushed, and
// withdraws the pushes of those decided since.
func (s *Service) restoreCards(ctx context.Context) {
	if s.cards == nil {
		return
	}
	pushed, err := s.store.PushedCards(ctx)
	if err != nil {
		slog.Warn("push: list the pushed cards", "error", err)
		return
	}
	for member, ids := range pushed {
		lock, known := s.knownCards(member)
		lock.Lock()
		for _, id := range ids {
			// A card this service announced before the restore read the
			// store keeps the summary its push showed.
			if _, ok := known[id]; !ok {
				known[id] = announced{pushed: true}
			}
		}
		lock.Unlock()
		s.cardsChanged(ctx, member)
	}
}

// knownCards is member's lock and announced cards, which only its holder
// reads or changes.
func (s *Service) knownCards(member domain.MemberID) (*sync.Mutex, map[string]announced) {
	s.cardPushes.mu.Lock()
	defer s.cardPushes.mu.Unlock()
	if s.cardPushes.members[member] == nil {
		s.cardPushes.members[member] = &sync.Mutex{}
		s.cardPushes.cards[member] = map[string]announced{}
	}
	return s.cardPushes.members[member], s.cardPushes.cards[member]
}

// cardsChanged withdraws the pushes of member's cards decided, expired or
// withdrawn, and pushes their new pending cards once they have left every
// dashboard alone, as needs wait. A card raised before the service started
// was announced, if at all, by the server before this one.
func (s *Service) cardsChanged(ctx context.Context, member domain.MemberID) {
	if s.cards == nil {
		return
	}
	lock, known := s.knownCards(member)
	lock.Lock()
	defer lock.Unlock()
	pending, err := s.cards.PendingCards(ctx, member)
	if err != nil {
		slog.Warn("push: read the assistant's cards", "member", member, "error", err)
		return
	}
	for id, a := range known {
		if slices.ContainsFunc(pending, func(c Card) bool { return c.ID == id }) {
			continue
		}
		s.unjoin(ctx, cardTopic(id), "")
		// A summary that took over the card's notification was updated
		// by unjoin; a clear would hide the rest of it.
		if a.pushed && !s.Summarizes(member, cardTopic(id)) {
			if err := s.withdrawCard(ctx, member, id, a); err != nil {
				slog.Warn("push: withdraw a decided card's notification; trying again later", "member", member, "card", id, "in", retry, "error", err)
				s.cardsAgain(ctx, member, retry)
				continue
			}
		}
		if err := s.store.DeletePushedCard(ctx, member, id); err != nil {
			slog.Warn("push: forget a pushed card", "member", member, "card", id, "error", err)
		}
		delete(known, id)
	}
	if wait := s.freeAt(member).Sub(s.now()); wait > 0 {
		s.cardsAgain(ctx, member, wait)
		return
	}
	started := s.startedAt()
	for _, c := range pending {
		if _, ok := known[c.ID]; ok || c.CreatedAt.Before(started) {
			continue
		}
		pushed, err := s.announceCard(ctx, member, c)
		if err != nil {
			slog.Warn("push: announce a card; trying again later", "member", member, "card", c.ID, "in", retry, "error", err)
			s.cardsAgain(ctx, member, retry)
			continue
		}
		known[c.ID] = announced{pushed: pushed, summary: cardBody(c)}
		if pushed {
			// A push that went out is recorded even when the server is
			// stopping, so the next start can withdraw it.
			if err := s.store.AddPushedCard(context.WithoutCancel(ctx), member, c.ID); err != nil {
				slog.Warn("push: record a pushed card", "member", member, "card", c.ID, "error", err)
			}
		}
	}
}

// cardsAgain has member's cards read again after wait, once however often
// it is asked meanwhile: they wait for the member to leave the dashboard,
// or a push or withdrawal failed.
func (s *Service) cardsAgain(ctx context.Context, member domain.MemberID, wait time.Duration) {
	s.cardPushes.mu.Lock()
	defer s.cardPushes.mu.Unlock()
	if s.cardPushes.held[member] {
		return
	}
	s.cardPushes.held[member] = true
	time.AfterFunc(wait, func() {
		s.cardPushes.mu.Lock()
		delete(s.cardPushes.held, member)
		s.cardPushes.mu.Unlock()
		if ctx.Err() == nil {
			s.cardsChanged(ctx, member)
		}
	})
}

// withdrawCard replaces the notification of card id, decided anywhere,
// with how it ended and the summary its push showed.
func (s *Service) withdrawCard(ctx context.Context, member domain.MemberID, id string, a announced) error {
	state, summary, err := s.cards.CardState(ctx, member, id)
	if err != nil {
		return err
	}
	if a.summary != "" {
		summary = a.summary
	}
	outcome := outcomes[state]
	if outcome == "" {
		outcome = "Withdrawn"
	}
	results, err := s.Deliver(ctx, member, Message{Title: cardDecidedTitle, Body: outcome + ": " + summary, URL: cardLink(id), Topic: cardTopic(id), Clear: true})
	if failed(results, err) {
		return errors.Join(err, deviceErrors(results))
	}
	return nil
}

// announceCard pushes card c, or queues it for the digest quiet hours
// send, and reports whether it has a notification of its own: one joined
// to a burst's summary has none to withdraw. The push offers Review and
// Decline only when it shows the whole summary; a private or long one
// opens the card instead. Approving is always done on the card itself.
func (s *Service) announceCard(ctx context.Context, member domain.MemberID, c Card) (bool, error) {
	prefs, err := s.store.GetNotifyPrefs(ctx, member)
	if err != nil {
		return false, err
	}
	// As for a session's need: routed as of when the card was raised and as
	// of now, the later route winning.
	now := s.now()
	route := RouteOf(prefs, KindCard, now)
	if c.CreatedAt.Before(now) {
		route = max(route, RouteOf(prefs, KindCard, c.CreatedAt))
	}
	if route != RouteNow {
		return false, s.Queue(ctx, &store.DigestItem{
			MemberID: member, Source: cardSource, Key: c.ID, Kind: KindCard, Title: cardTitle, Body: c.Summary,
			URL: cardLink(c.ID), Overnight: route == RouteOvernight, CreatedAt: s.now(),
		})
	}
	m := Message{Title: cardTitle, Body: cardBody(c), URL: cardLink(c.ID), Topic: cardTopic(c.ID), Urgent: true}
	if !c.Private && utf8.RuneCountInString(c.Summary) <= maxBodyRunes {
		m.Card = c.ID
	}
	results, joined, err := s.Push(ctx, member, m, true)
	if failed(results, err) {
		return !joined, errors.Join(err, deviceErrors(results))
	}
	return !joined, nil
}

// cardItems settles cards queued for a digest: one is worth telling while
// it still waits for a decision.
type cardItems struct{ s *Service }

func (c cardItems) Stale(ctx context.Context, item *store.DigestItem) (string, error) {
	if c.s.cards == nil {
		return "", nil
	}
	pending, err := c.s.cards.PendingCards(ctx, item.MemberID)
	if err != nil {
		return "", err
	}
	if slices.ContainsFunc(pending, func(card Card) bool { return card.ID == item.Key }) {
		return "", nil
	}
	return "Decided before the digest was sent", nil
}

func (cardItems) Sent(context.Context, *store.DigestItem, []DeviceResult, error, bool) error {
	return nil
}
func (cardItems) Dropped(context.Context, *store.DigestItem, string) error { return nil }
