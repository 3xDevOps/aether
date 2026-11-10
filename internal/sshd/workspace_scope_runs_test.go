package sshd

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/evidence"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
	"github.com/3xDevOps/Aether/internal/watch"
)

// Lists by workspace leave out every mention of a session the caller does
// not reach in all its workspaces, and of a pull request in a repository
// only others reach: coordination messages, evidence, the roster's
// watched sessions and wait conditions.
func TestWorkspaceListsLeaveOutSessionsWithAnUnreachableRepo(t *testing.T) {
	t.Parallel()
	w := newScopeWorld(t)
	ctx := t.Context()
	db := w.e.db
	runA, ua := w.runA, w.runUA
	if err := db.SetWorkspaceOrigin(ctx, w.wsB.ID, "https://github.com/acme/b.git"); err != nil {
		t.Fatal(err)
	}
	for _, m := range []*store.RunMessage{
		{WorkspaceID: w.wsA.ID, FromRun: runA.ID, ToRun: ua.ID, Body: "from two repos", Kind: store.RunMessageKindMessage, CreatedAt: time.Now()},
		{WorkspaceID: w.wsA.ID, FromRun: ua.ID, ToRun: runA.ID, Body: "to two repos", Kind: store.RunMessageKindMessage, CreatedAt: time.Now()},
		{WorkspaceID: w.wsA.ID, FromRun: ua.ID, ToRun: ua.ID, Body: "note to self", Kind: store.RunMessageKindMessage, CreatedAt: time.Now()},
	} {
		if err := db.AppendRunMessage(ctx, m, 100); err != nil {
			t.Fatal(err)
		}
	}
	w.e.srv.cfg.Services.Evidence = &handlerEvidenceService{list: protocol.EvidencePacketListResult{Packets: []protocol.EvidencePacket{
		protocol.EvidencePacketFromStore(handlerPacket("p-a", w.wsA.ID, runA.ID, w.a.ID)),
		protocol.EvidencePacketFromStore(handlerPacket("p-ua", w.wsA.ID, ua.ID, w.u.ID)),
	}}}
	w.e.srv.cfg.Services.Watches = fakeRunWaits{waits: []protocol.WaitCondition{
		{ID: "wat-run", Spec: "run:" + string(runA.ID) + ":finished", Kind: protocol.WaitKindRunFinished, RunID: string(runA.ID), State: protocol.WaitPending},
		{ID: "wat-b", Spec: "pr:acme/b#7:merged", Kind: protocol.WaitKindPRMerged, Repo: "acme/b", Number: 7, State: protocol.WaitPending},
		{ID: "wat-x", Spec: "pr:acme/x#1:merged", Kind: protocol.WaitKindPRMerged, Repo: "acme/x", Number: 1, State: protocol.WaitPending},
	}}
	for _, r := range []*domain.Run{runA, ua} {
		if _, err := w.bus.Publish(ctx, events.Event{WorkspaceID: w.wsA.ID, RunID: r.ID, ActorID: w.a.ID,
			Payload: events.PresencePayload{State: events.PresenceWatching}}); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		var roster protocol.PresenceRosterResult
		if err := w.call(t, "S", protocol.MethodPresenceRoster, protocol.PresenceRosterParams{WorkspaceID: string(w.wsA.ID)}, &roster); err != nil {
			t.Fatal(err)
		}
		if len(roster.Members) == 1 && len(roster.Members[0].Watching) == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("roster = %+v, want A watching both sessions", roster)
		}
		time.Sleep(10 * time.Millisecond)
	}
	calls := []struct {
		method string
		params any
		kept   []string
	}{
		{protocol.MethodCoordMessagesList, protocol.CoordMessagesListParams{WorkspaceID: string(w.wsA.ID)}, []string{"note to self"}},
		{protocol.MethodCoordMessagesList, protocol.CoordMessagesListParams{WorkspaceID: string(w.wsA.ID), RunID: string(ua.ID)}, []string{"note to self"}},
		{protocol.MethodRunEvidenceList, protocol.RunEvidenceListParams{WorkspaceID: string(w.wsA.ID)}, []string{`"p-ua"`}},
		{protocol.MethodPresenceRoster, protocol.PresenceRosterParams{WorkspaceID: string(w.wsA.ID)}, []string{string(ua.ID)}},
		{protocol.MethodPresenceRoster, protocol.PresenceRosterParams{WorkspaceID: string(w.wsA.ID), RunID: string(runA.ID)}, nil},
		{protocol.MethodRunGet, protocol.RunIDParams{RunID: string(ua.ID)}, []string{"wat-x"}},
		{protocol.MethodRunList, protocol.RunListParams{WorkspaceID: string(w.wsA.ID)}, []string{"wat-x"}},
	}
	for _, who := range []string{"S", "A", "U"} {
		for _, c := range calls {
			var res json.RawMessage
			if err := w.call(t, who, c.method, c.params, &res); err != nil {
				t.Fatalf("%s %s: %v", who, c.method, err)
			}
			for _, kept := range c.kept {
				if !strings.Contains(string(res), kept) {
					t.Errorf("%s %s %+v = %s, want %s kept", who, c.method, c.params, res, kept)
				}
			}
			leaked := strings.Contains(string(res), string(runA.ID)) || strings.Contains(string(res), "acme/b")
			if leaked != (who == "S") {
				t.Errorf("%s %s %+v names A's two-repo session or wsB's repository = %v, want %v: %s", who, c.method, c.params, leaked, who == "S", res)
			}
		}
	}
}

// A wait on a pull request in a repository only others reach stays unseen
// in coordination history, where its wake is left out, and in
// run.wait.cancel, which answers as for a missing wait and cancels nothing.
func TestAHiddenWaitStaysHiddenInHistoryAndCancel(t *testing.T) {
	t.Parallel()
	w := newScopeWorld(t)
	ctx := t.Context()
	db := w.e.db
	ua := w.runUA
	if err := db.SetWorkspaceOrigin(ctx, w.wsB.ID, "https://github.com/acme/b.git"); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	const spec = "pr:acme/b#7:merged"
	waits := []*store.RunWait{{
		Watch: store.Watch{MemberID: w.u.ID, WorkspaceID: w.wsA.ID, TargetKind: protocol.WatchTargetPR, Repo: "acme/b", Number: 7, TargetTitle: spec,
			Conditions: []string{protocol.WaitKindPRMerged}, Source: protocol.WatchSourceAgent, CreatedAt: now, UpdatedAt: now,
			ExpiresAt: now.Add(time.Hour), NextCheckAt: &now},
		Spec: spec, Kind: protocol.WaitKindPRMerged}}
	if _, err := db.RecordWaitingReport(ctx, &store.CoordStateReport{RunID: ua.ID, IdempotencyKey: "wait-b", Outcome: protocol.CoordOutcomeWaiting,
		WaitingFor: "ci", Until: []string{spec}}, waits); err != nil {
		t.Fatal(err)
	}
	wait := waits[0].ID
	wake := "Aether: your wait " + spec + " resolved: merged."
	for _, m := range []*store.RunMessage{
		{WorkspaceID: w.wsA.ID, FromRun: ua.ID, ToRun: ua.ID, Body: "note to self", Kind: store.RunMessageKindMessage, CreatedAt: now},
		{WorkspaceID: w.wsA.ID, FromRun: ua.ID, ToRun: ua.ID, Body: wake, Kind: store.RunMessageKindMessage, CorrelationID: wait, CreatedAt: now.Add(time.Second)},
	} {
		if err := db.AppendRunMessage(ctx, m, 100); err != nil {
			t.Fatal(err)
		}
	}
	watches, err := watch.New(watch.Config{Store: db, Bus: w.bus, Runs: w.e.srv.cfg.Runs})
	if err != nil {
		t.Fatal(err)
	}
	w.e.srv.cfg.Services.Watches = watches

	for who, want := range map[string][]string{"S": {wake, "note to self"}, "U": {"note to self"}} {
		var list protocol.CoordMessagesListResult
		if err := w.call(t, who, protocol.MethodCoordMessagesList, protocol.CoordMessagesListParams{WorkspaceID: string(w.wsA.ID), RunID: string(ua.ID)}, &list); err != nil {
			t.Fatalf("%s coord.messages.list: %v", who, err)
		}
		var bodies []string
		for _, m := range list.Messages {
			bodies = append(bodies, m.Body)
		}
		if strings.Join(bodies, "|") != strings.Join(want, "|") {
			t.Errorf("%s coord.messages.list = %q, want %q", who, bodies, want)
		}
	}
	cancel := func(id string) error {
		return w.call(t, "U", protocol.MethodRunWaitCancel, protocol.RunWaitCancelParams{RunID: string(ua.ID), WaitID: id}, nil)
	}
	hidden, missing := cancel(wait), cancel("wat-doesnotexist")
	if hidden == nil || missing == nil || wireErrOf(t, hidden).Code != wireErrOf(t, missing).Code ||
		strings.ReplaceAll(wireErrOf(t, hidden).Message, wait, "wat-doesnotexist") != wireErrOf(t, missing).Message {
		t.Errorf("U run.wait.cancel of the hidden wait = %v, of a missing one = %v; want them identical", hidden, missing)
	}
	if got, err := db.GetRunWait(ctx, wait); err != nil || got.WaitState == protocol.WaitCancelled {
		t.Errorf("the hidden wait after U's cancel = %+v, %v; want it untouched", got, err)
	}
	var res protocol.RunWaitCancelResult
	if err := w.call(t, "S", protocol.MethodRunWaitCancel, protocol.RunWaitCancelParams{RunID: string(ua.ID), WaitID: wait}, &res); err != nil || res.Wait.Repo != "acme/b" {
		t.Errorf("S run.wait.cancel = %+v, %v; want the wait", res, err)
	}
}

// Pages of coordination messages and evidence are filled with what the
// caller sees, and their cursors point at it, so a page never ends on a
// hidden row.
func TestPagesFillWithWhatTheCallerSees(t *testing.T) {
	t.Parallel()
	w := newScopeWorld(t)
	ctx := t.Context()
	db := w.e.db
	ua := w.runUA
	now := time.Now()
	for _, m := range []*store.RunMessage{
		{WorkspaceID: w.wsA.ID, FromRun: ua.ID, ToRun: ua.ID, Body: "note to self", Kind: store.RunMessageKindMessage, CreatedAt: now},
		{WorkspaceID: w.wsA.ID, FromRun: w.runA.ID, ToRun: ua.ID, Body: "from two repos", Kind: store.RunMessageKindMessage, CreatedAt: now.Add(time.Second)},
		{WorkspaceID: w.wsA.ID, FromRun: ua.ID, ToRun: w.runA.ID, Body: "to two repos", Kind: store.RunMessageKindMessage, CreatedAt: now.Add(2 * time.Second)},
	} {
		if err := db.AppendRunMessage(ctx, m, 100); err != nil {
			t.Fatal(err)
		}
	}
	var packetIDs []string
	for i, r := range []*domain.Run{ua, w.runA} {
		p := handlerPacket("p-"+string(r.ID), w.wsA.ID, r.ID, r.MemberID)
		p.CapturedAt = now.Add(time.Duration(i) * time.Second)
		if err := db.CreateEvidencePacket(ctx, p); err != nil {
			t.Fatal(err)
		}
		packetIDs = append(packetIDs, p.ID)
	}
	ev, err := evidence.New(evidence.Config{Store: db, Runs: db, Git: struct{ evidence.GitEvidence }{}, EvidenceDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	w.e.srv.cfg.Services.Evidence = ev

	var messages protocol.CoordMessagesListResult
	if err = w.call(t, "U", protocol.MethodCoordMessagesList, protocol.CoordMessagesListParams{WorkspaceID: string(w.wsA.ID), Limit: 1}, &messages); err != nil {
		t.Fatal(err)
	}
	if len(messages.Messages) != 1 || messages.Messages[0].Body != "note to self" || messages.NextBefore != "" {
		t.Errorf("U's first page of one message = %+v, want the note and no cursor", messages)
	}
	var packets protocol.EvidencePacketListResult
	if err = w.call(t, "U", protocol.MethodRunEvidenceList, protocol.RunEvidenceListParams{WorkspaceID: string(w.wsA.ID), Limit: 1}, &packets); err != nil {
		t.Fatal(err)
	}
	visible, err := db.GetEvidencePacket(ctx, packetIDs[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(packets.Packets) != 1 || packets.Packets[0].ID != visible.ID || packets.NextBefore != store.EncodeEvidenceCursor(visible.CapturedAt, visible.ID) {
		t.Errorf("U's first page of one packet = %+v, want %s and a cursor at it", packets, visible.ID)
	}
	var all protocol.CoordMessagesListResult
	if err := w.call(t, "S", protocol.MethodCoordMessagesList, protocol.CoordMessagesListParams{WorkspaceID: string(w.wsA.ID), Limit: 2}, &all); err != nil {
		t.Fatal(err)
	}
	if len(all.Messages) != 2 || all.Messages[1].Body != "from two repos" || all.NextBefore == "" {
		t.Errorf("S's first page of two messages = %+v, want the two newest and a cursor", all)
	}
}
