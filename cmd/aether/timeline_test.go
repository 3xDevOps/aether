package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
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
