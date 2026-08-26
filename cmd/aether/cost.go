package main

import (
	"flag"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/3xDevOps/Aether/internal/protocol"
)

func init() {
	register(command{
		name:  "cost",
		short: "report token usage and cost per member and per run",
		run:   runCost,
	})
}

func runCost(args []string) error {
	fs := flag.NewFlagSet("cost", flag.ExitOnError)
	workspace := fs.String("workspace", "", "workspace ID or name (default: the only workspace)")
	runs := fs.Bool("runs", false, "list every run's usage, not just the per-member split")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return withControl(func(c *protocol.Client) error {
		wsID, err := resolveWorkspace(c, *workspace)
		if err != nil {
			return err
		}
		var res protocol.CostReportResult
		if err := c.Call(protocol.MethodCostReport, protocol.CostReportParams{WorkspaceID: wsID}, &res); err != nil {
			return err
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "MEMBER\tRUNS\tINPUT\tOUTPUT\tCOST\tUNMETERED")
		for _, m := range res.Members {
			_, _ = fmt.Fprintf(tw, "%s\t%d\t%d\t%d\t%s\t%d\n", m.MemberID, m.Rollup.Runs,
				m.Rollup.InputTokens, m.Rollup.OutputTokens, usd(m.Rollup.CostUSD), m.Rollup.Unmetered)
		}
		_, _ = fmt.Fprintf(tw, "TOTAL\t%d\t%d\t%d\t%s\t%d\n", res.Total.Runs,
			res.Total.InputTokens, res.Total.OutputTokens, usd(res.Total.CostUSD), res.Total.Unmetered)
		if *runs {
			_, _ = fmt.Fprintln(tw, "\nRUN\tMEMBER\tINPUT\tOUTPUT\tCOST\tMETERED")
			for _, r := range res.Runs {
				_, _ = fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%s\t%s\n", r.RunID, r.MemberID,
					r.InputTokens, r.OutputTokens, usd(r.CostUSD), meteredLabel(r.Metered))
			}
		}
		if err := tw.Flush(); err != nil {
			return err
		}
		fmt.Println(unmeteredNote(res.Total))
		return nil
	})
}

func usd(v float64) string { return fmt.Sprintf("$%.4f", v) }

func meteredLabel(metered bool) string {
	if metered {
		return "yes"
	}
	return "no"
}

// unmeteredNote states plainly what the totals do and do not cover. Runs
// whose harness has no output adapter are never measured, so their usage
// is unknown rather than zero and every total above is a floor.
func unmeteredNote(total protocol.CostRollup) string {
	if total.Unmetered == 0 {
		if total.Runs == 0 {
			return "no usage recorded for this workspace yet"
		}
		return fmt.Sprintf("all %d runs metered", total.Runs)
	}
	return fmt.Sprintf("%d of %d runs are unmetered (their harness reports no usage): the totals above are a floor, not the real spend",
		total.Unmetered, total.Runs)
}
