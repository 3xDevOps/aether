package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/cli"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/testhome"
)

// TestTimelineDetailIsPrintable pins that agent-authored event text is
// neutralized before it reaches the operator's terminal: an escape
// sequence or tab in a timeline note must not survive into the table.
func TestTimelineDetailIsPrintable(t *testing.T) {
	payload, err := json.Marshal(events.TimelinePayload{
		Kind:    events.TimelineNote,
		Message: "coordination message: \x1b[2J\x07spoof\tcolumn",
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	ev := protocol.Event{Type: string(events.TypeTimeline), Payload: payload}
	got := printable(timelineDetail(ev, func(id string) string { return id }))
	if strings.ContainsFunc(got, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		t.Fatalf("detail carries control characters: %q", got)
	}
	if !strings.Contains(got, "spoof") {
		t.Fatalf("detail lost its text: %q", got)
	}
}

func TestTimelineDetailNamesAgentMail(t *testing.T) {
	for _, tc := range []struct {
		payload events.Payload
		want    string
	}{
		{events.CoordMessagePayload{MessageID: "m1", FromRunID: "r1", ToRunID: "r2", Kind: "question"}, "r1 -> r2 · question"},
		{events.CoordMessageAckedPayload{MessageID: "m1", ToRunID: "r2"}, "r2 acknowledged m1"},
		{events.TimelinePayload{Kind: events.TimelineReport, Outcome: "success", Summary: "added the check\nand its test"}, "success: added the check ..."},
		{events.RunStatusPayload{From: "running", To: "needs-attention", Reason: "waiting for 2 workers", Activity: "waiting-agents", Cause: events.CauseWorkers},
			"running -> needs-attention (waiting for 2 workers) · waiting-agents by workers"},
		{events.TimelinePayload{Kind: events.TimelineReport, Outcome: "waiting", WaitingFor: "ci", Summary: "CI on PR 12"}, "waiting for ci: CI on PR 12"},
	} {
		raw, err := json.Marshal(tc.payload)
		if err != nil {
			t.Fatalf("marshal payload: %v", err)
		}
		ev := protocol.Event{Type: string(tc.payload.EventType()), Payload: raw}
		if got := timelineDetail(ev, func(id string) string { return id }); got != tc.want {
			t.Fatalf("detail = %q, want %q", got, tc.want)
		}
	}
}

func TestTimelineTakesTheRunAsAnArgument(t *testing.T) {
	testhome.Isolate(t)
	for _, args := range [][]string{{"timeline", "run-1", "run-2"}, {"timeline", "run-1", "--run", "run-2"}} {
		if err := dispatch(args); err == nil || !strings.Contains(err.Error(), "name the session once") {
			t.Errorf("aether %s = %v, want the session named once", strings.Join(args, " "), err)
		}
	}
	if err := dispatch([]string{"timeline", "run-1", "--jsonl"}); !errors.Is(err, cli.ErrNotLinked) {
		t.Errorf("aether timeline run-1 --jsonl = %v, want it to reach the server", err)
	}
}
