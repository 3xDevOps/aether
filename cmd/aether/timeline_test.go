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
