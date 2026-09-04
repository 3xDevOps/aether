package main

import (
	"flag"
	"fmt"
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
		short: "list runs (--attention: only the ones waiting on a human)",
		run:   runRuns,
	})
}

// needsAttention is the CLI's half of the stall notification path: a run
// the scheduler parked because its agent went quiet, or one whose agent
// exited and left results to look at, both land here.
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

func runRuns(args []string) error {
	fs := flag.NewFlagSet("runs", flag.ExitOnError)
	attention := fs.Bool("attention", false, "list only runs waiting on a human")
	if err := fs.Parse(args); err != nil {
		return err
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
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "ID\tSTATUS\tHARNESS\tMEMBER\tOVERLAP\tTITLE\tTASK")
		waiting := 0
		for _, r := range rl.Runs {
			if needsAttention(r) {
				waiting++
			}
			if *attention && !needsAttention(r) {
				continue
			}
			title := r.Title
			if title == "" {
				title = r.Task
			}
			title = strings.ReplaceAll(title, "\n", " ")
			task := strings.ReplaceAll(r.Task, "\n", " ")
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				r.ID, r.Status, r.Harness, memberName(r.MemberID), overlapOf[r.ID], title, task)
		}
		if err := tw.Flush(); err != nil {
			return err
		}
		// The notice goes to stderr so it never lands in a pipeline reading
		// the table, and it is skipped when the table already is the answer.
		if waiting > 0 && !*attention {
			_, _ = fmt.Fprintf(os.Stderr, "\n%s waiting on you: aether runs --attention\n",
				plural(waiting, "run"))
		}
		return nil
	})
}
