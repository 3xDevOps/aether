package push

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/store"
)

const (
	cardSource = "card"
	cardTitle  = "Assistant asks for approval"
	// cardDecided replaces a card's push once it is decided anywhere.
	cardDecided = "Decided"
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
type Cards interface {
	PendingCards(ctx context.Context, member domain.MemberID) ([]Card, error)
}

// cardPushes is, per member, the cards announced: true for one pushed to
// their devices, false for one queued for a digest. held marks a member
// whose new cards wait for them to leave the dashboard.
type cardPushes struct {
	mu    sync.Mutex
	cards map[domain.MemberID]map[string]bool
	held  map[domain.MemberID]bool
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
	s.cardPushes.mu.Lock()
	for member, ids := range pushed {
		known := s.knownCards(member)
		for _, id := range ids {
			known[id] = true
		}
	}
	s.cardPushes.mu.Unlock()
	for member := range pushed {
		s.cardsChanged(ctx, member)
	}
}

// knownCards is member's announced cards. cardPushes.mu is held.
func (s *Service) knownCards(member domain.MemberID) map[string]bool {
	known := s.cardPushes.cards[member]
	if known == nil {
		known = map[string]bool{}
		s.cardPushes.cards[member] = known
	}
	return known
}

// cardsChanged withdraws the pushes of member's cards decided, expired or
// withdrawn, and pushes their new pending cards once they have left every
// dashboard alone, as needs wait. A card raised before the service started
// was announced, if at all, by the server before this one.
func (s *Service) cardsChanged(ctx context.Context, member domain.MemberID) {
	if s.cards == nil {
		return
	}
	s.cardPushes.mu.Lock()
	defer s.cardPushes.mu.Unlock()
	pending, err := s.cards.PendingCards(ctx, member)
	if err != nil {
		slog.Warn("push: read the assistant's cards", "member", member, "error", err)
		return
	}
	known := s.knownCards(member)
	for id, pushed := range known {
		if slices.ContainsFunc(pending, func(c Card) bool { return c.ID == id }) {
			continue
		}
		s.unjoin(ctx, cardTopic(id))
		if pushed {
			results, err := s.Deliver(ctx, member, Message{Title: cardTitle, Body: cardDecided, URL: CardLink, Topic: cardTopic(id), Clear: true})
			if failed(results, err) {
				slog.Warn("push: withdraw a decided card's notification", "member", member, "card", id, "error", errors.Join(err, deviceErrors(results)))
			}
			if err := s.store.DeletePushedCard(ctx, member, id); err != nil {
				slog.Warn("push: forget a pushed card", "member", member, "card", id, "error", err)
			}
		}
		delete(known, id)
	}
	if wait := s.freeAt(member).Sub(s.now()); wait > 0 {
		if !s.cardPushes.held[member] {
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
		return
	}
	started := s.startedAt()
	for _, c := range pending {
		if _, ok := known[c.ID]; ok || c.CreatedAt.Before(started) {
			continue
		}
		pushed, err := s.announceCard(ctx, member, c)
		if err != nil {
			slog.Warn("push: announce a card", "member", member, "card", c.ID, "error", err)
		}
		known[c.ID] = pushed
		if pushed {
			// A push that went out is recorded even when the server is
			// stopping, so the next start can withdraw it.
			if err := s.store.AddPushedCard(context.WithoutCancel(ctx), member, c.ID); err != nil {
				slog.Warn("push: record a pushed card", "member", member, "card", c.ID, "error", err)
			}
		}
	}
}

// announceCard pushes card c, or queues it for the digest quiet hours
// send, and reports whether it has a notification of its own: one joined
// to a burst's summary has none to withdraw. The push offers Approve and
// Decline only when it shows the whole card; a private or long one opens
// the card instead.
func (s *Service) announceCard(ctx context.Context, member domain.MemberID, c Card) (bool, error) {
	prefs, err := s.store.GetNotifyPrefs(ctx, member)
	if err != nil {
		return false, err
	}
	if route := RouteOf(prefs, KindCard, s.now()); route != RouteNow {
		return false, s.Queue(ctx, &store.DigestItem{
			MemberID: member, Source: cardSource, Key: c.ID, Kind: KindCard, Title: cardTitle, Body: c.Summary,
			URL: CardLink, Overnight: route == RouteOvernight, CreatedAt: s.now(),
		})
	}
	m := Message{Title: cardTitle, Body: c.Summary, URL: CardLink, Topic: cardTopic(c.ID), Urgent: true}
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
