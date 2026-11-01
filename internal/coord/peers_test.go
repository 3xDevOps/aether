package coord

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

func wantDenied(t *testing.T, err *protocol.Error, reason string) {
	t.Helper()
	if err == nil || err.Code != protocol.CodeDenied || !strings.Contains(err.Message, reason) ||
		!strings.Contains(err.Message, "aether-internal status") {
		t.Fatalf("send = %v, want CodeDenied naming %q and pointing at aether-internal status", err, reason)
	}
}

// TestWorkspaceRouteFollowsTheMessagePermission covers the permission
// behind the workspace route: protection admits only the owner's runs and
// admins, a viewer-owned run reaches nobody on any route, and the sender
// owner's role is re-read on every send.
func TestWorkspaceRouteFollowsTheMessagePermission(t *testing.T) {
	h := newHarness(t, 4)
	ctx := context.Background()
	mine, sibling, theirs, adminRun := h.run(0), h.run(1), h.run(2), h.run(3)
	h.ownBy(1, h.runs[0].MemberID, true)
	h.isolate(2)
	h.ownBy(3, h.member(domain.RoleAdmin).ID, false)

	if _, err := h.svc.Send(ctx, mine, sendParams(sibling, "same owner")); err != nil {
		t.Fatalf("send to the owner's own protected session: %v", err)
	}
	_, err := h.svc.Send(ctx, mine, sendParams(theirs, "other member"))
	wantDenied(t, err, "session is protected: only its owner or an admin may message it")
	if _, err = h.svc.Send(ctx, adminRun, sendParams(theirs, "admin")); err != nil {
		t.Fatalf("send from an admin-owned run to a protected session: %v", err)
	}
	st, err := h.svc.Status(ctx, mine)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	for _, p := range st.Peers {
		if p.RunID == string(theirs) {
			t.Fatalf("status lists the protected session of another member: %+v", st.Peers)
		}
	}
	if len(st.Peers) != 2 || st.PeerTotal != 2 {
		t.Fatalf("status peers = %+v, want the sibling and the admin's run", st.Peers)
	}

	owner, gerr := h.db.GetMember(ctx, h.runs[0].MemberID)
	if gerr != nil {
		t.Fatalf("GetMember: %v", gerr)
	}
	owner.Role = domain.RoleViewer
	if uerr := h.db.UpdateMember(ctx, owner); uerr != nil {
		t.Fatalf("demote owner: %v", uerr)
	}
	// A viewer-owned run reaches nobody, not even over an overlap.
	h.peers.pair(mine, theirs, "src/auth.go")
	viewer := fmt.Sprintf("member %s (viewer) may not message it: messaging another session requires the collaborator role", owner.ID)
	for _, target := range []domain.RunID{sibling, theirs, adminRun} {
		h.advance(sendRefill)
		_, err = h.svc.Send(ctx, mine, sendParams(target, "after demotion"))
		wantDenied(t, err, viewer)
	}
	if st, err = h.svc.Status(ctx, mine); err != nil || len(st.Peers) != 0 || st.PeerTotal != 0 {
		t.Fatalf("viewer status = %+v (err %v), want no peers", st.Peers, err)
	}
}

// TestAnyRunMessagesARunInAnotherWorkspace expects a run to message a live
// run in another workspace by ID, in both directions, without listing it.
func TestAnyRunMessagesARunInAnotherWorkspace(t *testing.T) {
	h := newHarness(t, 1)
	ctx := context.Background()
	ws := &domain.Workspace{Name: "other", BaseBranch: domain.DefaultBaseBranch}
	if err := h.db.CreateWorkspace(ctx, ws); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	foreign := &domain.Run{WorkspaceID: ws.ID, MemberID: h.runs[0].MemberID, Task: "foreign",
		Harness: "claude", Mode: domain.LaunchTUI, Status: domain.RunRunning}
	if err := h.db.CreateRun(ctx, foreign); err != nil {
		t.Fatalf("create run: %v", err)
	}
	local := h.run(0)
	h.peers.pair(local, foreign.ID, "src/auth.go")

	for _, dir := range [][2]domain.RunID{{local, foreign.ID}, {foreign.ID, local}} {
		h.advance(sendRefill)
		if _, err := h.svc.Send(ctx, dir[0], sendParams(dir[1], "across")); err != nil {
			t.Fatalf("send %s -> %s: %v", dir[0], dir[1], err)
		}
		h.advance(sendRefill)
		asked, err := h.svc.Ask(ctx, dir[0], protocol.CoordAskParams{ToRunID: string(dir[1]), Body: "across?", IdempotencyKey: "ask-" + string(dir[0])})
		if err != nil {
			t.Fatalf("ask %s -> %s: %v", dir[0], dir[1], err)
		}
		h.advance(sendRefill)
		if _, err := h.svc.Reply(ctx, dir[1], protocol.CoordReplyParams{QuestionID: asked.QuestionID, Body: "yes", IdempotencyKey: "reply-" + string(dir[0])}); err != nil {
			t.Fatalf("reply %s -> %s: %v", dir[1], dir[0], err)
		}
	}
	st, err := h.svc.Status(ctx, local)
	if err != nil || len(st.Peers) != 0 || st.PeerTotal != 0 {
		t.Fatalf("status = %+v (err %v), want the foreign run unlisted", st, err)
	}
}

// TestSendReceiptCarriesRecipientStatus shows a sender that its message is
// queued for a run that is waiting on its user, on first send and retry.
func TestSendReceiptCarriesRecipientStatus(t *testing.T) {
	h := newHarness(t, 2)
	ctx := context.Background()
	a, b := h.run(0), h.run(1)
	if _, err := h.db.UpdateRunStatus(ctx, b, domain.RunNeedsAttention, "waiting for input", "", nil, nil); err != nil {
		t.Fatalf("update status: %v", err)
	}
	params := sendParams(b, "ping")
	for _, attempt := range []string{"send", "retry"} {
		sent, err := h.svc.Send(ctx, a, params)
		if err != nil || sent.RecipientStatus != string(domain.RunNeedsAttention) {
			t.Fatalf("%s receipt = %+v (err %v), want recipient_status needs-attention", attempt, sent, err)
		}
	}
	asked, err := h.svc.Ask(ctx, a, protocol.CoordAskParams{ToRunID: string(b), Body: "ready?", IdempotencyKey: "ask-status"})
	if err != nil || asked.RecipientStatus != string(domain.RunNeedsAttention) {
		t.Fatalf("ask receipt = %+v (err %v), want recipient_status needs-attention", asked, err)
	}
}

// Mail to a child run a person controls waits for them to let go, and the
// sender is told so.
func TestReceiptsAndStatusShowMailHeldForAPerson(t *testing.T) {
	h := newHarness(t, 2)
	ctx := context.Background()
	a, b := h.run(0), h.run(1)
	h.svc.cfg.Children = parentOf{child: b, parent: a}
	h.svc.cfg.Controlled = func(run domain.RunID) bool { return run == b }
	sent, err := h.svc.Send(ctx, a, sendParams(b, "ping"))
	if err != nil || sent.RecipientStatus != protocol.CoordSentStateHeld {
		t.Fatalf("send receipt = %+v (err %v), want recipient_status held", sent, err)
	}
	asked, err := h.svc.Ask(ctx, a, protocol.CoordAskParams{ToRunID: string(b), Body: "ready?", IdempotencyKey: "ask-held"})
	if err != nil || asked.RecipientStatus != protocol.CoordSentStateHeld {
		t.Fatalf("ask receipt = %+v (err %v), want recipient_status held", asked, err)
	}
	st, err := h.svc.Status(ctx, a)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	i := slices.IndexFunc(st.Peers, func(p protocol.CoordPeer) bool { return p.RunID == string(b) })
	if i < 0 || st.Peers[i].LastSent == nil || st.Peers[i].LastSent.State != protocol.CoordSentStateHeld {
		t.Fatalf("status peers = %+v, want last_sent to the held subsession held", st.Peers)
	}
}

// TestStatusShowsWhatHappenedToTheLastMessageSent follows the newest message
// a run sent each peer from sent through delivered to acked, back to sent
// when a message stamped earlier commits later, and to retired for a peer
// released before it acknowledged.
func TestStatusShowsWhatHappenedToTheLastMessageSent(t *testing.T) {
	h := newHarness(t, 4)
	ctx := context.Background()
	a, b, c, silent := h.run(0), h.run(1), h.run(2), h.run(3)
	lastSent := func() map[string]*protocol.CoordSentMessage {
		t.Helper()
		st, err := h.svc.Status(ctx, a)
		if err != nil {
			t.Fatalf("Status: %v", err)
		}
		out := make(map[string]*protocol.CoordSentMessage, len(st.Peers))
		for _, p := range st.Peers {
			out[p.RunID] = p.LastSent
		}
		return out
	}
	if _, err := h.svc.Send(ctx, a, sendParams(b, "first")); err != nil {
		t.Fatalf("send first: %v", err)
	}
	second, err := h.svc.Send(ctx, a, sendParams(b, "second"))
	if err != nil {
		t.Fatalf("send second: %v", err)
	}
	if _, err = h.svc.Send(ctx, b, sendParams(a, "the other direction")); err != nil {
		t.Fatalf("send b -> a: %v", err)
	}
	got := lastSent()
	if m := got[string(b)]; m == nil || m.MessageID != second.MessageID || m.State != protocol.CoordSentStateSent ||
		m.CreatedAt == "" || m.DeliveredAt != "" || m.AckedAt != "" {
		t.Fatalf("last_sent to b = %+v, want %s sent", m, second.MessageID)
	}
	if m := got[string(silent)]; m != nil {
		t.Fatalf("last_sent to a peer never written to = %+v, want none", m)
	}

	batch, err := h.svc.Inbox(ctx, b, protocol.CoordInboxParams{})
	if err != nil || len(batch.Messages) != 2 {
		t.Fatalf("b inbox = %+v (err %v), want both messages", batch, err)
	}
	if m := lastSent()[string(b)]; m == nil || m.State != protocol.CoordSentStateDelivered || m.DeliveredAt == "" || m.AckedAt != "" {
		t.Fatalf("last_sent after b read = %+v, want delivered", m)
	}
	if _, err = h.svc.Inbox(ctx, b, protocol.CoordInboxParams{AckToken: batch.AckToken}); err != nil {
		t.Fatalf("b ack: %v", err)
	}
	if m := lastSent()[string(b)]; m == nil || m.State != protocol.CoordSentStateAcked || m.DeliveredAt == "" || m.AckedAt == "" {
		t.Fatalf("last_sent after b acked = %+v, want acked", m)
	}
	late := &store.RunMessage{WorkspaceID: h.workspace, FromRun: a, ToRun: b, Body: "stamped before second, committed after",
		CreatedAt: time.Now().Add(-time.Hour)}
	if aerr := h.db.AppendRunMessage(ctx, late, protocol.CoordMaxUnread); aerr != nil {
		t.Fatalf("AppendRunMessage: %v", aerr)
	}
	if m := lastSent()[string(b)]; m == nil || m.MessageID != late.ID || m.State != protocol.CoordSentStateSent {
		t.Fatalf("last_sent after a late commit = %+v, want %s sent", m, late.ID)
	}

	if _, err = h.svc.Send(ctx, a, sendParams(c, "never read")); err != nil {
		t.Fatalf("send c: %v", err)
	}
	if rerr := h.db.RetireRunMessages(ctx, c); rerr != nil {
		t.Fatalf("RetireRunMessages: %v", rerr)
	}
	if m := lastSent()[string(c)]; m == nil || m.State != protocol.CoordSentStateRetired || m.DeliveredAt != "" || m.AckedAt != "" {
		t.Fatalf("last_sent to released c = %+v, want retired", m)
	}
}

// TestStatusBoundsAndDedupsAllThreeRoutes lists lineage, radar, and
// workspace peers that overlap one another past the status bound: each run
// appears once, in route order, the most recently active workspace run
// first even though it was created earliest, and the total counts each
// distinct peer once.
func TestStatusBoundsAndDedupsAllThreeRoutes(t *testing.T) {
	n := protocol.CoordMaxStatusPeers + 8
	h := newHarness(t, n+1)
	ctx := context.Background()
	self := h.run(0)
	h.svc.cfg.Children = familyOf{self, h.run(1), h.run(2), h.run(3)}
	h.peers.hub(self, []domain.RunID{h.run(3), h.run(4), h.run(5)}, "src/auth.go")
	recent := 6
	h.runs[recent].Task = strings.Repeat("é", protocol.CoordMaxStatusTaskBytes)
	if err := h.db.UpdateRun(ctx, h.runs[recent]); err != nil {
		t.Fatalf("update task: %v", err)
	}
	if _, err := h.db.UpdateRunStatus(ctx, h.run(recent), domain.RunNeedsAttention, "needs input: "+strings.Repeat("é", protocol.CoordMaxStatusTaskBytes), domain.ActivityNeedsYou, nil, nil); err != nil {
		t.Fatalf("update status: %v", err)
	}

	st, err := h.svc.Status(ctx, self)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(st.Peers) != protocol.CoordMaxStatusPeers || st.PeerTotal != n || !st.PeersTruncated {
		t.Fatalf("status = %d peers, total %d, truncated %v; want %d, %d, true",
			len(st.Peers), st.PeerTotal, st.PeersTruncated, protocol.CoordMaxStatusPeers, n)
	}
	seen := make(map[string]bool, len(st.Peers))
	for i, p := range st.Peers {
		if seen[p.RunID] {
			t.Fatalf("peer %s listed twice", p.RunID)
		}
		seen[p.RunID] = true
		want := protocol.CoordPeerWorkspace
		switch {
		case i < 3:
			want = protocol.CoordPeerLineage
		case i < 5:
			want = protocol.CoordPeerActive
		}
		if p.State != want {
			t.Fatalf("peer %d = %+v, want state %q", i, p, want)
		}
	}
	if p := st.Peers[5]; p.RunID != string(h.run(recent)) || p.RunStatus != string(domain.RunNeedsAttention) ||
		p.TaskBytes != 2*protocol.CoordMaxStatusTaskBytes || !p.TaskTruncated ||
		len(p.Task) > protocol.CoordMaxStatusTaskBytes || !utf8.ValidString(p.Task) ||
		p.Activity != string(domain.ActivityNeedsYou) || !p.ReasonTruncated || len(p.Reason) > protocol.CoordMaxStatusTaskBytes || !utf8.ValidString(p.Reason) {
		t.Fatalf("first workspace peer = %+v, want the most recently active session %s with its task and reason bounded", p, h.run(recent))
	}
}

// TestAdminsOnlyLimitsTheWorkspaceRoute covers message-others=admins_only:
// a collaborator's run reaches only its owner's runs over the workspace
// route, an admin-owned run reaches every run, and the lineage and overlap
// routes stay open.
func TestAdminsOnlyLimitsTheWorkspaceRoute(t *testing.T) {
	h := newHarness(t, 6)
	ctx := context.Background()
	mine, sibling, other, child, overlap, adminRun := h.run(0), h.run(1), h.run(2), h.run(3), h.run(4), h.run(5)
	for _, i := range []int{2, 3, 4} {
		h.ownBy(i, h.member(domain.RoleCollaborator).ID, false)
	}
	h.ownBy(5, h.member(domain.RoleAdmin).ID, false)
	if err := h.db.SetWorkspaceSteerOthers(ctx, h.workspace, domain.SteerOthersAdminsOnly); err != nil {
		t.Fatalf("set steer_others: %v", err)
	}
	h.svc.cfg.Children = familyOf{mine, child}
	h.peers.pair(mine, overlap, "src/auth.go")

	_, err := h.svc.Send(ctx, mine, sendParams(other, "other member"))
	wantDenied(t, err, "workspace restricts messaging others' sessions to their owner and admins")
	for _, target := range []domain.RunID{sibling, child, overlap} {
		h.advance(sendRefill)
		if _, err = h.svc.Send(ctx, mine, sendParams(target, "allowed")); err != nil {
			t.Fatalf("send to %s under admins_only: %v", target, err)
		}
	}
	if _, err = h.svc.Send(ctx, adminRun, sendParams(other, "admin")); err != nil {
		t.Fatalf("send from an admin-owned run under admins_only: %v", err)
	}
	st, serr := h.svc.Status(ctx, mine)
	if serr != nil {
		t.Fatalf("Status: %v", serr)
	}
	listed := make(map[string]bool, len(st.Peers))
	for _, p := range st.Peers {
		listed[p.RunID] = true
	}
	if len(listed) != 3 || st.PeerTotal != 3 || !listed[string(sibling)] || !listed[string(child)] || !listed[string(overlap)] {
		t.Fatalf("status peers = %+v (total %d), want the sibling, subsession and overlap runs", st.Peers, st.PeerTotal)
	}
}

// familyOf is a ChildService whose members are all parent and child to one
// another.
type familyOf []domain.RunID

func (f familyOf) Capabilities(context.Context, domain.RunID) ([]string, error) { return nil, nil }

func (f familyOf) HandleAgent(context.Context, domain.RunID, string, json.RawMessage) (any, error) {
	return nil, nil
}

func (f familyOf) Related(_ context.Context, a, b domain.RunID) (bool, error) {
	return a != b && slices.Contains(f, a) && slices.Contains(f, b), nil
}

func (f familyOf) Lineage(_ context.Context, run domain.RunID) ([]domain.RunID, error) {
	if !slices.Contains(f, run) {
		return nil, nil
	}
	return slices.DeleteFunc(slices.Clone(f), func(id domain.RunID) bool { return id == run }), nil
}

func (f familyOf) ReportRecipient(context.Context, domain.RunID) (domain.RunID, error) {
	return "", nil
}

func (f familyOf) ValidateReport(context.Context, domain.RunID, store.CoordOutcome) error { return nil }

func (f familyOf) NeedsInput(context.Context, domain.RunID, string) {}
