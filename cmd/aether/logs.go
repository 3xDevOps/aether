package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/3xDevOps/Aether/internal/acphost"
	"github.com/3xDevOps/Aether/internal/protocol"
)

func init() {
	register(command{
		name:  "logs",
		short: "print what a run's agent did as plain text: terminal lines, or an Enhanced run's messages and tool calls",
		run:   runLogs,
	})
}

func runLogs(args []string) error {
	fs := flag.NewFlagSet("logs", flag.ExitOnError)
	limit := fs.Int("limit", 200, "print the newest N terminal lines, or an Enhanced run's newest N messages and tool calls")
	runID, err := parseLeadingArg(fs, args)
	if err != nil || runID == "" || *limit < 1 {
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
	for read < limit {
		var res protocol.TerminalHistoryResult
		if err := c.Call(protocol.MethodTerminalHistory, protocol.TerminalHistoryParams{RunID: runID, Before: before,
			Limit: min(limit-read, protocol.MaxTerminalHistoryLimit)}, &res); err != nil {
			return err
		}
		pages, read = append(pages, res.Lines), read+len(res.Lines)
		if !res.HasMore || len(res.Lines) == 0 {
			truncated = res.TruncatedBefore
			break
		}
		before = res.NextCursor
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
// and a tool call in snapshots, so an entry gathers its items by id.
type acpEntry struct {
	text strings.Builder
	tool *acphost.ToolCall
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

// A reset frame drops what came before it.
func acpEntries(frames []protocol.ACPFrame) []*acpEntry {
	var entries []*acpEntry
	byKey := map[string]*acpEntry{}
	for _, f := range frames {
		if f.Reset {
			entries, byKey = nil, map[string]*acpEntry{}
			continue
		}
		var it acphost.Item
		if json.Unmarshal(f.Item, &it) != nil {
			continue
		}
		key := ""
		switch {
		case it.Kind == acphost.KindMessage && it.Message != nil:
			key = "message " + it.Message.MessageID
		case it.Kind == acphost.KindToolCall && it.ToolCall != nil:
			key = "tool " + it.ToolCall.ID
		default:
			continue
		}
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

// printACPLogs reads pages back from the newest until they hold more than
// limit entries, so the oldest entry printed is whole.
func printACPLogs(c *protocol.Client, w io.Writer, runID string, limit int) error {
	var frames []protocol.ACPFrame
	var before int64
	truncated := false
	for {
		var res protocol.RunACPHistoryResult
		if err := c.Call(protocol.MethodRunACPHistory, protocol.RunACPHistoryParams{RunID: runID, BeforeSeq: before}, &res); err != nil {
			return err
		}
		frames, truncated = append(res.Frames, frames...), res.TruncatedBefore
		if len(res.Frames) == 0 || res.Frames[0].Seq <= res.OldestSeq || len(acpEntries(frames)) > limit {
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
