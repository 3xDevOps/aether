package coord

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
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
	stub := &missionTransportStub{}
	h := newHarness(t, 4, func(c *Config) { c.Mission = stub })
	ctx := context.Background()
	mine, sibling, theirs, adminRun := h.run(0), h.run(1), h.run(2), h.run(3)
	h.ownBy(1, h.runs[0].MemberID, true)
	h.isolate(2)
	h.ownBy(3, h.member(domain.RoleAdmin).ID, false)

	if _, err := h.svc.Send(ctx, mine, sendParams(sibling, "same owner")); err != nil {
		t.Fatalf("send to the owner's own protected run: %v", err)
	}
	_, err := h.svc.Send(ctx, mine, sendParams(theirs, "other member"))
	wantDenied(t, err, "run is protected: only its owner or an admin may message it")
	if _, err = h.svc.Send(ctx, adminRun, sendParams(theirs, "admin")); err != nil {
		t.Fatalf("send from an admin-owned run to a protected run: %v", err)
	}
	st, err := h.svc.Status(ctx, mine)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	for _, p := range st.Peers {
		if p.RunID == string(theirs) {
			t.Fatalf("status lists the protected run of another member: %+v", st.Peers)
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
	// A viewer-owned run reaches nobody, not even over a swarm or an overlap.
	stub.mission = []domain.RunID{mine, sibling}
	h.peers.pair(mine, theirs, "src/auth.go")
	viewer := fmt.Sprintf("member %s (viewer) may not message it: messaging another run requires the collaborator role", owner.ID)
	for _, target := range []domain.RunID{sibling, theirs, adminRun} {
		h.advance(sendRefill)
		_, err = h.svc.Send(ctx, mine, sendParams(target, "after demotion"))
		wantDenied(t, err, viewer)
	}
	if st, err = h.svc.Status(ctx, mine); err != nil || len(st.Peers) != 0 || st.PeerTotal != 0 {
		t.Fatalf("viewer status = %+v (err %v), want no peers", st.Peers, err)
	}
}

// TestRoutesNeverCrossWorkspaces offers a run in another workspace through
// every route at once - an overlap edge, a shared swarm, and the same
// owner - and expects each direction refused and unlisted.
func TestRoutesNeverCrossWorkspaces(t *testing.T) {
	stub := &missionTransportStub{}
	h := newHarness(t, 1, func(c *Config) { c.Mission = stub })
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
	stub.mission = []domain.RunID{local, foreign.ID}
	h.peers.pair(local, foreign.ID, "src/auth.go")

	for _, dir := range [][2]domain.RunID{{local, foreign.ID}, {foreign.ID, local}} {
		_, err := h.svc.Send(ctx, dir[0], sendParams(dir[1], "across"))
		wantDenied(t, err, "it is in a different workspace")
		_, err = h.svc.Ask(ctx, dir[0], protocol.CoordAskParams{ToRunID: string(dir[1]), Body: "across?", IdempotencyKey: "ask-" + string(dir[0])})
		wantDenied(t, err, "it is in a different workspace")
	}
	st, err := h.svc.Status(ctx, local)
	if err != nil || len(st.Peers) != 0 || st.PeerTotal != 0 {
		t.Fatalf("status = %+v (err %v), want the foreign run unlisted", st, err)
	}
}

// TestSwarmAndIndependentRunsMessageEachOther covers the routes between a
// swarm and a run launched on its own, and that an integrator reaches more
// workers than the peer cap allows conversations without spending a slot.
func TestSwarmAndIndependentRunsMessageEachOther(t *testing.T) {
	stub := &missionTransportStub{}
	workers := maxPeers + 2
	h := newHarness(t, workers+maxPeers+2, func(c *Config) { c.Mission = stub })
	ctx := context.Background()
	integrator, independent := h.run(0), h.run(workers+1)
	stub.integrator = integrator
	stub.mission = []domain.RunID{integrator}
	for i := 1; i <= workers; i++ {
		stub.mission = append(stub.mission, h.run(i))
	}
	worker := h.run(1)

	if _, err := h.svc.Send(ctx, worker, sendParams(independent, "I am about to edit auth.go")); err != nil {
		t.Fatalf("worker -> independent run: %v", err)
	}
	if _, err := h.svc.Send(ctx, independent, sendParams(worker, "so am I")); err != nil {
		t.Fatalf("independent run -> worker: %v", err)
	}
	for i := 1; i <= workers; i++ {
		h.advance(sendRefill)
		if _, err := h.svc.Send(ctx, integrator, sendParams(h.run(i), "status?")); err != nil {
			t.Fatalf("integrator -> worker %d of %d: %v", i, workers, err)
		}
	}
	for i := 0; i < maxPeers; i++ {
		h.advance(sendRefill)
		if _, err := h.svc.Send(ctx, integrator, sendParams(h.run(workers+1+i), "heads up")); err != nil {
			t.Fatalf("integrator -> independent run %d after messaging every worker: %v", i, err)
		}
	}
	h.advance(sendRefill)
	_, err := h.svc.Send(ctx, integrator, sendParams(h.run(workers+1+maxPeers), "one too many"))
	if err == nil || err.Code != protocol.CodeConflict || !strings.Contains(err.Message, "limit of 8 coordination peers") {
		t.Fatalf("send past the peer cap = %v, want the peer limit", err)
	}
	// A worker that leaves the swarm but stays live is still an existing
	// conversation for the integrator at its cap.
	stub.mission = slices.DeleteFunc(stub.mission, func(id domain.RunID) bool { return id == worker })
	h.advance(sendRefill)
	if _, err := h.svc.Send(ctx, integrator, sendParams(worker, "still there?")); err != nil {
		t.Fatalf("integrator -> former worker at the peer cap: %v", err)
	}
}

// TestSendReceiptCarriesRecipientStatus shows a sender that its message is
// queued for a run that is waiting on its user, on first send and retry.
func TestSendReceiptCarriesRecipientStatus(t *testing.T) {
	h := newHarness(t, 2)
	ctx := context.Background()
	a, b := h.run(0), h.run(1)
	if err := h.db.UpdateRunStatus(ctx, b, domain.RunNeedsAttention, "waiting for input", "", nil, nil); err != nil {
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

// TestStatusBoundsAndDedupsAllThreeRoutes lists mission, radar, and
// workspace peers that overlap one another past the status bound: each run
// appears once, in route order, the most recently active workspace run
// first even though it was created earliest, and the total counts each
// distinct peer once.
func TestStatusBoundsAndDedupsAllThreeRoutes(t *testing.T) {
	stub := &missionTransportStub{}
	n := protocol.CoordMaxStatusPeers + 8
	h := newHarness(t, n+1, func(c *Config) { c.Mission = stub })
	ctx := context.Background()
	self := h.run(0)
	stub.mission = []domain.RunID{self, h.run(1), h.run(2), h.run(3)}
	h.peers.hub(self, []domain.RunID{h.run(3), h.run(4), h.run(5)}, "src/auth.go")
	recent := 6
	h.runs[recent].Task = strings.Repeat("é", protocol.CoordMaxStatusTaskBytes)
	if err := h.db.UpdateRun(ctx, h.runs[recent]); err != nil {
		t.Fatalf("update task: %v", err)
	}
	if err := h.db.UpdateRunStatus(ctx, h.run(recent), domain.RunNeedsAttention, "", "", nil, nil); err != nil {
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
			want = protocol.CoordPeerMission
		case i < 5:
			want = protocol.CoordPeerActive
		}
		if p.State != want {
			t.Fatalf("peer %d = %+v, want state %q", i, p, want)
		}
	}
	if p := st.Peers[5]; p.RunID != string(h.run(recent)) || p.RunStatus != string(domain.RunNeedsAttention) ||
		p.TaskBytes != 2*protocol.CoordMaxStatusTaskBytes || !p.TaskTruncated ||
		len(p.Task) > protocol.CoordMaxStatusTaskBytes || !utf8.ValidString(p.Task) {
		t.Fatalf("first workspace peer = %+v, want the most recently active run %s with its task bounded", p, h.run(recent))
	}
}

// TestAdminsOnlyLimitsTheWorkspaceRoute covers message-others=admins_only:
// a collaborator's run reaches only its owner's runs over the workspace
// route, an admin-owned run reaches every run, and the swarm and overlap
// routes stay open.
func TestAdminsOnlyLimitsTheWorkspaceRoute(t *testing.T) {
	stub := &missionTransportStub{}
	h := newHarness(t, 6, func(c *Config) { c.Mission = stub })
	ctx := context.Background()
	mine, sibling, other, swarm, overlap, adminRun := h.run(0), h.run(1), h.run(2), h.run(3), h.run(4), h.run(5)
	for _, i := range []int{2, 3, 4} {
		h.ownBy(i, h.member(domain.RoleCollaborator).ID, false)
	}
	h.ownBy(5, h.member(domain.RoleAdmin).ID, false)
	if err := h.db.SetWorkspaceSteerOthers(ctx, h.workspace, domain.SteerOthersAdminsOnly); err != nil {
		t.Fatalf("set steer_others: %v", err)
	}
	stub.mission = []domain.RunID{mine, swarm}
	h.peers.pair(mine, overlap, "src/auth.go")

	_, err := h.svc.Send(ctx, mine, sendParams(other, "other member"))
	wantDenied(t, err, "workspace restricts messaging others' runs to their owner and admins")
	for _, target := range []domain.RunID{sibling, swarm, overlap} {
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
	if len(listed) != 3 || st.PeerTotal != 3 || !listed[string(sibling)] || !listed[string(swarm)] || !listed[string(overlap)] {
		t.Fatalf("status peers = %+v (total %d), want the sibling, swarm and overlap runs", st.Peers, st.PeerTotal)
	}
}

// TestStatusAtThePeerCapListsOnlyReachableRuns fills the sender's peer
// slots and expects status to list only its swarm peer and the runs it
// already wrote to, exactly the runs send still accepts.
func TestStatusAtThePeerCapListsOnlyReachableRuns(t *testing.T) {
	stub := &missionTransportStub{}
	n := maxPeers + 4
	h := newHarness(t, n, func(c *Config) { c.Mission = stub })
	ctx := context.Background()
	self := h.run(0)
	stub.mission = []domain.RunID{self, h.run(1)}
	h.peers.pair(self, h.run(n-2), "src/auth.go")
	for i := 2; i < 2+maxPeers; i++ {
		h.advance(sendRefill)
		if _, err := h.svc.Send(ctx, self, sendParams(h.run(i), "heads up")); err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
	}

	st, err := h.svc.Status(ctx, self)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(st.Peers) != maxPeers+1 || st.PeerTotal != maxPeers+1 || st.PeersTruncated {
		t.Fatalf("status = %d peers, total %d; want the swarm peer and %d conversations", len(st.Peers), st.PeerTotal, maxPeers)
	}
	listed := make(map[string]bool, len(st.Peers))
	for _, p := range st.Peers {
		listed[p.RunID] = true
	}
	for i := 1; i < n; i++ {
		h.advance(sendRefill)
		_, serr := h.svc.Send(ctx, self, sendParams(h.run(i), "again"))
		if listed[string(h.run(i))] != (serr == nil) {
			t.Fatalf("run %d listed=%v but send error=%v", i, listed[string(h.run(i))], serr)
		}
	}
}
