package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/3xDevOps/Aether/internal/protocol"
)

func init() {
	register(command{
		name:    "logs",
		short:   "print what a run's agent did as plain text: terminal lines, or an Enhanced run's messages and tool calls",
		usage:   "aether logs <run-id> [--limit <n>]",
		example: "aether logs run-d6ay38tj8j --limit 50",
		flags:   true,
		run:     runLogs,
	})
}

func runLogs(args []string) error {
	fs := newFlagSet("logs")
	limit := fs.Int("limit", 200, "print the newest N terminal lines, or an Enhanced run's newest N messages and tool calls")
	runID, err := parseLeadingArg(fs, args)
	if err != nil {
		return fmt.Errorf("%w\nusage: aether logs <run-id> [--limit N]", err)
	}
	if runID == "" || *limit < 1 {
		return fmt.Errorf("usage: aether logs <run-id> [--limit N]")
	}
	return withControl(func(c *protocol.Client) error {
		r, err := getRun(c, runID)
		if err != nil {
			return err
		}
		if r.ACP {
			return printACPLogs(c, os.Stdout, r.ID, *limit)
		}
		return printTerminalLogs(c, os.Stdout, r.ID, *limit)
	})
}

func printTerminalLogs(c *protocol.Client, w io.Writer, runID string, limit int) error {
	var pages [][]protocol.TerminalHistoryLine
	before, read, truncated := "", 0, false
	seen := map[string]bool{}
	for read < limit {
		var res protocol.TerminalHistoryResult
		if err := c.Call(protocol.MethodTerminalHistory, protocol.TerminalHistoryParams{RunID: runID, Before: before,
			Limit: min(limit-read, protocol.MaxTerminalHistoryLimit)}, &res); err != nil {
			return err
		}
		pages, read = append(pages, res.Lines), read+len(res.Lines)
		if !res.HasMore || res.NextCursor == "" || seen[res.NextCursor] {
			truncated = res.TruncatedBefore
			break
		}
		seen[res.NextCursor], before = true, res.NextCursor
	}
	if truncated {
		if _, err := fmt.Fprintln(w, "[earlier output has expired]"); err != nil {
			return err
		}
	}
	for _, page := range slices.Backward(pages) {
		for _, line := range page {
			if _, err := fmt.Fprintln(w, printable(line.Text)); err != nil {
				return err
			}
		}
	}
	return nil
}

// acpEntry is one message or tool call. A message is logged in segments
// and a tool call in snapshots, so an entry gathers its items by id within
// a turn (an agent may reuse a tool call id in a later turn) and sits where
// its first item was written.
type acpEntry struct {
	text strings.Builder
	tool *acpToolCall
}

// acpItem is the part of a run's ACP item log aether logs reads. The client
// decodes it itself instead of importing the server's acphost package.
type acpItem struct {
	Turn     int64        `json:"turn"`
	Kind     string       `json:"kind"`
	Message  *acpMessage  `json:"message,omitempty"`
	ToolCall *acpToolCall `json:"tool_call,omitempty"`
}

type acpMessage struct {
	Role      string `json:"role"`
	MessageID string `json:"message_id"`
	Text      string `json:"text"`
}

type acpToolCall struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Status   string `json:"status,omitempty"`
	ExitCode *int   `json:"exit_code,omitempty"`
}

func (e *acpEntry) line() string {
	if e.tool == nil {
		return printable(strings.TrimSpace(e.text.String()))
	}
	line := strings.TrimSpace("tool "+e.tool.Status) + ": " + printable(e.tool.Title)
	if e.tool.ExitCode != nil {
		line += fmt.Sprintf(" (exit %d)", *e.tool.ExitCode)
	}
	return line
}

func acpEntries(frames []protocol.ACPFrame) []*acpEntry {
	var entries []*acpEntry
	byKey := map[string]*acpEntry{}
	for _, f := range frames {
		if f.Reset {
			entries, byKey = nil, map[string]*acpEntry{}
			continue
		}
		var it acpItem
		if json.Unmarshal(f.Item, &it) != nil {
			continue
		}
		key := ""
		switch {
		case it.Kind == "reset":
			entries, byKey = nil, map[string]*acpEntry{}
			continue
		case it.Kind == "message" && it.Message != nil:
			key = "message " + it.Message.MessageID
		case it.Kind == "tool_call" && it.ToolCall != nil:
			key = "tool " + it.ToolCall.ID
		default:
			continue
		}
		key = fmt.Sprintf("%d %s", it.Turn, key)
		e := byKey[key]
		if e == nil {
			e = &acpEntry{}
			if it.Message != nil {
				e.text.WriteString(it.Message.Role + ": ")
			}
			byKey[key] = e
			entries = append(entries, e)
		}
		if it.Message != nil {
			e.text.WriteString(it.Message.Text)
		} else {
			e.tool = it.ToolCall
		}
	}
	return entries
}

// The server cuts a frame to fit a history page; when the whole item cannot
// be fetched, the cut text stays, marked with the fetch error.
func fullACPFrame(c *protocol.Client, runID string, f protocol.ACPFrame) protocol.ACPFrame {
	if !f.Truncated {
		return f
	}
	var res protocol.RunACPItemResult
	fetchErr := c.Call(protocol.MethodRunACPItem, protocol.RunACPItemParams{RunID: runID, Seq: f.Seq}, &res)
	if fetchErr == nil {
		f.Item = res.Item
		return f
	}
	mark := fmt.Sprintf(" [truncated; %s: %v]", protocol.MethodRunACPItem, fetchErr)
	var item map[string]json.RawMessage
	var it acpItem
	if json.Unmarshal(f.Item, &item) != nil || json.Unmarshal(f.Item, &it) != nil {
		return f
	}
	var err error
	switch {
	case it.Message != nil:
		it.Message.Text += mark
		item["message"], err = json.Marshal(it.Message)
	case it.ToolCall != nil:
		it.ToolCall.Title += mark
		item["tool_call"], err = json.Marshal(it.ToolCall)
	}
	if err != nil {
		return f
	}
	if raw, merr := json.Marshal(item); merr == nil {
		f.Item = raw
	}
	return f
}

// acpEntries drops everything before a reset, so older pages add nothing.
func hasReset(frames []protocol.ACPFrame) bool {
	return slices.ContainsFunc(frames, func(f protocol.ACPFrame) bool {
		var it acpItem
		return f.Reset || json.Unmarshal(f.Item, &it) == nil && it.Kind == "reset"
	})
}

// A message resumes under its MessageID only after tool calls, so a message
// whose first segment is off the page is the page's first message.
func enough(entries []*acpEntry, limit int) bool {
	first := slices.IndexFunc(entries, func(e *acpEntry) bool { return e.tool == nil })
	return len(entries) > limit && len(entries)-limit > first
}

// printACPLogs reads pages back from the newest until the last limit entries
// are whole.
func printACPLogs(c *protocol.Client, w io.Writer, runID string, limit int) error {
	var frames []protocol.ACPFrame
	var before int64
	truncated := false
	for {
		var res protocol.RunACPHistoryResult
		if err := c.Call(protocol.MethodRunACPHistory, protocol.RunACPHistoryParams{RunID: runID, BeforeSeq: before}, &res); err != nil {
			return err
		}
		for i, f := range res.Frames {
			res.Frames[i] = fullACPFrame(c, runID, f)
		}
		frames, truncated = append(res.Frames, frames...), res.TruncatedBefore
		if len(res.Frames) == 0 || res.Frames[0].Seq <= res.OldestSeq || hasReset(res.Frames) || enough(acpEntries(frames), limit) {
			break
		}
		before = res.Frames[0].Seq
	}
	entries := acpEntries(frames)
	if len(entries) > limit {
		entries, truncated = entries[len(entries)-limit:], false
	}
	if truncated {
		if _, err := fmt.Fprintln(w, "[earlier conversation has expired]"); err != nil {
			return err
		}
	}
	for _, e := range entries {
		if _, err := fmt.Fprintln(w, e.line()); err != nil {
			return err
		}
	}
	return nil
}
