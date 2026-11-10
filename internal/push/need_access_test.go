package push

import (
	"fmt"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

// A member who lost the run's workspace hears no more of its needs, on a
// device or in the sink, and their digest drops what it held for it.
func TestANeedIsNotToldToAnOwnerWhoLostTheWorkspace(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	heard := &sink{heard: make(chan string, 64)}
	h.sink = heard
	if err := h.svc.Close(); err != nil {
		t.Fatal(err)
	}
	h.start()
	phone := h.subscribe(h.ada)
	run := h.run(h.ada, "Fix the login redirect", domain.LaunchACP)
	want := notification{Title: "Fix the login redirect", Body: "which region?", Run: string(run.ID)}

	h.status(run, domain.RunNeedsAttention, domain.ActivityNeedsYou, "needs input: which region?")
	phone.expect(t, want)
	heard.expect(t, fmt.Sprintf("%s needs %q, answered by %q", run.ID, "which region?", ""))

	if err := h.db.RevokeWorkspaceGrant(h.ctx, h.ws.ID, h.ada.ID); err != nil {
		t.Fatal(err)
	}
	h.status(run, domain.RunNeedsAttention, domain.ActivityNeedsYou, "blocked: need the staging API key")
	phone.expect(t, notification{Title: "Fix the login redirect", Body: clearedBody, Run: string(run.ID), Clear: true})
	heard.expect(t, string(run.ID)+" needs nothing")
	phone.expectNone(t, testQuiet)
	select {
	case got := <-heard.heard:
		if got != string(run.ID)+" needs nothing" {
			t.Fatalf("sink heard %q after the owner lost the workspace", got)
		}
	default:
	}
}

func TestADigestDropsARunWhoseOwnerLostTheWorkspace(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	phone := h.subscribe(h.ada)
	run := h.run(h.ada, "Upgrade the linter", domain.LaunchACP)
	h.finish(run)
	h.eventuallyQueued(h.ada, 1)

	if err := h.db.RevokeWorkspaceGrant(h.ctx, h.ws.ID, h.ada.ID); err != nil {
		t.Fatal(err)
	}
	h.clock.advance(30 * time.Minute)
	if err := h.svc.sendDigest(h.ctx, h.ada.ID); err != nil {
		t.Fatal(err)
	}
	phone.expectNone(t, testQuiet)
	if got := h.digestItems(h.ada); len(got) != 0 {
		t.Fatalf("digest still holds %d items", len(got))
	}
}
