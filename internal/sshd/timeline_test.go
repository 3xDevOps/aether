package sshd

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/timeline"
)

// TestHandoffAndWorkspaceTimeline drives the acceptance path of  the
// way a user does: an owner hands a live run to a teammate, ownership and
// attribution follow without the agent being touched, and the workspace
// timeline shows the whole history, filters per member, and exports as
// JSONL that parses back into the same events.
func TestHandoffAndWorkspaceTimeline(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	log, err := events.OpenSQLiteLog(filepath.Join(dir, "events.db"))
	if err != nil {
		t.Fatalf("event log: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })
	bus, err := events.NewInProc(ctx, log)
	if err != nil {
		t.Fatalf("bus: %v", err)
	}
	t.Cleanup(func() { _ = bus.Close() })

	e := newTestEnv(t, func(c *Config) {
		c.Bus = bus
		c.Services.Timeline = timeline.NewReader(log)
	})
	_, grace := addMember(t, e, "Grace", domain.RoleCollaborator, false)

	publish := func(actor domain.MemberID, payload events.Payload) {
		t.Helper()
		if _, perr := bus.Publish(ctx, events.Event{
			WorkspaceID: e.ws.ID, RunID: e.run.ID, ActorID: actor, Payload: payload,
		}); perr != nil {
			t.Fatalf("publish %s: %v", payload.EventType(), perr)
		}
	}
	// History before the handoff: the run started, and a diff snapshot the
	// curated feed must leave out.
	publish("", events.RunStatusPayload{From: domain.RunQueued, To: domain.RunRunning})
	publish("", events.RunDiffPayload{Files: []events.FileDiffStat{{Path: "main.go", Additions: 3}}})

	// The owner hands the live run to Grace.
	ada := controlClient(t, e)
	if err = ada.Call(protocol.MethodRunHandoff, protocol.RunHandoffParams{
		RunID: string(e.run.ID), ToMemberID: string(grace.ID),
	}, nil); err != nil {
		t.Fatalf("run.handoff: %v", err)
	}

	// Ownership moved and the agent was never interrupted: the run is
	// still running and the scheduler was not asked to do anything.
	moved, err := e.store.GetRun(ctx, e.run.ID)
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if moved.MemberID != grace.ID {
		t.Fatalf("owner = %q, want %q", moved.MemberID, grace.ID)
	}
	if moved.Status != domain.RunRunning {
		t.Fatalf("status = %q, want running", moved.Status)
	}
	if calls := e.runs.Calls(); len(calls) != 0 {
		t.Fatalf("handoff touched the agent: %v", calls)
	}

	// Attribution follows the owner: the run now lists under Grace only.
	runsOf := func(member domain.MemberID) []string {
		t.Helper()
		var res protocol.RunListResult
		if lerr := ada.Call(protocol.MethodRunList, protocol.RunListParams{MemberID: string(member)}, &res); lerr != nil {
			t.Fatalf("run.list: %v", lerr)
		}
		ids := make([]string, 0, len(res.Runs))
		for _, r := range res.Runs {
			ids = append(ids, r.ID)
		}
		return ids
	}
	if got := runsOf(grace.ID); len(got) != 1 || got[0] != string(e.run.ID) {
		t.Fatalf("runs of the new owner = %v, want [%s]", got, e.run.ID)
	}
	if got := runsOf(e.member.ID); len(got) != 0 {
		t.Fatalf("runs of the previous owner = %v, want none", got)
	}

	// Grace steers the run she now owns.
	publish(grace.ID, events.TimelinePayload{Kind: events.TimelineSteer, Message: "keep going"})

	all := readTimeline(t, ada, protocol.WorkspaceTimelineParams{WorkspaceID: string(e.ws.ID)})
	if types := eventTypes(all); !slices.Equal(types, []string{"run.status", "workspace.timeline", "workspace.timeline"}) {
		t.Fatalf("timeline types = %v, want the status change, the handoff, and the steer", types)
	}
	handoff := all[1]
	if handoff.ActorID != string(e.member.ID) {
		t.Fatalf("handoff actor = %q, want the handing-off owner %q", handoff.ActorID, e.member.ID)
	}
	if handoff.RunID != string(e.run.ID) {
		t.Fatalf("handoff run = %q, want %q", handoff.RunID, e.run.ID)
	}
	if p := decodePayload(t, handoff).(events.TimelinePayload); p.Kind != events.TimelineHandoff || p.Message != string(grace.ID) {
		t.Fatalf("handoff payload = %+v, want a handoff to %s", p, grace.ID)
	}

	// Filtered per member: only what Grace did.
	byGrace := readTimeline(t, ada, protocol.WorkspaceTimelineParams{
		WorkspaceID: string(e.ws.ID), MemberID: string(grace.ID),
	})
	if len(byGrace) != 1 || byGrace[0].Seq != all[2].Seq {
		t.Fatalf("timeline for Grace = %v, want only her steer", eventTypes(byGrace))
	}

	// Filtered per type, including the detail stream the feed hides by
	// default.
	diffs := readTimeline(t, ada, protocol.WorkspaceTimelineParams{
		WorkspaceID: string(e.ws.ID), Types: []string{string(events.TypeRunDiff)},
	})
	if len(diffs) != 1 {
		t.Fatalf("run.diff filter returned %d events, want 1", len(diffs))
	}

	// One entry at a time reads back the same history, so a long workspace
	// pages instead of loading whole.
	paged := readTimeline(t, ada, protocol.WorkspaceTimelineParams{WorkspaceID: string(e.ws.ID), Limit: 1})
	if !slices.Equal(eventSeqs(paged), eventSeqs(all)) {
		t.Fatalf("paged timeline = %v, want %v", eventSeqs(paged), eventSeqs(all))
	}

	// JSONL export round-trips: one JSON event per line, parsed back into
	// the same envelopes and the same typed payloads.
	var jsonl bytes.Buffer
	enc := json.NewEncoder(&jsonl)
	for _, ev := range all {
		if err = enc.Encode(ev); err != nil {
			t.Fatalf("encode jsonl: %v", err)
		}
	}
	var back []protocol.Event
	sc := bufio.NewScanner(&jsonl)
	for sc.Scan() {
		var ev protocol.Event
		if err = json.Unmarshal(sc.Bytes(), &ev); err != nil {
			t.Fatalf("parse jsonl line %q: %v", sc.Text(), err)
		}
		back = append(back, ev)
	}
	if err = sc.Err(); err != nil {
		t.Fatalf("scan jsonl: %v", err)
	}
	if len(back) != len(all) {
		t.Fatalf("jsonl round-trip returned %d events, want %d", len(back), len(all))
	}
	for i, ev := range back {
		if ev.ID != all[i].ID || ev.Seq != all[i].Seq || ev.Time != all[i].Time ||
			ev.ActorID != all[i].ActorID || ev.RunID != all[i].RunID || ev.Type != all[i].Type {
			t.Fatalf("jsonl event %d = %+v, want %+v", i, ev, all[i])
		}
		if got, want := decodePayload(t, ev), decodePayload(t, all[i]); !reflect.DeepEqual(got, want) {
			t.Fatalf("jsonl payload %d = %+v, want %+v", i, got, want)
		}
	}
}

// readTimeline pages workspace.timeline to exhaustion, the way the CLI does.
func readTimeline(t *testing.T, c *protocol.Client, params protocol.WorkspaceTimelineParams) []protocol.Event {
	t.Helper()
	var out []protocol.Event
	for {
		var res protocol.WorkspaceTimelineResult
		if err := c.Call(protocol.MethodWorkspaceTimeline, params, &res); err != nil {
			t.Fatalf("workspace.timeline: %v", err)
		}
		out = append(out, res.Events...)
		if !res.More || res.NextSeq <= params.AfterSeq {
			return out
		}
		params.AfterSeq = res.NextSeq
	}
}

func decodePayload(t *testing.T, ev protocol.Event) events.Payload {
	t.Helper()
	p, err := events.DecodePayload(events.Type(ev.Type), ev.Payload)
	if err != nil {
		t.Fatalf("decode %s payload: %v", ev.Type, err)
	}
	return p
}

func eventTypes(evs []protocol.Event) []string {
	out := make([]string, 0, len(evs))
	for _, ev := range evs {
		out = append(out, ev.Type)
	}
	return out
}

func eventSeqs(evs []protocol.Event) []string {
	out := make([]string, 0, len(evs))
	for _, ev := range evs {
		out = append(out, ev.ID)
	}
	return out
}

// TestWorkspaceTimelineUnavailableWithoutReader proves the seam degrades
// like the others rather than panicking when no history is wired.
func TestWorkspaceTimelineUnavailableWithoutReader(t *testing.T) {
	e := newTestEnv(t, nil)
	err := controlClient(t, e).Call(protocol.MethodWorkspaceTimeline,
		protocol.WorkspaceTimelineParams{WorkspaceID: string(e.ws.ID)}, nil)
	var pe *protocol.Error
	if !errors.As(err, &pe) || pe.Code != protocol.CodeUnavailable {
		t.Fatalf("workspace.timeline without a reader = %v, want CodeUnavailable", err)
	}
}
