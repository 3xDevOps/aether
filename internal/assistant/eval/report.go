package eval

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// Summary renders results as Markdown: a row per scenario with its pass rate
// and counted failure modes, the checks that failed, and the briefs and
// replies a person should read.
func Summary(label string, scenarios []Scenario, results []Result) string {
	w := &strings.Builder{}
	fmt.Fprintf(w, "# Assistant eval: %s\n\n", label)
	fmt.Fprintln(w, "| scenario | capability | pass | questions | brief offers | bad launches | must-not dropped | invented | tool calls | latency | cost |")
	fmt.Fprintln(w, "| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |")
	var pass, total int
	for _, s := range scenarios {
		var rs []Result
		for _, r := range results {
			if r.Scenario == s.ID {
				rs = append(rs, r)
			}
		}
		if len(rs) == 0 {
			continue
		}
		var p, calls int
		var f Flags
		var lat time.Duration
		var cost float64
		costed := 0
		for _, r := range rs {
			if r.Pass {
				p++
			}
			calls += len(r.Calls)
			lat += r.Latency
			f.UnnecessaryQuestions += r.Flags.UnnecessaryQuestions
			f.OfferedFollowUps += r.Flags.OfferedFollowUps
			f.UnwantedLaunches += r.Flags.UnwantedLaunches
			f.MustNotDropped += r.Flags.MustNotDropped
			f.InventedScope += r.Flags.InventedScope
			if r.CostUSD != nil {
				cost += *r.CostUSD
				costed++
			}
		}
		capability := "available"
		if pending := s.Pending(); len(pending) > 0 {
			capability = "pending: " + strings.Join(pending, ", ")
		} else {
			pass += p
			total += len(rs)
		}
		n := len(rs)
		costCol := "not measured"
		if costed > 0 {
			costCol = fmt.Sprintf("$%.3f", cost/float64(costed))
		}
		fmt.Fprintf(w, "| %s | %s | %d/%d | %d | %d | %d | %d | %d | %d | %s | %s |\n", s.ID, capability, p, n,
			f.UnnecessaryQuestions, f.OfferedFollowUps, f.UnwantedLaunches, f.MustNotDropped, f.InventedScope,
			calls/n, (lat / time.Duration(n)).Round(time.Second), costCol)
	}
	fmt.Fprintf(w, "\nScenarios whose capability exists: %d/%d runs passed. Rows marked pending use an eval-only stub of a tool the registry does not offer yet; their rate is a baseline for that stub, not product behavior. Counts are totals over the runs; tool calls, latency and cost are per-run means.\n", pass, total)

	fmt.Fprintln(w, "\n## Failed checks")
	for _, r := range results {
		var bad []string
		for _, c := range r.Checks {
			if !c.Pass {
				bad = append(bad, strings.TrimSpace(c.Name+" "+c.Detail))
			}
		}
		if r.Error != "" {
			bad = append(bad, "error: "+r.Error)
		}
		if len(bad) > 0 {
			fmt.Fprintf(w, "- %s #%d: %s\n", r.Scenario, r.Run, strings.Join(bad, "; "))
		}
	}

	fmt.Fprintln(w, "\n## Manual review")
	for _, s := range scenarios {
		if len(s.Review) == 0 || !slices.ContainsFunc(results, func(r Result) bool { return r.Scenario == s.ID }) {
			continue
		}
		fmt.Fprintf(w, "\n### %s\n", s.ID)
		for _, q := range s.Review {
			fmt.Fprintf(w, "- [ ] %s\n", q)
		}
		for _, r := range results {
			if r.Scenario != s.ID {
				continue
			}
			for _, b := range r.Briefs {
				fmt.Fprintf(w, "- run %d brief: %q\n", r.Run, b)
			}
			fmt.Fprintf(w, "- run %d reply: %q\n", r.Run, strings.Join(strings.Fields(r.Answer), " "))
		}
	}
	return w.String()
}
