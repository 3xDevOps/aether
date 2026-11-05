package assistant

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/preference"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store/storetest"
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
	if state, summary, stateErr := w.svc.CardState(t.Context(), w.member, c.ID); stateErr != nil || state == protocol.AssistantCardPending || summary != c.Summary {
		t.Fatalf("card state = %q, %q, %v; want decided with the card's summary", state, summary, stateErr)
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
	open, err := w.svc.openCards(t.Context(), w.member)
	if err != nil || len(open) != 1 || !open[0].Private || open[0].Summary != c.Summary {
		t.Fatalf("status cards = %+v, %v, want the card whole and marked private for the desktop app", open, err)
	}
}

// A preference disabled while its card waits for the member to leave the
// dashboard must not let the card's text reach a locked screen.
func TestACardStaysPrivateWhenItsPreferenceIsDisabled(t *testing.T) {
	dir := t.TempDir()
	db, err := storetest.Open(filepath.Join(dir, "aether.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ada := &domain.Member{DisplayName: "Ada", TailnetLogin: "ada@example.com", Color: "#3cb44b", Role: domain.RoleCollaborator}
	if err = db.CreateMember(t.Context(), ada); err != nil {
		t.Fatal(err)
	}
	prefs := preference.New(liveStore{db}, filepath.Join(dir, "tombstones"))
	saved, err := prefs.Add(t.Context(), ada.ID, "my door code is 4471", "", protocol.PreferenceSourceSettings, "")
	if err != nil {
		t.Fatal(err)
	}
	c := protocol.AssistantConfirmation{
		ID: "launch-1", Method: protocol.MethodRunLaunch, Summary: `Launch claude in api: "Let the courier in, my door code is 4471"`,
		Params: []byte(`{"task":"Let the courier in, my door code is 4471"}`), CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().Add(time.Hour).UTC(), State: protocol.AssistantCardPending,
	}
	row, err := cardRow(ada.ID, c, Door(protocol.AssistantDoorDashboard))
	if err != nil {
		t.Fatal(err)
	}
	if err = db.AddAssistantCard(t.Context(), row); err != nil {
		t.Fatal(err)
	}
	s := New(Config{Dir: filepath.Join(dir, "assistant"), Store: db, Host: &fakeHost{}, Preferences: prefs})
	t.Cleanup(func() { _ = s.Close() })
	private := func(s *Service) bool {
		t.Helper()
		pending, readErr := s.PendingCards(t.Context(), ada.ID)
		if readErr != nil || len(pending) != 1 {
			t.Fatal(pending, readErr)
		}
		return pending[0].Private && !strings.Contains(pending[0].Summary, "4471")
	}
	if !private(s) {
		t.Fatal("a card sending a saved preference is not private")
	}
	off := false
	if _, err = prefs.Update(t.Context(), ada.ID, protocol.PreferenceUpdateParams{ID: saved.Preference.ID, Enabled: &off}, protocol.PreferenceSourceSettings, ""); err != nil {
		t.Fatal(err)
	}
	if !private(s) {
		t.Fatal("the card lost its privacy when its preference was disabled")
	}
	fresh := New(Config{Dir: filepath.Join(dir, "assistant2"), Store: db, Host: &fakeHost{}, Preferences: prefs})
	t.Cleanup(func() { _ = fresh.Close() })
	if !private(fresh) {
		t.Fatal("a server that never saw the card enabled still shows a disabled preference's text")
	}
}
