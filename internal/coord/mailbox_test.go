package coord

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/evidence"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

// TestSendAuthorizationFollowsTheRadar covers the whole authorization
// model: only a run the radar has this one in conflict with is reachable,
// the grace window keeps an in-flight reply landing after the overlap
// clears, and the window really does expire.
func TestSendAuthorizationFollowsTheRadar(t *testing.T) {
	h := newHarness(t, 3)
	ctx := context.Background()
	a, b, c := h.run(0), h.run(1), h.run(2)

	send := func(from, to domain.RunID) *protocol.Error {
		_, err := h.svc.Send(ctx, from, sendParams(to, "ping"))
		return err
	}

	// No overlap at all: nobody is reachable. The message is pinned by the
	// wire-v1 error fixtures, so it is asserted verbatim here.
	err := send(a, b)
	if err == nil || err.Code != protocol.CodeDenied {
		t.Fatalf("send without an overlap = %v, want CodeDenied", err)
	}
	if want := fmt.Sprintf("coord.send: run %s is not an authorized peer of run %s", b, a); err.Message != want {
		t.Fatalf("denial message = %q, want %q", err.Message, want)
	}

	// The live overlap view is memoized for a moment, and nothing here
	// publishes the overlap event that would drop that memo, so step past
	// the window the way a deployment with a dropped event would.
	h.peers.pair(a, b, "src/auth.go")
	h.advance(radarRefreshInterval)
	if err = send(a, b); err != nil {
		t.Fatalf("send across an active overlap: %v", err)
	}
	// A third run sharing no file stays unreachable even while a is in
	// conflict with someone else.
	if err = send(a, c); err == nil || err.Code != protocol.CodeDenied {
		t.Fatalf("send to a non-overlapping run = %v, want CodeDenied", err)
	}
	if err = send(a, "run_does_not_exist"); err == nil || err.Code != protocol.CodeNotFound ||
		err.Message != "coord.send: unknown run run_does_not_exist" {
		t.Fatalf("send to an unknown run = %v, want CodeNotFound", err)
	}

	// The overlap clears; the grace window opens where it cleared and
	// keeps the pair talking until it runs out.
	h.peers.clear()
	h.advance(radarRefreshInterval)
	st, serr := h.svc.Status(ctx, a)
	if serr != nil {
		t.Fatalf("Status: %v", serr)
	}
	if len(st.Peers) != 1 || st.Peers[0].State != protocol.CoordPeerGrace || st.Peers[0].ExpiresAt == "" {
		t.Fatalf("status peers = %+v, want one grace peer with an expiry", st.Peers)
	}
	h.advance(DefaultGrace - time.Minute)
	if err = send(a, b); err != nil {
		t.Fatalf("send inside the grace window: %v", err)
	}

	h.advance(2 * time.Minute)
	if err = send(a, b); err == nil || err.Code != protocol.CodeDenied {
		t.Fatalf("send after the grace window = %v, want CodeDenied", err)
	}
	if st, serr = h.svc.Status(ctx, a); serr != nil || len(st.Peers) != 0 {
		t.Fatalf("status after expiry = %+v (err %v), want no peers", st.Peers, serr)
	}
}

// TestStatusReportsExactlyTheSendableSet pins the equality the agent
// depends on: every peer status lists is sendable, and nothing else is.
func TestStatusReportsExactlyTheSendableSet(t *testing.T) {
	h := newHarness(t, 3)
	ctx := context.Background()
	a, b, c := h.run(0), h.run(1), h.run(2)
	h.peers.pair(a, b, "src/auth.go")

	st, err := h.svc.Status(ctx, a)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if st.WireVersion != protocol.CoordWireVersion || st.RunID != string(a) || st.WorkspaceID != string(h.workspace) {
		t.Fatalf("status identity = %+v, want the calling run", st)
	}
	listed := make(map[string]bool, len(st.Peers))
	for _, p := range st.Peers {
		listed[p.RunID] = true
		if p.State != protocol.CoordPeerActive || p.MemberID == "" || len(p.Files) == 0 {
			t.Fatalf("peer %+v, want an attributed active peer with files", p)
		}
	}
	for _, target := range []domain.RunID{b, c} {
		_, serr := h.svc.Send(ctx, a, sendParams(target, "ping"))
		if listed[string(target)] != (serr == nil) {
			t.Fatalf("run %s listed=%v but send error=%v", target, listed[string(target)], serr)
		}
	}
	if st.Unread != 0 {
		t.Fatalf("unread = %d, want 0", st.Unread)
	}
	if st, err = h.svc.Status(ctx, b); err != nil || st.Unread != 1 {
		t.Fatalf("target unread = %d (err %v), want 1", st.Unread, err)
	}
}

// TestMissionRunSeesAndMessagesRadarPeers follows the overlap notice a
// mission worker gets about a run outside its mission: status lists that
// run with the shared files after the assignment peers, and the worker can
// message or ask it, while a run it shares nothing with stays refused.
func TestMissionRunSeesAndMessagesRadarPeers(t *testing.T) {
	stub := &missionTransportStub{}
	h := newHarness(t, 4, func(c *Config) { c.Mission = stub })
	ctx := context.Background()
	worker, integrator, outsider, unrelated := h.run(0), h.run(1), h.run(2), h.run(3)
	stub.mission = []domain.RunID{worker, integrator}
	h.peers.hub(worker, []domain.RunID{integrator, outsider}, "src/auth.go")

	st, err := h.svc.Status(ctx, worker)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if st.Assignment == nil || len(st.Peers) != 2 || st.PeerTotal != 2 || st.PeersTruncated {
		t.Fatalf("status = %+v, want an assignment and exactly two peers", st)
	}
	for i, want := range []struct {
		run   domain.RunID
		state string
	}{{integrator, protocol.CoordPeerMission}, {outsider, protocol.CoordPeerActive}} {
		p := st.Peers[i]
		if p.RunID != string(want.run) || p.State != want.state || p.MemberID == "" ||
			len(p.Files) != 1 || p.Files[0] != "src/auth.go" || p.FileTotal != 1 || p.FilesTruncated {
			t.Fatalf("peer %d = %+v, want run %s in state %q with src/auth.go", i, p, want.run, want.state)
		}
	}

	if _, err = h.svc.Send(ctx, worker, sendParams(integrator, "mission peer")); err != nil {
		t.Fatalf("send to a mission peer: %v", err)
	}
	if _, err = h.svc.Send(ctx, worker, sendParams(outsider, "we both touch auth.go")); err != nil {
		t.Fatalf("send to an overlapping run outside the mission: %v", err)
	}
	if _, err = h.svc.Ask(ctx, worker, protocol.CoordAskParams{
		ToRunID: string(outsider), Body: "May I take auth.go?", IdempotencyKey: "ask-outsider",
	}); err != nil {
		t.Fatalf("ask an overlapping run outside the mission: %v", err)
	}
	_, err = h.svc.Send(ctx, worker, sendParams(unrelated, "ping"))
	if err == nil || err.Code != protocol.CodeDenied ||
		err.Message != fmt.Sprintf("coord.send: run %s is not an authorized peer of run %s", unrelated, worker) {
		t.Fatalf("send to an unrelated run = %v, want CodeDenied", err)
	}
	if st, err = h.svc.Status(ctx, outsider); err != nil || st.Unread != 2 {
		t.Fatalf("outsider unread = %d (err %v), want 2", st.Unread, err)
	}
}

// TestSendCaps covers the three guards that keep an agent bounded: body
// size, inbox depth, and the send rate.
func TestSendCaps(t *testing.T) {
	ctx := context.Background()

	t.Run("body size", func(t *testing.T) {
		h := newHarness(t, 2)
		h.peers.pair(h.run(0), h.run(1), "src/auth.go")
		body := strings.Repeat("x", protocol.CoordMaxBodyBytes+1)
		_, err := h.svc.Send(ctx, h.run(0), sendParams(h.run(1), body))
		if err == nil || err.Code != protocol.CodeInvalidParams ||
			err.Message != "coord.send: body exceeds 4096 bytes" {
			t.Fatalf("oversized body = %v, want the pinned CodeInvalidParams", err)
		}
	})

	t.Run("rate limit", func(t *testing.T) {
		h := newHarness(t, 2)
		h.peers.pair(h.run(0), h.run(1), "src/auth.go")
		for i := range sendBurst {
			if _, err := h.svc.Send(ctx, h.run(0), sendParams(h.run(1), fmt.Sprintf("ping-%d", i))); err != nil {
				t.Fatalf("send %d within the burst: %v", i, err)
			}
		}
		_, err := h.svc.Send(ctx, h.run(0), sendParams(h.run(1), "ping-final"))
		if err == nil || err.Code != protocol.CodeConflict ||
			err.Message != "coord.send: rate limit exceeded (burst 5, 1 message per 5s)" {
			t.Fatalf("send past the burst = %v, want the pinned CodeConflict", err)
		}
		h.advance(sendRefill)
		if _, err := h.svc.Send(ctx, h.run(0), sendParams(h.run(1), "ping-refill")); err != nil {
			t.Fatalf("send after a refill: %v", err)
		}
	})

	t.Run("full inbox", func(t *testing.T) {
		h := newHarness(t, 2)
		h.peers.pair(h.run(0), h.run(1), "src/auth.go")
		// The depth cap, not the rate, is under test: fill the inbox
		// through the store and let one send hit the wall.
		for range protocol.CoordMaxUnread {
			msg := &store.RunMessage{WorkspaceID: h.workspace, FromRun: h.run(0), ToRun: h.run(1), Body: "filler"}
			if err := h.db.AppendRunMessage(ctx, msg, protocol.CoordMaxUnread); err != nil {
				t.Fatalf("seed mailbox: %v", err)
			}
		}
		_, err := h.svc.Send(ctx, h.run(0), sendParams(h.run(1), "one too many"))
		if err == nil || err.Code != protocol.CodeConflict || !strings.Contains(err.Message, "inbox is full") {
			t.Fatalf("send to a full inbox = %v, want an explicit CodeConflict", err)
		}
	})
}

// TestSendRejectsFinishedAndSelfTargets covers the remaining rejection
// paths a live conversation runs into.
func TestSendRejectsFinishedAndSelfTargets(t *testing.T) {
	h := newHarness(t, 2)
	ctx := context.Background()
	a, b := h.run(0), h.run(1)
	h.peers.pair(a, b, "src/auth.go")

	if _, err := h.svc.Send(ctx, a, sendParams(a, "hi")); err == nil || err.Code != protocol.CodeInvalidParams {
		t.Fatalf("self-send = %v, want CodeInvalidParams", err)
	}
	if err := h.db.UpdateRunStatus(ctx, b, domain.RunMerged, "", nil, nil); err != nil {
		t.Fatalf("finish run: %v", err)
	}
	_, err := h.svc.Send(ctx, a, sendParams(b, "hi-finished"))
	if err == nil || err.Code != protocol.CodeUnavailable ||
		err.Message != fmt.Sprintf("coord.send: run %s has finished", b) {
		t.Fatalf("send to a finished run = %v, want CodeUnavailable", err)
	}
}

// TestInboxBatchAndTokenSemantics drives the at-least-once contract
// through the service: one batch per token, redelivery until it is
// acknowledged, a foreign token acknowledging nothing, and an empty inbox
// handing out no token at all.
func TestInboxBatchAndTokenSemantics(t *testing.T) {
	h := newHarness(t, 2)
	ctx := context.Background()
	a, b := h.run(0), h.run(1)
	h.peers.pair(a, b, "src/auth.go")

	if _, err := h.svc.Send(ctx, a, sendParams(b, "first")); err != nil {
		t.Fatalf("send: %v", err)
	}
	first, err := h.svc.Inbox(ctx, b, protocol.CoordInboxParams{})
	if err != nil {
		t.Fatalf("Inbox: %v", err)
	}
	if len(first.Messages) != 1 || first.Messages[0].Body != "first" || first.AckToken == "" {
		t.Fatalf("first read = %+v, want one message and a token", first)
	}
	if first.Messages[0].FromRunID != string(a) {
		t.Fatalf("message sender = %q, want %q", first.Messages[0].FromRunID, a)
	}

	// The response never reached the agent: the retry redelivers.
	if _, serr := h.svc.Send(ctx, a, sendParams(b, "second")); serr != nil {
		t.Fatalf("send (second): %v", serr)
	}
	retry, err := h.svc.Inbox(ctx, b, protocol.CoordInboxParams{})
	if err != nil {
		t.Fatalf("Inbox (retry): %v", err)
	}
	if len(retry.Messages) != 1 || retry.Messages[0].ID != first.Messages[0].ID || retry.AckToken != first.AckToken {
		t.Fatalf("retry = %+v, want the same batch under the same token", retry)
	}
	// A token from the other run acknowledges nothing.
	if _, oerr := h.svc.Inbox(ctx, a, protocol.CoordInboxParams{AckToken: first.AckToken}); oerr != nil {
		t.Fatalf("Inbox (other run): %v", oerr)
	}
	if again, aerr := h.svc.Inbox(ctx, b, protocol.CoordInboxParams{}); aerr != nil || len(again.Messages) != 1 {
		t.Fatalf("after a foreign ack = %+v (err %v), want the batch outstanding", again, aerr)
	}

	next, err := h.svc.Inbox(ctx, b, protocol.CoordInboxParams{AckToken: first.AckToken})
	if err != nil {
		t.Fatalf("Inbox (ack): %v", err)
	}
	if len(next.Messages) != 1 || next.Messages[0].Body != "second" || next.AckToken == first.AckToken {
		t.Fatalf("after ack = %+v, want the second message under a fresh token", next)
	}
	drained, err := h.svc.Inbox(ctx, b, protocol.CoordInboxParams{AckToken: next.AckToken})
	if err != nil {
		t.Fatalf("Inbox (drain): %v", err)
	}
	if len(drained.Messages) != 0 || drained.AckToken != "" {
		t.Fatalf("drained inbox = %+v, want no messages and no token", drained)
	}
}

// TestGraceWindowRunsFromTheLastOverlap covers the case where the clearing
// is noticed late: the bus drops the overlap event when a subscriber falls
// behind, and nobody calls coord.* for a while afterwards. The window must
// still be measured from when the runs last overlapped, or a late
// discovery would authorize a send long after the spec's 10 minutes.
func TestGraceWindowRunsFromTheLastOverlap(t *testing.T) {
	h := newHarness(t, 2)
	ctx := context.Background()
	a, b := h.run(0), h.run(1)

	h.peers.pair(a, b, "src/auth.go")
	if _, err := h.svc.Send(ctx, a, sendParams(b, "ping-active")); err != nil {
		t.Fatalf("send across an active overlap: %v", err)
	}

	// The overlap clears here, and the notification never arrives: no event
	// is published and nothing calls the service for 45 minutes.
	h.peers.clear()
	h.advance(45 * time.Minute)

	if _, err := h.svc.Send(ctx, a, sendParams(b, "ping-after-clear")); err == nil || err.Code != protocol.CodeDenied {
		t.Fatalf("send %v after the overlap cleared = %v, want CodeDenied", 45*time.Minute, err)
	}
	st, err := h.svc.Status(ctx, a)
	if err != nil || len(st.Peers) != 0 {
		t.Fatalf("status = %+v (err %v), want no peers", st.Peers, err)
	}
}

// TestGraceWindowRunsFromAWitnessedClear covers the other side of the
// anchor: the clearing arrives as a live event, but the pair had been
// overlapping quietly for far longer than the grace period, so nothing
// had refreshed the peer's lastSeen. The window must run its full length
// from the witnessed clear, not be pre-expired by the stale timestamp.
func TestGraceWindowRunsFromAWitnessedClear(t *testing.T) {
	h := newHarness(t, 3)
	ctx := context.Background()
	h.start()
	a, b, c := h.run(0), h.run(1), h.run(2)

	h.peers.pair(a, b, "src/auth.go")
	if _, err := h.svc.Send(ctx, a, sendParams(b, "ping-active")); err != nil {
		t.Fatalf("send across an active overlap: %v", err)
	}

	// The overlap persists untouched for 45 minutes - nothing calls the
	// service - then clears in real time: the index reports a's set is
	// now just c and publishes it. The banner for c proves the event was
	// consumed before the sends below.
	h.advance(45 * time.Minute)
	h.peers.pair(a, c, "src/other.go")
	h.announce(t, a, events.OverlapPeer{RunID: c, Files: []string{"src/other.go"}})
	h.waitForInjections(t, 1)

	h.advance(DefaultGrace - time.Minute)
	if _, err := h.svc.Send(ctx, a, sendParams(b, "ping-grace")); err != nil {
		t.Fatalf("send inside the grace window: %v", err)
	}
	h.advance(2 * time.Minute)
	if _, err := h.svc.Send(ctx, a, sendParams(b, "ping-expired")); err == nil || err.Code != protocol.CodeDenied {
		t.Fatalf("send after the grace window = %v, want CodeDenied", err)
	}
}

// TestSendPeerCap bounds the spray a manufactured overlap edge would buy.
// The edge itself is computed from the two runs' own diff snapshots, so a
// run that touches every tracked file is reported as overlapping with
// every other run in the workspace and every one of them is authorized.
// What the cap denies is talking to all of them.
func TestSendPeerCap(t *testing.T) {
	h := newHarness(t, maxPeers+3)
	ctx := context.Background()
	spray := h.run(0)
	targets := make([]domain.RunID, 0, maxPeers+2)
	for i := 1; i < len(h.runs); i++ {
		targets = append(targets, h.run(i))
	}
	h.peers.hub(spray, targets, "src/auth.go")

	send := func(to domain.RunID) *protocol.Error {
		// Step the clock so the send rate, a separate cap, is never what
		// answers here.
		h.advance(sendRefill)
		_, err := h.svc.Send(ctx, spray, sendParams(to, "ping"))
		return err
	}

	if st, err := h.svc.Status(ctx, spray); err != nil || len(st.Peers) != len(targets) {
		t.Fatalf("status peers = %d (err %v), want all %d authorized", len(st.Peers), err, len(targets))
	}
	for i, to := range targets[:maxPeers] {
		if err := send(to); err != nil {
			t.Fatalf("send to peer %d of %d: %v", i+1, maxPeers, err)
		}
	}
	err := send(targets[maxPeers])
	if err == nil || err.Code != protocol.CodeConflict {
		t.Fatalf("send to peer %d = %v, want CodeConflict", maxPeers+1, err)
	}
	if err = send(targets[maxPeers+1]); err == nil || err.Code != protocol.CodeConflict {
		t.Fatalf("the cap did not hold for a second new peer: %v", err)
	}
	// A conversation already open is unaffected: the cap counts peers, not
	// messages.
	if err = send(targets[0]); err != nil {
		t.Fatalf("send to an already-open peer: %v", err)
	}
}

// TestStatusSharesOneRadarRead covers the cost of an unauthenticated,
// unmetered call. Every coord.status recomputes the radar's whole view
// under the index's own lock, so an agent looping it on the connections it
// holds could degrade the radar for the entire server; a burst has to
// share one read.
func TestStatusSharesOneRadarRead(t *testing.T) {
	h := newHarness(t, 2)
	ctx := context.Background()
	h.peers.pair(h.run(0), h.run(1), "src/auth.go")

	for range 25 {
		if _, err := h.svc.Status(ctx, h.run(0)); err != nil {
			t.Fatalf("Status: %v", err)
		}
	}
	if got := h.peers.readCount(); got != 1 {
		t.Fatalf("radar reads for a burst of 25 status calls = %d, want 1", got)
	}

	h.advance(radarRefreshInterval)
	if _, err := h.svc.Status(ctx, h.run(0)); err != nil {
		t.Fatalf("Status after the memo window: %v", err)
	}
	if got := h.peers.readCount(); got != 2 {
		t.Fatalf("radar reads after the memo window = %d, want 2", got)
	}
}

// TestInboxRateLimit bounds the one coord call that opens a write
// transaction on the server's shared database: a run driving the raw
// socket cannot loop coord.inbox past its bucket.
func TestInboxRateLimit(t *testing.T) {
	h := newHarness(t, 2)
	ctx := context.Background()
	b := h.run(1)

	for i := range inboxBurst {
		if _, err := h.svc.Inbox(ctx, b, protocol.CoordInboxParams{}); err != nil {
			t.Fatalf("inbox %d within the burst: %v", i, err)
		}
	}
	_, err := h.svc.Inbox(ctx, b, protocol.CoordInboxParams{})
	if err == nil || err.Code != protocol.CodeConflict || !strings.Contains(err.Message, "rate limit") {
		t.Fatalf("inbox past the burst = %v, want CodeConflict", err)
	}
	h.advance(inboxRefill)
	if _, err := h.svc.Inbox(ctx, b, protocol.CoordInboxParams{}); err != nil {
		t.Fatalf("inbox after a refill: %v", err)
	}
}

// TestSendStampsOneWorkspaceNote pins the audit trail a send leaves.
// Radar peers are always runs of the same workspace, so both sides of the
// exchange share one timeline and one note covers it: a second stamp would
// only double the entry the humans read.
func TestSendStampsOneWorkspaceNote(t *testing.T) {
	h := newHarness(t, 2)
	ctx := context.Background()
	a, b := h.run(0), h.run(1)
	h.peers.pair(a, b, "src/auth.go")

	timeline, err := h.bus.Subscribe(ctx, events.SubscribeOptions{
		Filter: events.Filter{Types: []events.Type{events.TypeTimeline}},
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer timeline.Close() //nolint:errcheck // test cleanup

	if _, serr := h.svc.Send(ctx, a, sendParams(b, "hold off on auth.go")); serr != nil {
		t.Fatalf("Send: %v", serr)
	}

	var note events.Event
	select {
	case note = <-timeline.Events():
	case <-time.After(2 * time.Second):
		t.Fatal("the coordination message was never stamped into the timeline")
	}
	p, ok := note.Payload.(events.TimelinePayload)
	if !ok || note.ActorID != "" || note.WorkspaceID != h.workspace || note.RunID != a {
		t.Fatalf("timeline event = %+v, want a server-originated note on the sender's run", note)
	}
	if !strings.Contains(p.Message, "coordination message to run "+string(b)) {
		t.Fatalf("note = %q, want the outgoing stamp", p.Message)
	}

	// A second send is what proves the first left exactly one note: its own
	// stamp is the next event on the stream, with nothing between them.
	if _, serr := h.svc.Send(ctx, a, sendParams(b, "still on it")); serr != nil {
		t.Fatalf("second Send: %v", serr)
	}
	select {
	case next := <-timeline.Events():
		np, nok := next.Payload.(events.TimelinePayload)
		if !nok || !strings.Contains(np.Message, "still on it") {
			t.Fatalf("second event = %+v, want the second send's own stamp and no duplicate of the first", next)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the second coordination message was never stamped")
	}
}

// TestWitnessedClearSurvivesAnInterleavedRefresh covers the race between
// the index publishing a clearing and the consumer processing it: a coord
// call in that window re-reads the index first and expires the pair
// against its stale anchor. The event, once it lands, must still open the
// full grace window.
func TestWitnessedClearSurvivesAnInterleavedRefresh(t *testing.T) {
	h := newHarness(t, 2)
	ctx := context.Background()
	a, b := h.run(0), h.run(1)

	h.peers.pair(a, b, "src/auth.go")
	if _, err := h.svc.Send(ctx, a, sendParams(b, "ping-active")); err != nil {
		t.Fatalf("send across an active overlap: %v", err)
	}
	h.advance(45 * time.Minute)

	// The overlap clears and a status call re-reads the index before the
	// published event is consumed: the stale anchor expires the pair.
	h.peers.clear()
	if st, err := h.svc.Status(ctx, a); err != nil || len(st.Peers) != 0 {
		t.Fatalf("status during the window = %+v (err %v), want no peers", st.Peers, err)
	}

	// The in-flight event lands: the clearing was witnessed after all, so
	// the grace window anchors at the event.
	h.svc.radar.observe(a, nil)
	if _, err := h.svc.Send(ctx, a, sendParams(b, "still there?")); err != nil {
		t.Fatalf("send inside the witnessed grace window: %v", err)
	}
	h.advance(DefaultGrace + time.Minute)
	if _, err := h.svc.Send(ctx, a, sendParams(b, "ping-expired")); err == nil || err.Code != protocol.CodeDenied {
		t.Fatalf("send after the grace window = %v, want CodeDenied", err)
	}
}

func TestAskReplyCorrelationAndAuthorization(t *testing.T) {
	h := newHarness(t, 2)
	ctx := context.Background()
	a, b := h.run(0), h.run(1)
	h.peers.pair(a, b, "src/auth.go")

	asked, err := h.svc.Ask(ctx, a, protocol.CoordAskParams{
		ToRunID: string(b), Body: "May I update auth.go?", IdempotencyKey: "ask-1",
	})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	inbox, err := h.svc.Inbox(ctx, b, protocol.CoordInboxParams{})
	if err != nil || len(inbox.Messages) != 1 {
		t.Fatalf("question inbox = %+v (err %v)", inbox, err)
	}
	if inbox.Messages[0].Kind != protocol.CoordMessageKindQuestion ||
		inbox.Messages[0].CorrelationID != asked.QuestionID {
		t.Fatalf("question = %+v, want kind/correlation", inbox.Messages[0])
	}

	// A correlated reply remains deliverable after the ordinary overlap
	// grace has expired.
	h.peers.clear()
	h.advance(DefaultGrace + radarRefreshInterval)
	replied, err := h.svc.Reply(ctx, b, protocol.CoordReplyParams{
		QuestionID: asked.QuestionID, Body: "Yes, proceed.", IdempotencyKey: "reply-1",
	})
	if err != nil {
		t.Fatalf("Reply after grace: %v", err)
	}
	if replied.MessageID == "" {
		t.Fatal("Reply returned no message id")
	}
	if _, denied := h.svc.Send(ctx, b, protocol.CoordSendParams{
		ToRunID: string(a), Body: "unrelated", IdempotencyKey: "send-unrelated",
	}); denied == nil || denied.Code != protocol.CodeDenied {
		t.Fatalf("unrelated send after grace = %v, want CodeDenied", denied)
	}
	got, err := h.svc.Inbox(ctx, a, protocol.CoordInboxParams{})
	if err != nil || len(got.Messages) != 1 || got.Messages[0].Kind != protocol.CoordMessageKindReply ||
		got.Messages[0].CorrelationID != asked.QuestionID {
		t.Fatalf("reply inbox = %+v (err %v), want correlated reply", got, err)
	}

	retry, err := h.svc.Reply(ctx, b, protocol.CoordReplyParams{
		QuestionID: asked.QuestionID, Body: "Yes, proceed.", IdempotencyKey: "reply-1",
	})
	if err != nil || retry.MessageID != replied.MessageID {
		t.Fatalf("idempotent reply = %+v (err %v), want %q", retry, err, replied.MessageID)
	}
	if _, err := h.svc.Reply(ctx, b, protocol.CoordReplyParams{
		QuestionID: asked.QuestionID, Body: "changed on retry", IdempotencyKey: "reply-1",
	}); err == nil || err.Code != protocol.CodeConflict {
		t.Fatalf("changed reply retry = %v, want CodeConflict", err)
	}
}

func TestInboxWaitWakesOnMessage(t *testing.T) {
	h := newHarness(t, 2)
	ctx := context.Background()
	a, b := h.run(0), h.run(1)
	h.peers.pair(a, b, "src/auth.go")
	result := make(chan protocol.CoordInboxResult, 1)
	errs := make(chan *protocol.Error, 1)
	go func() {
		got, err := h.svc.Inbox(ctx, b, protocol.CoordInboxParams{WaitSeconds: 2})
		result <- got
		errs <- err
	}()
	time.Sleep(50 * time.Millisecond)
	if _, err := h.svc.Send(ctx, a, protocol.CoordSendParams{
		ToRunID: string(b), Body: "wake", IdempotencyKey: "wake-1",
	}); err != nil {
		t.Fatalf("Send wake: %v", err)
	}
	select {
	case got := <-result:
		if err := <-errs; err != nil || len(got.Messages) != 1 || got.Messages[0].Body != "wake" {
			t.Fatalf("wait result = %+v (err %v)", got, err)
		}
	case <-time.After(time.Second):
		t.Fatal("inbox wait did not wake on a newly persisted message")
	}
}

// coordReportEvidenceCapture is deliberately deterministic so this test can
// distinguish one automatic capture from an idempotent retry.
type coordReportEvidenceCapture struct {
	id    string
	calls atomic.Int32
}

func (c *coordReportEvidenceCapture) Capture(_ context.Context, req evidence.Request) (protocol.EvidencePacket, error) {
	c.calls.Add(1)
	return protocol.EvidencePacket{
		ID:             c.id,
		RunID:          string(req.RunID),
		CreatorID:      string(req.CreatorID),
		Trigger:        protocol.EvidenceReport,
		IdempotencyKey: req.IdempotencyKey,
	}, nil
}

type failingCoordReportReservationStore struct {
	store.MessageStore
	err error
}

func (s *failingCoordReportReservationStore) ReserveCoordReport(context.Context, *store.CoordReport) (bool, error) {
	return false, s.err
}

func TestCoordReportStopsBeforeEvidenceCaptureWhenReservationFails(t *testing.T) {
	capture := &coordReportEvidenceCapture{id: "must-not-capture"}
	reservationErr := fmt.Errorf("sqlite: database is busy")
	h := newHarness(t, 1, func(c *Config) {
		c.Evidence = capture
		c.Mail = &failingCoordReportReservationStore{MessageStore: c.Mail, err: reservationErr}
	})

	_, rpcErr := h.svc.CoordReport(context.Background(), h.run(0), protocol.CoordReportParams{
		Outcome:        protocol.CoordOutcomeFailure,
		Summary:        "reservation must succeed before capture",
		IdempotencyKey: "reservation-failure",
	})
	if rpcErr == nil || rpcErr.Code != protocol.CodeInternal ||
		!strings.Contains(rpcErr.Message, reservationErr.Error()) {
		t.Fatalf("CoordReport reservation failure = %v, want contextual CodeInternal", rpcErr)
	}
	if got := capture.calls.Load(); got != 0 {
		t.Fatalf("evidence captures after reservation failure = %d, want 0", got)
	}
}

type coordConflictBus struct{}

func (coordConflictBus) Publish(context.Context, events.Event) (events.Event, error) {
	return events.Event{}, events.ErrEventIDConflict
}

func (coordConflictBus) Subscribe(context.Context, events.SubscribeOptions) (events.Subscription, error) {
	return nil, events.ErrBusClosed
}

func (coordConflictBus) Close() error { return nil }

func TestCoordReportRejectsEmptyEvidencePacket(t *testing.T) {
	capture := &coordReportEvidenceCapture{}
	h := newHarness(t, 1, func(c *Config) { c.Evidence = capture })
	ctx := context.Background()
	run := h.run(0)
	_, err := h.svc.CoordReport(ctx, run, protocol.CoordReportParams{
		Outcome: protocol.CoordOutcomeSuccess, Summary: "nothing retained",
		IdempotencyKey: "empty-evidence",
	})
	if err == nil || err.Code != protocol.CodeInternal ||
		!strings.Contains(err.Message, "empty packet id") {
		t.Fatalf("CoordReport with empty evidence packet = %v, want empty packet id internal error", err)
	}
	reserved, lookupErr := h.db.GetCoordReportByIdempotency(ctx, run, "empty-evidence")
	if lookupErr != nil || reserved.State != store.CoordReportPending {
		t.Fatalf("empty evidence reservation = %+v (err %v), want retryable pending reservation", reserved, lookupErr)
	}
}

func TestCoordReportValidationAndIdempotency(t *testing.T) {
	capture := &coordReportEvidenceCapture{id: "ev_report_01"}
	h := newHarness(t, 1, func(c *Config) { c.Evidence = capture })
	ctx := context.Background()
	run := h.run(0)
	first, err := h.svc.CoordReport(ctx, run, protocol.CoordReportParams{
		Outcome: protocol.CoordOutcomeBlocked, Summary: "needs review",
		EvidenceRefs: []string{"ev_01"}, IdempotencyKey: "report-1",
	})
	if err != nil || first.ReportID == "" || first.EvidenceRef != capture.id {
		t.Fatalf("CoordReport = %+v (err %v), want a report and captured evidence ref %q", first, err, capture.id)
	}
	retry, err := h.svc.CoordReport(ctx, run, protocol.CoordReportParams{
		Outcome: protocol.CoordOutcomeBlocked, Summary: "needs review",
		EvidenceRefs: []string{"ev_01"}, IdempotencyKey: "report-1",
	})
	if err != nil || retry.ReportID != first.ReportID || retry.EvidenceRef != first.EvidenceRef {
		t.Fatalf("idempotent CoordReport = %+v (err %v), want report %q and evidence ref %q", retry, err, first.ReportID, first.EvidenceRef)
	}
	if _, err := h.svc.CoordReport(ctx, run, protocol.CoordReportParams{
		Outcome: protocol.CoordOutcomeSuccess, Summary: "different",
		IdempotencyKey: "report-1",
	}); err == nil || err.Code != protocol.CodeConflict {
		t.Fatalf("changed CoordReport retry = %v, want CodeConflict", err)
	}
	if got := capture.calls.Load(); got != 1 {
		t.Fatalf("evidence captures = %d, want 1 across initial report and retry", got)
	}
	stored, storeErr := h.db.GetCoordReportByIdempotency(ctx, run, "report-1")
	if storeErr != nil {
		t.Fatalf("GetCoordReportByIdempotency: %v", storeErr)
	}
	if stored.ID != first.ReportID || len(stored.EvidenceRefs) != 2 ||
		stored.EvidenceRefs[0] != "ev_01" || stored.EvidenceRefs[1] != capture.id {
		t.Fatalf("durable report = %+v, want report %q with refs [ev_01 %q]", stored, first.ReportID, capture.id)
	}

	if _, rpcErr := h.svc.CoordReport(ctx, run, protocol.CoordReportParams{
		Outcome: protocol.CoordOutcomeSuccess, Summary: "another key",
		IdempotencyKey: "report-2",
	}); rpcErr == nil || rpcErr.Code != protocol.CodeConflict {
		t.Fatalf("different-key CoordReport = %v, want CodeConflict", rpcErr)
	}

	for _, bad := range []protocol.CoordReportParams{
		{Outcome: "unknown", Summary: "bad", IdempotencyKey: "bad-1"},
		{Outcome: protocol.CoordOutcomeSuccess, Summary: "bad"},
		{Outcome: protocol.CoordOutcomeSuccess, Summary: strings.Repeat("x", protocol.CoordMaxSummaryBytes+1), IdempotencyKey: "bad-2"},
		{Outcome: protocol.CoordOutcomeSuccess, Summary: "too many refs",
			EvidenceRefs: make([]string, protocol.CoordMaxEvidenceRefs), IdempotencyKey: "bad-refs"},
	} {
		if _, err := h.svc.CoordReport(ctx, run, bad); err == nil || err.Code != protocol.CodeInvalidParams {
			t.Errorf("invalid CoordReport %+v = %v, want CodeInvalidParams", bad, err)
		}
	}
	if got := capture.calls.Load(); got != 1 {
		t.Fatalf("invalid report requests triggered evidence capture: %d calls, want 1", got)
	}
}

func TestCoordReportPublishesEvidenceOnlyAfterFinalizationOnce(t *testing.T) {
	capture := &coordReportEvidenceCapture{id: "ev_report_event"}
	h := newHarness(t, 1, func(c *Config) { c.Evidence = capture })
	ctx := context.Background()
	run := h.run(0)
	sub, err := h.bus.Subscribe(ctx, events.SubscribeOptions{
		Filter: events.Filter{Types: []events.Type{events.TypeEvidencePacket}},
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer sub.Close() //nolint:errcheck // test cleanup

	first, rpcErr := h.svc.CoordReport(ctx, run, protocol.CoordReportParams{
		Outcome: protocol.CoordOutcomeSuccess, Summary: "published once",
		IdempotencyKey: "report-event",
	})
	if rpcErr != nil {
		t.Fatalf("CoordReport: %v", rpcErr)
	}
	select {
	case event := <-sub.Events():
		payload, ok := event.Payload.(events.EvidencePacketPayload)
		if !ok || payload.PacketID != first.EvidenceRef || event.WorkspaceID != h.workspace || event.RunID != run {
			t.Fatalf("evidence event = %+v, want packet %q on run %s", event, first.EvidenceRef, run)
		}
	case <-time.After(time.Second):
		t.Fatal("coord.report returned without publishing its finalized evidence event")
	}
	if _, rpcErr = h.svc.CoordReport(ctx, run, protocol.CoordReportParams{
		Outcome: protocol.CoordOutcomeSuccess, Summary: "published once",
		IdempotencyKey: "report-event",
	}); rpcErr != nil {
		t.Fatalf("idempotent CoordReport retry: %v", rpcErr)
	}
	select {
	case event := <-sub.Events():
		t.Fatalf("idempotent retry published duplicate evidence event: %+v", event)
	case <-time.After(50 * time.Millisecond):
	}
}
func TestCoordOutboxQuarantinesEventIDConflict(t *testing.T) {
	ctx := context.Background()
	t.Run("report", func(t *testing.T) {
		capture := &coordReportEvidenceCapture{id: "ev_conflict_report"}
		h := newHarness(t, 1, func(c *Config) { c.Evidence = capture })
		h.svc.cfg.Bus = coordConflictBus{}
		result, rpcErr := h.svc.CoordReport(ctx, h.run(0), protocol.CoordReportParams{
			Outcome: protocol.CoordOutcomeFailure, Summary: "event collision",
			IdempotencyKey: "report-event-conflict",
		})
		if rpcErr != nil || result.ReportID == "" {
			t.Fatalf("CoordReport = %+v (err %v), want finalized report with deferred publication", result, rpcErr)
		}
		if _, _, err := h.svc.drainOutboxPage(ctx); err != nil {
			t.Fatalf("drain report conflict: %v", err)
		}
		pub, err := h.db.GetCoordReportPublication(ctx, result.ReportID)
		if err != nil {
			t.Fatalf("GetCoordReportPublication: %v", err)
		}
		if pub.QuarantinedAt == nil || pub.QuarantineError == "" || pub.Attempts != 1 {
			t.Fatalf("report conflict state = %+v, want one quarantined attempt", pub)
		}
	})
	t.Run("audit", func(t *testing.T) {
		h := newHarness(t, 2)
		h.svc.cfg.Bus = coordConflictBus{}
		msg := &store.RunMessage{
			WorkspaceID: h.workspace, FromRun: h.run(0), ToRun: h.run(1),
			Body: "event collision",
		}
		if err := h.db.AppendRunMessage(ctx, msg, 100); err != nil {
			t.Fatalf("AppendRunMessage: %v", err)
		}
		if _, _, err := h.svc.drainOutboxPage(ctx); err != nil {
			t.Fatalf("drain audit conflict: %v", err)
		}
		pub, err := h.db.GetCoordAuditPublication(ctx, msg.ID)
		if err != nil {
			t.Fatalf("GetCoordAuditPublication: %v", err)
		}
		if pub.QuarantinedAt == nil || pub.QuarantineError == "" || pub.Attempts != 1 {
			t.Fatalf("audit conflict state = %+v, want one quarantined attempt", pub)
		}
	})
}
