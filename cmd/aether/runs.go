package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"golang.org/x/term"

	"github.com/3xDevOps/Aether/internal/attribution"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

func init() {
	register(command{
		name:  "runs",
		short: "list runs (--needs-you: runs that need you; --archived: only archived)",
		run:   runRuns,
	})
}

// needsAttention selects the needs-attention execution status, not the
// independent set of outstanding input requests.
func needsAttention(r protocol.Run) bool {
	return r.Status == string(domain.RunNeedsAttention)
}

// plural renders a count with its noun, so the notice reads as a sentence
// rather than a field.
func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s is", noun)
	}
	return fmt.Sprintf("%d %ss are", n, noun)
}

// renderRuns writes the table. The caller rejects --needs-you together with
// archived, so archived alone decides the fifth column.
func renderRuns(w io.Writer, runs []protocol.Run, memberName func(string) string, overlapOf map[string]string, needsYou, archived bool) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fifthColumn := "OVERLAP"
	if archived {
		fifthColumn = "DELETES"
	}
	if _, err := fmt.Fprintln(tw, "ID\tSTATUS\tAGENT\tMEMBER\t"+fifthColumn+"\tTITLE\tTASK"); err != nil {
		return err
	}
	for _, r := range runs {
		if (r.ArchivedAt != nil) != archived {
			continue
		}
		if needsYou && !needsAttention(r) {
			continue
		}
		title := r.Title
		if title == "" {
			title = r.Task
		}
		title = strings.ReplaceAll(title, "\n", " ")
		task := strings.ReplaceAll(r.Task, "\n", " ")
		fifth := overlapOf[r.ID]
		if archived {
			fifth = ""
			if r.DeletesAt != nil {
				fifth = *r.DeletesAt
			}
		}
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			r.ID, displayStatus(r), r.Harness, memberName(r.MemberID), fifth, title, task); err != nil {
			return err
		}
	}
	return tw.Flush()
}

func displayStatus(r protocol.Run) string {
	if needsAttention(r) {
		return "needs-you"
	}
	return r.Status
}

func runRuns(args []string) error {
	fs := flag.NewFlagSet("runs", flag.ExitOnError)
	needsYou := fs.Bool("needs-you", false, "list only runs that need you (wire status needs-attention)")
	archived := fs.Bool("archived", false, "list only archived runs, with their deletion date")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *needsYou && *archived {
		return fmt.Errorf("aether runs: --needs-you and --archived cannot be used together")
	}
	return withControl(func(c *protocol.Client) error {
		var rl protocol.RunListResult
		if err := c.Call(protocol.MethodRunList, protocol.RunListParams{}, &rl); err != nil {
			return err
		}
		var ml protocol.MemberListResult
		if err := c.Call(protocol.MethodMemberList, struct{}{}, &ml); err != nil {
			return err
		}
		var ol protocol.RunOverlapsResult
		if err := c.Call(protocol.MethodRunOverlaps, struct{}{}, &ol); err != nil {
			return err
		}
		colorOf := map[string]string{}
		nameOf := map[string]string{}
		for _, m := range ml.Members {
			colorOf[m.ID] = m.Color
			nameOf[m.ID] = m.DisplayName
		}
		color := term.IsTerminal(int(os.Stdout.Fd()))
		memberName := func(id string) string {
			name := nameOf[id]
			if name == "" {
				name = id
			}
			if color {
				name = attribution.Sprint(colorOf[id], name)
			}
			return name
		}
		overlapOf := map[string]string{}
		for _, o := range ol.Overlaps {
			flags := make([]string, 0, len(o.With))
			for _, p := range o.With {
				flags = append(flags, strings.Join(p.Files, ",")+" with "+memberName(p.MemberID))
			}
			overlapOf[o.RunID] = strings.Join(flags, "; ")
		}
		waiting := 0
		for _, r := range rl.Runs {
			if needsAttention(r) {
				waiting++
			}
		}
		if err := renderRuns(os.Stdout, rl.Runs, memberName, overlapOf, *needsYou, *archived); err != nil {
			return err
		}
		// The notice goes to stderr so it never lands in a pipeline reading
		// the table, and it is skipped when the table already is the answer.
		if waiting > 0 && !*needsYou && !*archived {
			_, _ = fmt.Fprintf(os.Stderr, "\n%s waiting for you: aether runs --needs-you\n",
				plural(waiting, "run"))
		}
		return nil
	})
}
