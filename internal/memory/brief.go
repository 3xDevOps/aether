package memory

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/3xDevOps/Aether/internal/coordhooks"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

// Brief is what a launching run of member's learns from team memory: the
// titles of the workspace's pins and member's own, the runs other than
// self that finished in the last 48 hours, and where to look for more,
// framed by coordhooks.MemoryRule. Every line is a pin or built by the
// server, so no unpinned agent text, report prose or run title reaches it.
// It fits in protocol.MemoryBriefMaxBytes by dropping older run lines, and
// is empty when there is nothing to say.
func (s *Service) Brief(ctx context.Context, workspace domain.WorkspaceID, member domain.MemberID, self domain.RunID) (string, error) {
	const method = protocol.MethodMemoryBrief
	ws, err := s.cfg.Store.GetWorkspace(ctx, workspace)
	if err != nil {
		return "", fail(method, err)
	}
	now := time.Now()
	in, err := s.cfg.Store.ReadMemoryBrief(ctx, store.MemoryBriefQuery{Workspace: workspace, Member: member, Self: self,
		Since: now.Add(-protocol.MemoryBriefWindow), Runs: protocol.MemoryBriefMaxRuns + 1})
	if err != nil {
		return "", fail(method, err)
	}
	var runs []string
	for _, r := range in.Runs {
		if r.ID != self && len(runs) < protocol.MemoryBriefMaxRuns {
			runs = append(runs, runLine(r, now))
		}
	}
	if len(in.Pinned) == 0 && len(runs) == 0 && in.Notes == 0 {
		return "", nil
	}
	pins := make([]string, 0, len(in.Pinned))
	for _, m := range in.Pinned {
		whose := memberName(m.PinnedByName, string(m.PinnedBy))
		if m.Scope == store.MemoryScopeMember {
			whose = "yours"
		}
		pins = append(pins, fmt.Sprintf("- %s %s (%s): %s", m.ID, m.Kind, whose, inline(m.Title, 0)))
	}
	b := brief{pins: pins, runs: runs}
	b.head, b.tail = frame(ws.Name, in.LiveRuns, in.Notes, in.AgentNotes)
	// Pins within their budgets leave room for at least one run line, so
	// pins are dropped only when rows bypassed the budgets.
	for len(b.String()) > protocol.MemoryBriefMaxBytes && len(b.runs)+len(b.pins) > 0 {
		if len(b.runs) > 0 {
			b.runs = b.runs[:len(b.runs)-1]
			continue
		}
		b.pins = b.pins[:len(b.pins)-1]
		b.hidden++
	}
	return b.String(), nil
}

// frame is the brief's opening and closing lines. The workspace name is
// cut and unquoted so the whole frame stays within the 512 bytes the pin
// budgets leave beside the run lines.
func frame(workspace string, live, notes, agentNotes int) (string, string) {
	head := `<team-memory workspace="` + inline(strings.ReplaceAll(workspace, `"`, "'"), protocol.MemoryBriefTaskBytes) +
		"\">\n" + coordhooks.MemoryRule + "\n"
	tail := fmt.Sprintf("%s: aether-internal status. %s (%s): aether-internal memory search <words>\n</team-memory>",
		count(live, "live run"), count(notes, "team note"), count(agentNotes, "unpinned agent note"))
	return head, tail
}

type brief struct {
	head, tail string
	pins, runs []string
	hidden     int
}

func (b brief) String() string {
	var out strings.Builder
	out.WriteString(b.head)
	if len(b.pins) > 0 || b.hidden > 0 {
		out.WriteString("Pinned:\n")
		for _, line := range b.pins {
			out.WriteString(line + "\n")
		}
		if b.hidden > 0 {
			out.WriteString("- " + count(b.hidden, "more pinned note") + " did not fit\n")
		}
	}
	if len(b.runs) > 0 {
		fmt.Fprintf(&out, "Recent runs (%dh):\n", int(protocol.MemoryBriefWindow.Hours()))
		for _, line := range b.runs {
			out.WriteString(line + "\n")
		}
	}
	out.WriteString(b.tail)
	return out.String()
}

// runLine says who ran what and how it ended, from server-owned fields
// and the first line of the task a member typed (or the swarm's
// objective).
func runLine(r *store.MemoryBriefRun, now time.Time) string {
	outcome := string(r.Outcome)
	if outcome == "" {
		outcome = string(r.Status)
	}
	line := fmt.Sprintf("- %s %s %s", memberName(r.MemberName, "unknown"), r.Harness, outcome)
	if r.Branch != "" {
		line += " `" + r.Branch + "`"
	}
	if r.PRNumber > 0 {
		line += " PR #" + strconv.Itoa(r.PRNumber)
		if r.PRState != "" {
			line += " " + r.PRState
		}
	}
	line += ", " + age(now.Sub(r.FinishedAt))
	if task := inline(r.Task, protocol.MemoryBriefTaskBytes); task != "" {
		line += ": " + strconv.Quote(task)
	}
	return line
}

// memberName is display, or fallback without one, cut so a pin's line
// stays within store.MemoryPinLineOverhead plus its title.
func memberName(display, fallback string) string {
	return inline(name(display, fallback), store.MemoryPinNameBytes)
}

func age(d time.Duration) string {
	if d < time.Hour {
		return strconv.Itoa(max(1, int(d.Minutes()))) + "m"
	}
	return strconv.Itoa(int(d.Hours())) + "h"
}

func count(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}

// inline puts text on one line, defuses the brief's tag and, when maxBytes
// is positive, cuts it to that many bytes on a rune boundary.
func inline(text string, maxBytes int) string {
	text = frameTag.ReplaceAllStringFunc(strings.Join(strings.Fields(text), " "), func(tag string) string {
		return strings.Replace(tag, "-", " ", 1)
	})
	if maxBytes <= 0 || len(text) <= maxBytes {
		return text
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut]
}
