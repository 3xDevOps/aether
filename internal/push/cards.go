package push

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"sync"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

const (
	cardSource = "card"
	cardTitle  = "Assistant asks for approval"
	// CardLink opens the dashboard with the assistant panel, where the
	// card and its Always options are.
	CardLink = "/?assistant=1"
)

// Cards is what the assistant knows about a member's approval cards.
type Cards interface {
	PendingCards(ctx context.Context, member domain.MemberID) ([]protocol.AssistantConfirmation, error)
}

// cardPushes is, per member, the cards announced since the service started:
// true for one pushed to their devices, false for one queued for a digest.
type cardPushes struct {
	mu    sync.Mutex
	cards map[domain.MemberID]map[string]bool
}

// UseCards announces members' assistant cards to their devices. Call it
// before Start.
func (s *Service) UseCards(cards Cards) { s.cards = cards }

func cardTopic(id string) string { return "card:" + id }

// cardsChanged pushes member's new pending cards and withdraws the pushes
// of those decided, expired or withdrawn since. A card raised before the
// service started was announced, if at all, by the server before this one.
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
	known := s.cardPushes.cards[member]
	if known == nil {
		known = map[string]bool{}
		s.cardPushes.cards[member] = known
	}
	for id, pushed := range known {
		if slices.ContainsFunc(pending, func(c protocol.AssistantConfirmation) bool { return c.ID == id }) {
			continue
		}
		s.unjoin(ctx, cardTopic(id))
		if pushed {
			results, err := s.Deliver(ctx, member, Message{Title: cardTitle, URL: CardLink, Topic: cardTopic(id), Clear: true})
			if failed(results, err) {
				slog.Warn("push: withdraw a decided card's notification", "member", member, "card", id, "error", errors.Join(err, deviceErrors(results)))
			}
		}
		delete(known, id)
	}
	started := s.startedAt()
	for _, c := range pending {
		if _, ok := known[c.ID]; ok || c.CreatedAt.Before(started) {
			continue
		}
		pushed, err := s.announceCard(ctx, member, c)
		if err != nil {
			slog.Warn("push: announce a card", "member", member, "card", c.ID, "error", err)
			continue
		}
		known[c.ID] = pushed
	}
}

// announceCard pushes card c with Approve and Decline, or queues it for
// the digest quiet hours send, and reports whether it has a notification
// of its own: one joined to a burst's summary has none to withdraw.
func (s *Service) announceCard(ctx context.Context, member domain.MemberID, c protocol.AssistantConfirmation) (bool, error) {
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
	results, joined, err := s.Push(ctx, member, Message{Title: cardTitle, Body: c.Summary, URL: CardLink, Topic: cardTopic(c.ID), Urgent: true, Card: c.ID}, true)
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
	if slices.ContainsFunc(pending, func(card protocol.AssistantConfirmation) bool { return card.ID == item.Key }) {
		return "", nil
	}
	return "Decided before the digest was sent", nil
}

func (cardItems) Sent(context.Context, *store.DigestItem, []DeviceResult, error, bool) error {
	return nil
}
func (cardItems) Dropped(context.Context, *store.DigestItem, string) error { return nil }
