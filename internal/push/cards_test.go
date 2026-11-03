package push

import (
	"context"
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
	pending map[domain.MemberID][]protocol.AssistantConfirmation
}

func (f *fakeCards) PendingCards(_ context.Context, member domain.MemberID) ([]protocol.AssistantConfirmation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pending[member], nil
}

func (f *fakeCards) set(member domain.MemberID, cards ...protocol.AssistantConfirmation) {
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
	cards := &fakeCards{pending: map[domain.MemberID][]protocol.AssistantConfirmation{}}
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
	card := protocol.AssistantConfirmation{ID: "c1", Summary: "Launch claude in api", CreatedAt: h.clock.now()}

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
	phone.expect(t, notification{Title: cardTitle, Body: clearedBody, Clear: true})
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
	cards.set(h.ada.ID, protocol.AssistantConfirmation{ID: "c1", Summary: "Stop run-8x2", CreatedAt: now},
		protocol.AssistantConfirmation{ID: "c2", Summary: "Launch codex in web", CreatedAt: now})
	h.cardsChanged(h.ada)
	h.eventuallyQueued(h.ada, 2)
	phone.expectNone(t, testQuiet)

	cards.set(h.ada.ID, protocol.AssistantConfirmation{ID: "c2", Summary: "Launch codex in web", CreatedAt: now})
	h.clock.advance(time.Hour + time.Minute)
	if err := h.svc.sendDigest(h.ctx, h.ada.ID); err != nil {
		t.Fatal(err)
	}
	phone.expect(t, notification{Title: "Aether · 1 to approve", Body: "Overnight: Assistant asks for approval: Launch codex in web  [Approve]", Notice: true})
}
