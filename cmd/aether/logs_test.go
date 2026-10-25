package main

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/3xDevOps/Aether/internal/acphost"
	"github.com/3xDevOps/Aether/internal/protocol"
)

func acpFrame(t *testing.T, seq int64, it acphost.Item) protocol.ACPFrame {
	t.Helper()
	raw, err := json.Marshal(it)
	if err != nil {
		t.Fatal(err)
	}
	return protocol.ACPFrame{Seq: seq, Item: raw}
}

// An Enhanced run's log is one line per message and tool call, however many
// segments and snapshots the session wrote for each.
func TestLogsPrintsAnEnhancedSessionAsLines(t *testing.T) {
	exit := 1
	frames := []protocol.ACPFrame{
		acpFrame(t, 1, acphost.Item{Kind: acphost.KindMessage, Message: &acphost.Message{Role: "user", MessageID: "u1", Text: "fix the tests", Complete: true}}),
		acpFrame(t, 2, acphost.Item{Kind: acphost.KindThought, Message: &acphost.Message{Role: "assistant", MessageID: "t1", Text: "hmm"}}),
		acpFrame(t, 3, acphost.Item{Kind: acphost.KindMessage, Message: &acphost.Message{Role: "assistant", MessageID: "a1", Text: "Running \x1b[31mthe"}}),
		acpFrame(t, 4, acphost.Item{Kind: acphost.KindToolCall, ToolCall: &acphost.ToolCall{ID: "c1", Title: "go test ./...", Status: "pending"}}),
		acpFrame(t, 5, acphost.Item{Kind: acphost.KindMessage, Message: &acphost.Message{Role: "assistant", MessageID: "a1", Text: " suite\nnow.", Complete: true}}),
		acpFrame(t, 6, acphost.Item{Kind: acphost.KindToolCall, ToolCall: &acphost.ToolCall{ID: "c1", Title: "go test ./...", Status: "failed", ExitCode: &exit}}),
	}
	c := fakeControlMethods(t, map[string]*fakeReply{
		protocol.MethodRunACPHistory: {result: protocol.RunACPHistoryResult{Frames: frames, OldestSeq: 1}},
	})
	var out bytes.Buffer
	if err := printACPLogs(c, &out, "r1", 10); err != nil {
		t.Fatal(err)
	}
	want := "user: fix the tests\nassistant: Running  [31mthe suite now.\ntool failed: go test ./... (exit 1)\n"
	if got := out.String(); got != want {
		t.Fatalf("logs = %q, want %q", got, want)
	}
	out.Reset()
	if err := printACPLogs(c, &out, "r1", 1); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "tool failed: go test ./... (exit 1)\n" {
		t.Fatalf("logs --limit 1 = %q", got)
	}
}

func TestLogsPrintsTerminalHistoryOldestFirst(t *testing.T) {
	c := fakeControlMethods(t, map[string]*fakeReply{
		protocol.MethodTerminalHistory: {result: protocol.TerminalHistoryResult{Lines: []protocol.TerminalHistoryLine{{Text: "$ go test"}, {Text: "ok"}}, TruncatedBefore: true}},
	})
	var out bytes.Buffer
	if err := printTerminalLogs(c, &out, "r1", 50); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "[earlier output has expired]\n$ go test\nok\n"; got != want {
		t.Fatalf("logs = %q, want %q", got, want)
	}
}
