package push

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

type fakeCards struct {
	mu      sync.Mutex
	pending map[domain.MemberID][]Card
}

func (f *fakeCards) PendingCards(_ context.Context, member domain.MemberID) ([]Card, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pending[member], nil
}

func (f *fakeCards) CardState(_ context.Context, _ domain.MemberID, id string) (string, string, error) {
	return protocol.AssistantCardDeclined, "card " + id, nil
}

func (f *fakeCards) set(member domain.MemberID, cards ...Card) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pending[member] = cards
}

func (h *harness) cardsChanged(member *domain.Member) {
	h.t.Helper()
	if _, err := h.bus.Publish(h.ctx, events.Event{WorkspaceID: events.ServerScope, Payload: events.AssistantCardsPayload{MemberID: member.ID}}); err != nil {
		h.t.Fatal(err)
	}
}

func newCardHarness(t *testing.T) (*harness, *fakeCards) {
	t.Helper()
	cards := &fakeCards{pending: map[domain.MemberID][]Card{}}
	h := newHarness(t)
	_ = h.svc.Close()
	h.cards = cards
	h.start()
	return h, cards
}

func TestACardIsPushedWithItsIDAndWithdrawnWhenDecidedAnywhere(t *testing.T) {
	t.Parallel()
	h, cards := newCardHarness(t)
	phone := h.subscribe(h.ada)
	card := Card{ID: "c1", Summary: "Launch claude in api", CreatedAt: h.clock.now()}

	cards.set(h.ada.ID, card)
	h.cardsChanged(h.ada)
	phone.expect(t, notification{Title: cardTitle, Body: "Launch claude in api", Card: "c1"})
	if got, want := phone.topic.Load(), topicOf(cardTopic("c1")); got != want {
		t.Errorf("topic = %v, want the card's own %s", got, want)
	}
	h.cardsChanged(h.ada)
	phone.expectNone(t, testQuiet)

	cards.set(h.ada.ID)
	h.cardsChanged(h.ada)
	phone.expect(t, notification{Title: cardDecidedTitle, Body: "Declined: Launch claude in api", Clear: true})
}

func TestACardPushOffersActionsOnlyWhenItShowsTheWholeCard(t *testing.T) {
	t.Parallel()
	h, cards := newCardHarness(t)
	phone := h.subscribe(h.ada)
	long := strings.Repeat("word ", maxBodyRunes/5+1)
	cards.set(h.ada.ID, Card{ID: "c1", Summary: "Forget a saved preference", Private: true, CreatedAt: h.clock.now()})
	h.cardsChanged(h.ada)
	phone.expect(t, notification{Title: cardTitle, Body: "Forget a saved preference. Open to review."})
	h.clock.advance(burstWindow)
	cards.set(h.ada.ID, Card{ID: "c1", Summary: "Forget a saved preference", Private: true, CreatedAt: h.clock.now()},
		Card{ID: "c2", Summary: long, CreatedAt: h.clock.now()})
	h.cardsChanged(h.ada)
	phone.expect(t, notification{Title: cardTitle, Body: strings.TrimSpace(strings.Repeat("word ", (maxBodyRunes-24)/5)) + "… Open to read the rest."})
}

func TestACardWaitsWhileItsMemberUsesADashboard(t *testing.T) {
	t.Parallel()
	h, cards := newCardHarness(t)
	phone := h.subscribe(h.ada)
	h.svc.Active(h.ada.ID)
	cards.set(h.ada.ID, Card{ID: "c1", Summary: "Stop run-8x2", CreatedAt: h.clock.now()})
	h.cardsChanged(h.ada)
	phone.expectNone(t, testQuiet/2)
	phone.expect(t, notification{Title: cardTitle, Body: "Stop run-8x2", Card: "c1"})

	// One decided while it waited is never pushed.
	h.svc.Active(h.ada.ID)
	cards.set(h.ada.ID, Card{ID: "c1", Summary: "Stop run-8x2", CreatedAt: h.clock.now()}, Card{ID: "c2", Summary: "Stop run-9y3", CreatedAt: h.clock.now()})
	h.cardsChanged(h.ada)
	cards.set(h.ada.ID, Card{ID: "c1", Summary: "Stop run-8x2", CreatedAt: h.clock.now()})
	phone.expectNone(t, 2*testQuiet)
}

func TestARestartWithdrawsTheCardsDecidedWhileItWasDown(t *testing.T) {
	t.Parallel()
	h, cards := newCardHarness(t)
	phone := h.subscribe(h.ada)
	cards.set(h.ada.ID, Card{ID: "c1", Summary: "Stop run-8x2", CreatedAt: h.clock.now()})
	h.cardsChanged(h.ada)
	phone.expect(t, notification{Title: cardTitle, Body: "Stop run-8x2", Card: "c1"})

	deadline := time.Now().Add(10 * time.Second)
	for pushed, _ := h.db.PushedCards(h.ctx); len(pushed[h.ada.ID]) != 1; pushed, _ = h.db.PushedCards(h.ctx) {
		if time.Now().After(deadline) {
			t.Fatal("the pushed card was never recorded")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := h.svc.Close(); err != nil {
		t.Fatal(err)
	}
	cards.set(h.ada.ID)
	h.start()
	phone.expect(t, notification{Title: cardDecidedTitle, Body: "Declined: card c1", Clear: true})
	deadline = time.Now().Add(10 * time.Second)
	for pushed, _ := h.db.PushedCards(h.ctx); len(pushed) != 0; pushed, _ = h.db.PushedCards(h.ctx) {
		if time.Now().After(deadline) {
			t.Fatalf("pushed cards = %v, want none left", pushed)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestACardInQuietHoursWaitsForTheDigestAndLeavesItOnceDecided(t *testing.T) {
	t.Parallel()
	h, cards := newCardHarness(t)
	phone := h.subscribe(h.ada)
	now := h.clock.now().UTC()
	if err := h.db.PutNotifyPrefs(h.ctx, h.ada.ID, store.NotifyPrefs{
		Push: true, Level: protocol.NotifyBatched, DigestAt: "09:00",
		QuietStart: now.Add(-time.Hour).Format("15:04"), QuietEnd: now.Add(time.Hour).Format("15:04"), QuietTZ: "UTC",
	}); err != nil {
		t.Fatal(err)
	}
	cards.set(h.ada.ID, Card{ID: "c1", Summary: "Stop run-8x2", CreatedAt: now},
		Card{ID: "c2", Summary: "Launch codex in web", CreatedAt: now})
	h.cardsChanged(h.ada)
	h.eventuallyQueued(h.ada, 2)
	phone.expectNone(t, testQuiet)

	cards.set(h.ada.ID, Card{ID: "c2", Summary: "Launch codex in web", CreatedAt: now})
	h.clock.advance(time.Hour + time.Minute)
	if err := h.svc.sendDigest(h.ctx, h.ada.ID); err != nil {
		t.Fatal(err)
	}
	phone.expect(t, notification{Title: "Aether · 1 to approve", Body: "Overnight: Assistant asks for approval: Launch codex in web  [Approve]", Notice: true})
}

func TestDecidingACardThatASummaryTookOverUpdatesTheSummary(t *testing.T) {
	t.Parallel()
	h, cards := newCardHarness(t)
	phone := h.subscribe(h.ada)
	a, b := Card{ID: "c1", Summary: "Stop run-8x2", CreatedAt: h.clock.now()}, Card{ID: "c2", Summary: "Stop run-9y3", CreatedAt: h.clock.now()}
	cards.set(h.ada.ID, a)
	h.cardsChanged(h.ada)
	phone.expect(t, notification{Title: cardTitle, Body: "Stop run-8x2", Card: "c1"})
	cards.set(h.ada.ID, a, b)
	h.cardsChanged(h.ada)
	phone.expect(t, notification{Title: "2 things need you", Body: "Assistant asks for approval: Stop run-8x2\nAssistant asks for approval: Stop run-9y3", Quiet: true})

	cards.set(h.ada.ID, b)
	h.cardsChanged(h.ada)
	phone.expect(t, notification{Title: "1 thing needs you", Body: "Assistant asks for approval: Stop run-9y3", Quiet: true})
	phone.expectNone(t, testQuiet)
}

func TestACardFromQuietHoursHeldByTheDashboardStaysOvernight(t *testing.T) {
	t.Parallel()
	h, cards := newCardHarness(t)
	phone := h.subscribe(h.ada)
	now := h.clock.now().UTC()
	if err := h.db.PutNotifyPrefs(h.ctx, h.ada.ID, store.NotifyPrefs{
		Push: true, Level: protocol.NotifyBatched, DigestAt: "09:00",
		QuietStart: now.Add(-time.Hour).Format("15:04"), QuietEnd: now.Add(time.Hour).Format("15:04"), QuietTZ: "UTC",
	}); err != nil {
		t.Fatal(err)
	}
	h.svc.Active(h.ada.ID)
	cards.set(h.ada.ID, Card{ID: "c1", Summary: "Stop run-8x2", CreatedAt: now})
	h.cardsChanged(h.ada)
	// Quiet hours end while the dashboard still holds the card.
	h.clock.advance(time.Hour + time.Minute)
	h.eventuallyQueued(h.ada, 1)
	phone.expectNone(t, testQuiet)
	if item := h.digestItems(h.ada)[0]; !item.Overnight {
		t.Fatalf("item = %+v, want it in the digest quiet hours end with", *item)
	}
}
