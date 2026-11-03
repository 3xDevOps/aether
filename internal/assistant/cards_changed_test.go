package assistant

import (
	"errors"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// A notification's Approve calls assistant.confirm as the member on their
// own device, with the card's id alone: it decides the card once, and the
// same id replayed, or sent by another member, decides nothing.
func TestAPushActionDecidesItsCardOnceAndOnlyForItsMember(t *testing.T) {
	w := newCardWorld(t)
	bob := &domain.Member{DisplayName: "Bob", TailnetLogin: "bob@example.com", Color: "#4363d8", Role: domain.RoleCollaborator}
	if err := w.db.CreateMember(t.Context(), bob); err != nil {
		t.Fatal(err)
	}
	c := w.leaveCard()
	pending, err := w.svc.PendingCards(t.Context(), w.member)
	if err != nil || len(pending) != 1 || pending[0].ID != c.ID || pending[0].Summary != c.Summary {
		t.Fatalf("pending cards = %+v, %v, want the card the push announces", pending, err)
	}

	if err = w.svc.Confirm(t.Context(), bob.ID, c.ID, true); !errors.Is(err, ErrNoConfirmation) {
		t.Fatalf("another member's approval = %v, want ErrNoConfirmation", err)
	}
	if ids := pendingIDs(w); len(ids) != 1 {
		t.Fatalf("pending after a foreign approval = %v, want the card untouched", ids)
	}
	if err = w.svc.Confirm(t.Context(), w.member, c.ID, true); err != nil {
		t.Fatalf("the member's approval = %v", err)
	}
	w.settled(c.ID)
	if err = w.svc.Confirm(t.Context(), w.member, c.ID, true); !errors.Is(err, ErrNoConfirmation) {
		t.Fatalf("a replayed approval = %v, want ErrNoConfirmation", err)
	}
	if n := w.caller.kills(); n != 1 {
		t.Fatalf("run.kill ran %d times, want once", n)
	}
	if pending, err = w.svc.PendingCards(t.Context(), w.member); err != nil || len(pending) != 0 {
		t.Fatalf("pending cards after the decision = %+v, %v, want none, so the push is withdrawn", pending, err)
	}
}

func TestRaisingAndDecidingACardAnnounceIt(t *testing.T) {
	bus, err := events.NewInProc(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bus.Close() })
	sub, err := bus.Subscribe(t.Context(), events.SubscribeOptions{Filter: events.Filter{Types: []events.Type{events.TypeAssistantCards}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sub.Close() })
	w := newCardWorld(t)
	w.svc.cfg.Bus = bus
	c := w.leaveCard()
	if err := w.svc.Confirm(t.Context(), w.member, c.ID, false); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		select {
		case e := <-sub.Events():
			if p, ok := e.Payload.(events.AssistantCardsPayload); !ok || p.MemberID != w.member {
				t.Fatalf("event = %+v, want the member's cards changed", e)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the card's change was not announced")
		}
	}
}

func TestAPreferenceCardsPushSaysOnlyWhatItDoes(t *testing.T) {
	w := newCardWorld(t)
	c := protocol.AssistantConfirmation{
		ID: "forget-1", Method: protocol.MethodAssistantForget, Summary: `Forget "Always deploy to eu-west-1"`,
		Params: []byte(`{}`), CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().Add(time.Hour).UTC(), State: protocol.AssistantCardPending,
	}
	row, err := cardRow(w.member, c, Door(protocol.AssistantDoorDashboard))
	if err != nil {
		t.Fatal(err)
	}
	if err = w.db.AddAssistantCard(t.Context(), row); err != nil {
		t.Fatal(err)
	}
	pending, err := w.svc.PendingCards(t.Context(), w.member)
	if err != nil || len(pending) != 1 || !pending[0].Private || pending[0].Summary != "Forget a saved preference" {
		t.Fatalf("pending cards = %+v, %v, want the preference card private and generic", pending, err)
	}
}
