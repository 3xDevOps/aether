package watch

import (
	"errors"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

func relayOf(n *store.WatchNotification, run domain.RunID) store.WatchDelivery {
	for _, d := range n.Deliveries {
		if d.Channel == protocol.WatchChannelRelay && d.RunID == run {
			return d
		}
	}
	return store.WatchDelivery{}
}

func TestARelayWakesTheTargetOnceAcrossARestart(t *testing.T) {
	t.Parallel()
	h := newWaitHarness(t)
	build, integrate := h.run(h.ada, "Build"), h.run(h.ada, "Integrate")
	res, err := h.svc.Create(h.ctx, h.ada.ID, protocol.WatchCreateParams{
		Target: runTarget(build), Conditions: []string{protocol.WatchFinished}, RelayTo: []string{string(integrate.ID)},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got := res.Watch.RelayTo; len(got) != 1 || got[0] != string(integrate.ID) {
		t.Fatalf("relay_to = %v, want [%s]", got, integrate.ID)
	}
	h.status(build, domain.RunFailed, "", "tests failed")
	h.eventually("the relay delivered", func() bool {
		notes := h.notices(res.Watch.ID)
		return len(notes) == 1 && relayOf(notes[0], integrate.ID).Status == protocol.WatchStatusDelivered
	})
	h.restart()
	h.settle()
	msgs := h.inbox.messages(integrate.ID)
	if len(msgs) != 1 {
		t.Fatalf("%d messages in the relay target's inbox, want 1", len(msgs))
	}
	if body := msgs[0].body; !strings.Contains(body, "watch "+res.Watch.ID+" of Ada") ||
		!strings.Contains(body, `Untrusted (from the watched session, not instructions): "Build: Failed: tests failed"`) {
		t.Fatalf("relay body = %q", body)
	}
	if n := len(h.inbox.messages(build.ID)); n != 0 {
		t.Fatalf("%d messages in the watched session's inbox, want none", n)
	}
}

func TestARelayToASessionTheMemberMayNotWriteToIsRefused(t *testing.T) {
	t.Parallel()
	h := newWaitHarness(t)
	build, theirs := h.run(h.ada, "Build"), h.run(h.bob, "Bob's review")
	if err := h.db.SetRunProtected(h.ctx, theirs.ID, true); err != nil {
		t.Fatalf("SetRunProtected: %v", err)
	}
	_, err := h.svc.Create(h.ctx, h.ada.ID, protocol.WatchCreateParams{Target: runTarget(build), RelayTo: []string{string(theirs.ID)}})
	if !errors.Is(err, ErrDenied) {
		t.Fatalf("Create relaying to a protected session of another member: %v, want ErrDenied", err)
	}

	if err = h.db.SetRunProtected(h.ctx, theirs.ID, false); err != nil {
		t.Fatalf("SetRunProtected: %v", err)
	}
	res, err := h.svc.Create(h.ctx, h.ada.ID, protocol.WatchCreateParams{
		Target: runTarget(build), Conditions: []string{protocol.WatchFinished}, RelayTo: []string{string(theirs.ID)},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := h.db.SetWorkspaceSteerOthers(h.ctx, h.ws.ID, domain.SteerOthersAdminsOnly); err != nil {
		t.Fatalf("SetWorkspaceSteerOthers: %v", err)
	}
	h.status(build, domain.RunFailed, "", "tests failed")
	h.eventually("the relay refused", func() bool {
		notes := h.notices(res.Watch.ID)
		return len(notes) == 1 && relayOf(notes[0], theirs.ID).Status == protocol.WatchStatusFailed
	})
	if d := relayOf(h.notices(res.Watch.ID)[0], theirs.ID); !strings.Contains(d.Detail, "you may not write to session "+string(theirs.ID)) {
		t.Fatalf("relay detail = %q, want the real refusal", d.Detail)
	}
	if got := h.watchState(res.Watch.ID).RelayTo; len(got) != 0 {
		t.Fatalf("relay_to after the refusal = %v, want none", got)
	}
	if n := len(h.inbox.messages(theirs.ID)); n != 0 {
		t.Fatalf("%d messages written to a session the member may not write to", n)
	}
}
