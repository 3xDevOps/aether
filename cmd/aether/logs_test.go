package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net"
	"strings"
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

func TestLogsKeepsAReusedToolIDPerTurn(t *testing.T) {
	tool := func(turn int64, status string) acphost.Item {
		return acphost.Item{Turn: turn, Kind: acphost.KindToolCall, ToolCall: &acphost.ToolCall{ID: "c1", Title: "ls", Status: status}}
	}
	msg := acphost.Item{Turn: 1, Kind: acphost.KindMessage, Message: &acphost.Message{Role: "assistant", MessageID: "a1", Text: "hi"}}
	frames := []protocol.ACPFrame{
		acpFrame(t, 1, tool(1, "pending")),
		acpFrame(t, 2, msg),
		acpFrame(t, 3, tool(1, "completed")),
		acpFrame(t, 4, tool(2, "failed")),
	}
	c := fakeControlMethods(t, map[string]*fakeReply{
		protocol.MethodRunACPHistory: {result: protocol.RunACPHistoryResult{Frames: frames, OldestSeq: 1}},
	})
	var out bytes.Buffer
	if err := printACPLogs(c, &out, "r1", 10); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "tool completed: ls\nassistant: hi\ntool failed: ls\n"; got != want {
		t.Fatalf("logs = %q, want %q", got, want)
	}
	out.Reset()
	if err := printACPLogs(c, &out, "r1", 1); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "tool failed: ls\n"; got != want {
		t.Fatalf("logs --limit 1 = %q, want %q", got, want)
	}
}

func TestLogsDropsEntriesBeforeAResetItem(t *testing.T) {
	frames := []protocol.ACPFrame{
		acpFrame(t, 1, acphost.Item{Kind: acphost.KindMessage, Message: &acphost.Message{Role: "user", MessageID: "u1", Text: "old"}}),
		acpFrame(t, 2, acphost.Item{Kind: acphost.KindReset}),
		acpFrame(t, 3, acphost.Item{Kind: acphost.KindMessage, Message: &acphost.Message{Role: "user", MessageID: "u2", Text: "new"}}),
	}
	entries := acpEntries(frames)
	if len(entries) != 1 || entries[0].line() != "user: new" {
		t.Fatalf("entries after reset = %d, want only the new message", len(entries))
	}
}

func TestLogsFollowsAnEmptyTerminalPageWithAContinuationCursor(t *testing.T) {
	server, client := net.Pipe()
	go func() {
		defer server.Close() //nolint:errcheck
		r := bufio.NewReader(server)
		for {
			line, err := protocol.ReadLine(r)
			if err != nil {
				return
			}
			var req protocol.Request
			var p protocol.TerminalHistoryParams
			if json.Unmarshal(line, &req) != nil || json.Unmarshal(req.Params, &p) != nil {
				return
			}
			res := protocol.TerminalHistoryResult{}
			switch p.Before {
			case "":
				res = protocol.TerminalHistoryResult{HasMore: true, NextCursor: "c1"}
			case "c1":
				res = protocol.TerminalHistoryResult{Lines: []protocol.TerminalHistoryLine{{Text: "real"}}}
			}
			raw, _ := json.Marshal(res)
			out, _ := json.Marshal(protocol.Response{JSONRPC: "2.0", ID: req.ID, Result: raw})
			if _, err := server.Write(append(out, '\n')); err != nil {
				return
			}
		}
	}()
	c := protocol.NewClient(client)
	t.Cleanup(func() { _ = c.Close() })
	var out bytes.Buffer
	if err := printTerminalLogs(c, &out, "r1", 50); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "real\n" {
		t.Fatalf("logs = %q, want the line after the empty page", got)
	}
}

func TestLogsStopsWhenTheTerminalCursorRepeats(t *testing.T) {
	c := fakeControlMethods(t, map[string]*fakeReply{
		protocol.MethodTerminalHistory: {result: protocol.TerminalHistoryResult{HasMore: true, NextCursor: "same"}},
	})
	var out bytes.Buffer
	if err := printTerminalLogs(c, &out, "r1", 50); err != nil {
		t.Fatal(err)
	}
}

func TestLogsFetchesTheFullItemOfATruncatedFrame(t *testing.T) {
	full := acpFrame(t, 1, acphost.Item{Kind: acphost.KindMessage, Message: &acphost.Message{Role: "assistant", MessageID: "a1", Text: "the whole long message", Complete: true}})
	cut := acpFrame(t, 1, acphost.Item{Kind: acphost.KindMessage, Message: &acphost.Message{Role: "assistant", MessageID: "a1", Text: "the whole"}})
	cut.Truncated = true
	history := &fakeReply{result: protocol.RunACPHistoryResult{Frames: []protocol.ACPFrame{cut}, OldestSeq: 1}}
	item := &fakeReply{result: protocol.RunACPItemResult{Item: full.Item}}
	c := fakeControlMethods(t, map[string]*fakeReply{protocol.MethodRunACPHistory: history, protocol.MethodRunACPItem: item})
	var out bytes.Buffer
	if err := printACPLogs(c, &out, "r1", 10); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "assistant: the whole long message\n"; got != want {
		t.Fatalf("logs = %q, want %q", got, want)
	}

	item.err = &protocol.Error{Code: protocol.CodeMethodNotFound, Message: "gone"}
	out.Reset()
	if err := printACPLogs(c, &out, "r1", 10); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "assistant: the whole [truncated; run.acp.item: rpc error -32601: gone]\n"; got != want {
		t.Fatalf("logs = %q, want %q", got, want)
	}
}

func TestLogsReadsBackToTheStartOfAResumedMessage(t *testing.T) {
	frames := []protocol.ACPFrame{
		acpFrame(t, 1, acphost.Item{Kind: acphost.KindMessage, Message: &acphost.Message{Role: "assistant", MessageID: "a1", Text: "start "}}),
		acpFrame(t, 2, acphost.Item{Kind: acphost.KindToolCall, ToolCall: &acphost.ToolCall{ID: "c1", Title: "ls", Status: "completed"}}),
		acpFrame(t, 3, acphost.Item{Kind: acphost.KindMessage, Message: &acphost.Message{Role: "assistant", MessageID: "a1", Text: "end", Complete: true}}),
	}
	server, client := net.Pipe()
	go func() {
		defer server.Close() //nolint:errcheck
		br := bufio.NewReader(server)
		for {
			line, err := protocol.ReadLine(br)
			if err != nil {
				return
			}
			var req protocol.Request
			var params protocol.RunACPHistoryParams
			if json.Unmarshal(line, &req) != nil || json.Unmarshal(req.Params, &params) != nil {
				return
			}
			page := frames[1:]
			if params.BeforeSeq == 2 {
				page = frames[:1]
			}
			resp := protocol.Response{JSONRPC: "2.0", ID: req.ID}
			resp.Result, _ = json.Marshal(protocol.RunACPHistoryResult{Frames: page, OldestSeq: 1})
			out, _ := json.Marshal(resp)
			if _, err := server.Write(append(out, '\n')); err != nil {
				return
			}
		}
	}()
	c := protocol.NewClient(client)
	t.Cleanup(func() { _ = c.Close() })
	var out bytes.Buffer
	if err := printACPLogs(c, &out, "r1", 2); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "assistant: start end\ntool completed: ls\n"; got != want {
		t.Fatalf("logs = %q, want %q", got, want)
	}
}

// Nothing before a reset reaches the output, so paging stops at it.
func TestLogsStopPagingAtAReset(t *testing.T) {
	history := &fakeReply{result: protocol.RunACPHistoryResult{OldestSeq: 1, Frames: []protocol.ACPFrame{
		acpFrame(t, 5, acphost.Item{Kind: acphost.KindReset}),
		acpFrame(t, 6, acphost.Item{Kind: acphost.KindMessage, Message: &acphost.Message{Role: "assistant", MessageID: "a2", Text: "fresh"}}),
	}}}
	c := fakeControlMethods(t, map[string]*fakeReply{protocol.MethodRunACPHistory: history})
	var out bytes.Buffer
	if err := printACPLogs(c, &out, "r1", 10); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "assistant: fresh\n"; got != want {
		t.Fatalf("logs = %q, want %q", got, want)
	}
	if got := string(history.params()); strings.Contains(got, "before_seq") {
		t.Fatalf("history read past the reset: %s", got)
	}
}
