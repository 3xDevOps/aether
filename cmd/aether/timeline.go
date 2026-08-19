package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"golang.org/x/term"

	"github.com/3xDevOps/Aether/internal/attribution"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// timelinePageSize is how much history one request asks for; the command
// pages until the server reports no more.
const timelinePageSize = 200

func init() {
	register(command{
		name:  "timeline",
		short: "show a session's history, filterable and exportable as JSONL",
		run:   runTimeline,
	})
}

func runTimeline(args []string) error {
	fs := flag.NewFlagSet("timeline", flag.ExitOnError)
	session := fs.String("session", "", "session ID or name (default: the only session)")
	run := fs.String("run", "", "only events for this run")
	member := fs.String("member", "", "only events by this member (ID or display name)")
	types := fs.String("type", "", "comma-separated event types, e.g. run.status,session.timeline")
	limit := fs.Int("limit", 200, "stop after this many entries (0 for the whole history)")
	jsonl := fs.Bool("jsonl", false, "write one JSON event per line instead of a table")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return withControl(func(c *protocol.Client) error {
		sessID, err := resolveSession(c, *session)
		if err != nil {
			return err
		}
		params := protocol.SessionTimelineParams{
			SessionID: sessID,
			RunID:     *run,
			Types:     splitTypes(*types),
		}
		if *member != "" {
			m, merr := resolveMember(c, *member)
			if merr != nil {
				return merr
			}
			params.MemberID = m.ID
		}
		if *jsonl {
			enc := json.NewEncoder(os.Stdout)
			return eachTimelinePage(c, params, *limit, func(page []protocol.Event) error {
				for _, ev := range page {
					if encErr := enc.Encode(ev); encErr != nil {
						return fmt.Errorf("write jsonl: %w", encErr)
					}
				}
				return nil
			})
		}
		byID, err := memberDirectory(c)
		if err != nil {
			return err
		}
		return printTimeline(c, params, *limit, byID)
	})
}

func splitTypes(list string) []string {
	var out []string
	for _, t := range strings.Split(list, ",") {
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// eachTimelinePage walks the history a page at a time, stopping when the
// server reports no more, when want entries have been visited (want zero
// means the whole history), or when visit fails.
func eachTimelinePage(c *protocol.Client, params protocol.SessionTimelineParams, want int, visit func([]protocol.Event) error) error {
	seen := 0
	for {
		params.Limit = timelinePageSize
		if want > 0 && want-seen < timelinePageSize {
			params.Limit = want - seen
		}
		var res protocol.SessionTimelineResult
		if err := c.Call(protocol.MethodSessionTimeline, params, &res); err != nil {
			return err
		}
		if err := visit(res.Events); err != nil {
			return err
		}
		seen += len(res.Events)
		before := params.AfterSeq
		params.AfterSeq = res.NextSeq
		if !res.More || res.NextSeq <= before || (want > 0 && seen >= want) {
			return nil
		}
	}
}

func printTimeline(c *protocol.Client, params protocol.SessionTimelineParams, want int, byID map[string]protocol.Member) error {
	color := term.IsTerminal(int(os.Stdout.Fd()))
	name := func(id string) string {
		m, ok := byID[id]
		if !ok {
			return id
		}
		if color {
			return attribution.Sprint(m.Color, m.DisplayName)
		}
		return m.DisplayName
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "SEQ\tTIME\tACTOR\tRUN\tTYPE\tDETAIL")
	err := eachTimelinePage(c, params, want, func(page []protocol.Event) error {
		for _, ev := range page {
			actor := ""
			if ev.ActorID != "" {
				actor = name(ev.ActorID)
			}
			_, _ = fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\n",
				ev.Seq, shortTime(ev.Time), actor, ev.RunID, ev.Type, printable(timelineDetail(ev, name)))
		}
		return nil
	})
	if err != nil {
		return err
	}
	return tw.Flush()
}

// printable neutralizes control characters (the class internal/sshd's
// hasControlChars guards against) so agent-authored event text cannot
// drive the operator's terminal or break the table.
func printable(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
}

func shortTime(stamp string) string {
	t, err := time.Parse(time.RFC3339Nano, stamp)
	if err != nil {
		return stamp
	}
	return t.Local().Format("2006-01-02 15:04:05")
}

// timelineDetail renders one entry's payload as a single line. Payload
// types the CLI does not know - a newer server's - fall back to their raw
// JSON rather than being dropped.
func timelineDetail(ev protocol.Event, name func(string) string) string {
	p, err := events.DecodePayload(events.Type(ev.Type), ev.Payload)
	if err != nil {
		return string(ev.Payload)
	}
	switch v := p.(type) {
	case events.RunStatusPayload:
		detail := string(v.To)
		if v.From != "" {
			detail = string(v.From) + " -> " + detail
		}
		if v.Reason != "" {
			detail += " (" + v.Reason + ")"
		}
		return detail
	case events.TimelinePayload:
		detail := string(v.Kind)
		switch {
		case v.Kind == events.TimelineHandoff && v.Message != "":
			detail += " to " + name(v.Message)
		case v.Message != "":
			detail += ": " + firstLine(v.Message)
		}
		return detail
	case events.ApprovalPayload:
		return string(v.Decision) + " " + v.Action
	case events.PresencePayload:
		return string(v.State)
	case events.RunCostPayload:
		if !v.Metered {
			return "unmetered"
		}
		return fmt.Sprintf("%d in / %d out tokens, $%.4f", v.InputTokens, v.OutputTokens, v.CostUSD)
	}
	return string(ev.Payload)
}
