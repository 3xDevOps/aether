package sshd

import (
	"bufio"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/overlap"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

const streamEnd = "stream-end"

// endStream publishes the event replay stops at, which every member is
// streamed.
func (w *scopeWorld) endStream(t *testing.T) {
	t.Helper()
	if _, err := w.bus.Publish(t.Context(), events.Event{WorkspaceID: events.ServerScope,
		Payload: events.ServerUpdatePayload{Phase: events.ServerUpdateScheduled, Detail: streamEnd}}); err != nil {
		t.Fatal(err)
	}
}

// replay subscribes as who with replay from the start and returns every
// event's type and payload up to endStream's.
func (w *scopeWorld) replay(t *testing.T, who string) []string {
	t.Helper()
	client, err := w.e.dialWith(w.signers[who], nil)
	if err != nil {
		t.Fatalf("%s dial: %v", who, err)
	}
	t.Cleanup(func() { _ = client.Close() })
	pipe := openSubsystem(t, client, protocol.SubsystemEvents, nil)
	r := bufio.NewReader(pipe)
	if _, err := pipe.Write([]byte(`{"replay":true}` + "\n")); err != nil {
		t.Fatalf("%s subscribe: %v", who, err)
	}
	var ack protocol.SubscribeResponse
	readJSONLine(t, r, &ack)
	if !ack.OK {
		t.Fatalf("%s subscribe ack = %+v", who, ack)
	}
	var got []string
	for {
		var ev protocol.Event
		readJSONLine(t, r, &ev)
		if strings.Contains(string(ev.Payload), streamEnd) {
			return got
		}
		got = append(got, ev.Type+" "+string(ev.Payload))
	}
}

// A session whose extra repository is in a workspace a member does not
// reach shows in their lists, but none of its events reaches them: not on
// the event stream, its replay, workspace.timeline, changes.list,
// attention.list or run.overlaps.
func TestMultiRepoRunEventsNeedEveryWorkspace(t *testing.T) {
	t.Parallel()
	w := newScopeWorld(t)
	ctx := t.Context()
	const secret = "/repos/b/internal/secret_b.go"
	ua := w.runUA
	if _, err := w.e.db.UpdateRunStatus(ctx, w.runA.ID, domain.RunCompleted, "", "", nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.e.db.SetRunReview(ctx, w.runA.ID, domain.ReviewReady, ""); err != nil {
		t.Fatal(err)
	}
	for _, ev := range []events.Event{
		{WorkspaceID: w.wsA.ID, RunID: w.runA.ID, Payload: events.AgentEventPayload{Kind: events.AgentToolCall, Tool: "Read", Detail: secret}},
		{WorkspaceID: w.wsA.ID, RunID: w.runA.ID, Payload: events.RunStatusPayload{To: domain.RunNeedsAttention, Reason: "conflict in " + secret}},
		{WorkspaceID: w.wsA.ID, RunID: ua.ID, Payload: events.RunStatusPayload{To: domain.RunNeedsAttention, Reason: "ua-visible"}},
	} {
		if _, err := w.bus.Publish(ctx, ev); err != nil {
			t.Fatal(err)
		}
	}
	w.endStream(t)
	w.runOverlapsOf = fixedOverlaps{
		{RunID: w.runA.ID, With: []overlap.Peer{{RunID: ua.ID, MemberID: w.u.ID, Files: []string{"shared.go"}}}},
		{RunID: ua.ID, With: []overlap.Peer{{RunID: w.runA.ID, MemberID: w.a.ID, Files: []string{"shared.go"}}}},
	}

	for _, who := range []string{"S", "A", "U"} {
		full := who == "S"
		saw := strings.Join(w.replay(t, who), "\n")
		if strings.Contains(saw, secret) != full || !strings.Contains(saw, "ua-visible") {
			t.Errorf("%s event replay = %s; want runA's events %v and runUA's", who, saw, full)
		}
		var timeline json.RawMessage
		if err := w.call(t, who, protocol.MethodWorkspaceTimeline, protocol.WorkspaceTimelineParams{WorkspaceID: string(w.wsA.ID),
			Types: []string{string(events.TypeAgentEvent), string(events.TypeRunStatus)}}, &timeline); err != nil {
			t.Fatalf("%s workspace.timeline: %v", who, err)
		}
		if strings.Contains(string(timeline), secret) != full || !strings.Contains(string(timeline), "ua-visible") {
			t.Errorf("%s workspace.timeline = %s; want runA's events %v and runUA's", who, timeline, full)
		}
		var changes protocol.ChangesListResult
		if err := w.call(t, who, protocol.MethodChangesList, protocol.ChangesListParams{WorkspaceID: string(w.wsA.ID)}, &changes); err != nil {
			t.Fatalf("%s changes.list: %v", who, err)
		}
		runs := map[string]bool{}
		for _, c := range changes.Changes {
			runs[c.RunID] = true
		}
		if runs[string(w.runA.ID)] != full || !runs[string(ua.ID)] {
			t.Errorf("%s changes.list runs = %v; want runA %v and runUA", who, runs, full)
		}
		var overlaps protocol.RunOverlapsResult
		if err := w.call(t, who, protocol.MethodRunOverlaps, struct{}{}, &overlaps); err != nil {
			t.Fatalf("%s run.overlaps: %v", who, err)
		}
		if want := map[bool]int{true: 2, false: 0}[full]; len(overlaps.Overlaps) != want {
			t.Errorf("%s run.overlaps = %+v; want %d entries", who, overlaps.Overlaps, want)
		}
	}
	attention := func() []string {
		t.Helper()
		var res protocol.AttentionListResult
		if err := w.call(t, "A", protocol.MethodAttentionList, protocol.AttentionListParams{}, &res); err != nil {
			t.Fatalf("A attention.list: %v", err)
		}
		var ids []string
		for _, it := range res.Items {
			ids = append(ids, it.RunID)
		}
		return ids
	}
	if got := attention(); slices.Contains(got, string(w.runA.ID)) {
		t.Errorf("A attention.list = %v, want no runA while A does not reach its extra repository's workspace", got)
	}
	if err := w.e.db.GrantWorkspace(ctx, w.wsB.ID, w.a.ID, ""); err != nil {
		t.Fatal(err)
	}
	w.e.srv.accessChanged()
	if got := attention(); !slices.Contains(got, string(w.runA.ID)) {
		t.Errorf("A attention.list = %v, want runA once A reaches wsB", got)
	}
	if saw := strings.Join(w.replay(t, "A"), "\n"); !strings.Contains(saw, secret) {
		t.Errorf("A event replay = %s, want runA's events once A reaches wsB", saw)
	}
}

// member.device, member.merged and member.changed reach only members who
// see every member they name, on the stream and in workspace.timeline.
func TestMemberEventsReachOnlyThoseWhoSeeTheMembers(t *testing.T) {
	t.Parallel()
	w := newScopeWorld(t)
	ctx := t.Context()
	ids := w.e.store.(store.IdentityStore)
	device := func(m *domain.Member) *domain.Device {
		t.Helper()
		subject := "gh-" + string(m.ID)
		if err := ids.BindIdentity(ctx, &domain.Identity{Member: m.ID, Provider: "github", Subject: subject, Login: m.DisplayName}); err != nil {
			t.Fatal(err)
		}
		d := &domain.Device{Member: m.ID, Provider: "github", Subject: subject, Login: m.DisplayName,
			Credential: "cred-" + string(m.ID), Status: domain.DeviceApproved}
		if err := ids.RegisterDevice(ctx, d); err != nil {
			t.Fatal(err)
		}
		return d
	}
	devB, devU := device(w.b), device(w.u)
	w.e.srv.publishDevice(ctx, w.b.ID, devB.ID, domain.DeviceRevoked)
	w.e.srv.publishDevice(ctx, w.a.ID, devU.ID, domain.DeviceRevoked)
	for _, ev := range []events.Event{
		{WorkspaceID: events.ServerScope, ActorID: w.b.ID, Payload: events.MemberMergedPayload{MergeID: "mrg-b", SurvivorID: w.b.ID, AbsorbedID: "mem-gone"}},
		{WorkspaceID: w.wsA.ID, ActorID: w.b.ID, Payload: events.MemberChangedPayload{MemberID: w.b.ID, DisplayName: "renamed-b"}},
		{WorkspaceID: w.wsA.ID, ActorID: w.u.ID, Payload: events.MemberChangedPayload{MemberID: w.u.ID, DisplayName: "renamed-u"}},
	} {
		if _, err := w.bus.Publish(ctx, ev); err != nil {
			t.Fatal(err)
		}
	}
	w.endStream(t)
	named := []string{string(devB.ID), string(devU.ID), "mrg-b", "renamed-b", "renamed-u"}
	for who, want := range map[string][]string{
		"S": named,
		"A": {string(devU.ID), "renamed-u"},
		"U": {string(devU.ID), "renamed-u"},
		"N": {},
	} {
		saw := strings.Join(w.replay(t, who), "\n")
		for _, name := range named {
			if wanted := slices.Contains(want, name); strings.Contains(saw, name) != wanted {
				t.Errorf("%s stream shows %s = %v, want %v: %s", who, name, !wanted, wanted, saw)
			}
		}
		if who == "N" {
			continue
		}
		var timeline json.RawMessage
		if err := w.call(t, who, protocol.MethodWorkspaceTimeline, protocol.WorkspaceTimelineParams{WorkspaceID: string(w.wsA.ID)}, &timeline); err != nil {
			t.Fatalf("%s workspace.timeline: %v", who, err)
		}
		for _, name := range []string{"renamed-b", "renamed-u"} {
			if wanted := slices.Contains(want, name); strings.Contains(string(timeline), name) != wanted {
				t.Errorf("%s workspace.timeline shows %s = %v, want %v: %s", who, name, !wanted, wanted, timeline)
			}
		}
	}
}

// An audit entry's workspace is the one the server resolved for the call,
// never one the caller put in params: N's successful account.revoke naming
// B's workspace and run lands in no workspace, and A's grant in wsA lands
// in wsA.
func TestAuditWorkspaceComesFromTheServer(t *testing.T) {
	t.Parallel()
	w := newScopeWorld(t)
	params := map[string]string{"member_id": string(w.u.ID), "workspace_id": string(w.wsB.ID), "run_id": string(w.runB.ID)}
	if err := w.call(t, "N", protocol.MethodAccountRevoke, params, nil); err != nil {
		t.Fatalf("N account.revoke: %v", err)
	}
	if err := w.call(t, "A", protocol.MethodWorkspaceAccessGrant, protocol.WorkspaceAccessGrantParams{
		WorkspaceID: string(w.wsA.ID), MemberIDs: []string{string(w.u.ID)},
	}, nil); err != nil {
		t.Fatalf("A workspace.access.grant: %v", err)
	}
	as := w.e.store.(store.AuditStore)
	for member, want := range map[domain.MemberID]domain.WorkspaceID{w.n.ID: "", w.a.ID: w.wsA.ID} {
		entries, err := as.ListAudit(t.Context(), store.AuditQuery{Member: member, Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 1 || entries[0].WorkspaceID != string(want) {
			t.Errorf("%s's audit entries = %+v, want one in workspace %q", member, entries, want)
		}
	}
	var res protocol.AuditListResult
	if err := w.call(t, "B", protocol.MethodAuditList, protocol.AuditListParams{}, &res); err != nil {
		t.Fatal(err)
	}
	for _, en := range res.Entries {
		if en.MemberID == string(w.n.ID) {
			t.Errorf("B's audit view shows N's entry %+v", en)
		}
	}
}
