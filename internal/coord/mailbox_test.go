package coord

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
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
		_, err := h.svc.Send(ctx, from, protocol.CoordSendParams{ToRunID: string(to), Body: "ping"})
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
	if st.WireVersion != protocol.CoordWireVersion || st.RunID != string(a) || st.SessionID != string(h.session) {
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
		_, serr := h.svc.Send(ctx, a, protocol.CoordSendParams{ToRunID: string(target), Body: "ping"})
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

// TestSendCaps covers the three guards that keep an agent bounded: body
// size, inbox depth, and the send rate.
func TestSendCaps(t *testing.T) {
	ctx := context.Background()

	t.Run("body size", func(t *testing.T) {
		h := newHarness(t, 2)
		h.peers.pair(h.run(0), h.run(1), "src/auth.go")
		body := strings.Repeat("x", protocol.CoordMaxBodyBytes+1)
		_, err := h.svc.Send(ctx, h.run(0), protocol.CoordSendParams{ToRunID: string(h.run(1)), Body: body})
		if err == nil || err.Code != protocol.CodeInvalidParams ||
			err.Message != "coord.send: body exceeds 4096 bytes" {
			t.Fatalf("oversized body = %v, want the pinned CodeInvalidParams", err)
		}
	})

	t.Run("rate limit", func(t *testing.T) {
		h := newHarness(t, 2)
		h.peers.pair(h.run(0), h.run(1), "src/auth.go")
		for i := range sendBurst {
			if _, err := h.svc.Send(ctx, h.run(0), protocol.CoordSendParams{ToRunID: string(h.run(1)), Body: "ping"}); err != nil {
				t.Fatalf("send %d within the burst: %v", i, err)
			}
		}
		_, err := h.svc.Send(ctx, h.run(0), protocol.CoordSendParams{ToRunID: string(h.run(1)), Body: "ping"})
		if err == nil || err.Code != protocol.CodeConflict ||
			err.Message != "coord.send: rate limit exceeded (burst 5, 1 message per 5s)" {
			t.Fatalf("send past the burst = %v, want the pinned CodeConflict", err)
		}
		h.advance(sendRefill)
		if _, err := h.svc.Send(ctx, h.run(0), protocol.CoordSendParams{ToRunID: string(h.run(1)), Body: "ping"}); err != nil {
			t.Fatalf("send after a refill: %v", err)
		}
	})

	t.Run("full inbox", func(t *testing.T) {
		h := newHarness(t, 2)
		h.peers.pair(h.run(0), h.run(1), "src/auth.go")
		// The depth cap, not the rate, is under test: fill the inbox
		// through the store and let one send hit the wall.
		for range protocol.CoordMaxUnread {
			msg := &store.RunMessage{SessionID: h.session, FromRun: h.run(0), ToRun: h.run(1), Body: "filler"}
			if err := h.db.AppendRunMessage(ctx, msg, protocol.CoordMaxUnread); err != nil {
				t.Fatalf("seed mailbox: %v", err)
			}
		}
		_, err := h.svc.Send(ctx, h.run(0), protocol.CoordSendParams{ToRunID: string(h.run(1)), Body: "one too many"})
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

	if _, err := h.svc.Send(ctx, a, protocol.CoordSendParams{ToRunID: string(a), Body: "hi"}); err == nil || err.Code != protocol.CodeInvalidParams {
		t.Fatalf("self-send = %v, want CodeInvalidParams", err)
	}
	if err := h.db.UpdateRunStatus(ctx, b, domain.RunMerged, "", nil, nil); err != nil {
		t.Fatalf("finish run: %v", err)
	}
	_, err := h.svc.Send(ctx, a, protocol.CoordSendParams{ToRunID: string(b), Body: "hi"})
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

	if _, err := h.svc.Send(ctx, a, protocol.CoordSendParams{ToRunID: string(b), Body: "first"}); err != nil {
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
	if _, serr := h.svc.Send(ctx, a, protocol.CoordSendParams{ToRunID: string(b), Body: "second"}); serr != nil {
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
	if _, err := h.svc.Send(ctx, a, protocol.CoordSendParams{ToRunID: string(b), Body: "ping"}); err != nil {
		t.Fatalf("send across an active overlap: %v", err)
	}

	// The overlap clears here, and the notification never arrives: no event
	// is published and nothing calls the service for 45 minutes.
	h.peers.clear()
	h.advance(45 * time.Minute)

	if _, err := h.svc.Send(ctx, a, protocol.CoordSendParams{ToRunID: string(b), Body: "ping"}); err == nil || err.Code != protocol.CodeDenied {
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
	if _, err := h.svc.Send(ctx, a, protocol.CoordSendParams{ToRunID: string(b), Body: "ping"}); err != nil {
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
	if _, err := h.svc.Send(ctx, a, protocol.CoordSendParams{ToRunID: string(b), Body: "ping"}); err != nil {
		t.Fatalf("send inside the grace window: %v", err)
	}
	h.advance(2 * time.Minute)
	if _, err := h.svc.Send(ctx, a, protocol.CoordSendParams{ToRunID: string(b), Body: "ping"}); err == nil || err.Code != protocol.CodeDenied {
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
		_, err := h.svc.Send(ctx, spray, protocol.CoordSendParams{ToRunID: string(to), Body: "ping"})
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

// TestCrossSessionSendStampsBothTimelines covers the allowed cross-session
// case: the humans supervising the receiving run must see the incoming
// message on their own session's timeline, not just the sender's.
func TestCrossSessionSendStampsBothTimelines(t *testing.T) {
	h := newHarness(t, 1)
	ctx := context.Background()
	a := h.run(0)

	home, err := h.db.GetSession(ctx, h.session)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	other := &domain.Session{WorkspaceID: home.WorkspaceID, Name: "billing", BaseBranch: "main"}
	if cerr := h.db.CreateSession(ctx, other); cerr != nil {
		t.Fatalf("create session: %v", cerr)
	}
	b := &domain.Run{
		SessionID: other.ID,
		MemberID:  h.runs[0].MemberID,
		Task:      "task b",
		Harness:   "claude",
		Mode:      domain.LaunchTUI,
		Status:    domain.RunRunning,
	}
	if cerr := h.db.CreateRun(ctx, b); cerr != nil {
		t.Fatalf("create run: %v", cerr)
	}
	h.peers.pair(a, b.ID, "src/auth.go")

	timeline, err := h.bus.Subscribe(ctx, events.SubscribeOptions{
		Filter: events.Filter{Types: []events.Type{events.TypeTimeline}},
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer timeline.Close() //nolint:errcheck // test cleanup

	if _, serr := h.svc.Send(ctx, a, protocol.CoordSendParams{ToRunID: string(b.ID), Body: "hold off on auth.go"}); serr != nil {
		t.Fatalf("Send: %v", serr)
	}

	sessions := map[domain.SessionID]string{}
	for range 2 {
		select {
		case e := <-timeline.Events():
			p, ok := e.Payload.(events.TimelinePayload)
			if !ok || e.ActorID != h.runs[0].MemberID {
				t.Fatalf("timeline event = %+v, want a note attributed to the sender's owner", e)
			}
			sessions[e.SessionID] = p.Message
		case <-time.After(2 * time.Second):
			t.Fatalf("saw notes on %d timelines, want both sessions stamped", len(sessions))
		}
	}
	if !strings.Contains(sessions[h.session], "coordination message to run "+string(b.ID)) {
		t.Fatalf("sender-session note = %q, want the outgoing stamp", sessions[h.session])
	}
	if !strings.Contains(sessions[other.ID], "coordination message from run "+string(a)) {
		t.Fatalf("receiver-session note = %q, want the incoming stamp", sessions[other.ID])
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
	if _, err := h.svc.Send(ctx, a, protocol.CoordSendParams{ToRunID: string(b), Body: "ping"}); err != nil {
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
	if _, err := h.svc.Send(ctx, a, protocol.CoordSendParams{ToRunID: string(b), Body: "still there?"}); err != nil {
		t.Fatalf("send inside the witnessed grace window: %v", err)
	}
	h.advance(DefaultGrace + time.Minute)
	if _, err := h.svc.Send(ctx, a, protocol.CoordSendParams{ToRunID: string(b), Body: "ping"}); err == nil || err.Code != protocol.CodeDenied {
		t.Fatalf("send after the grace window = %v, want CodeDenied", err)
	}
}
