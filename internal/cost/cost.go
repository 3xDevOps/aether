// Package cost is token metering, cost rollups, and workspace budgets.
//
// Metering is honest before it is complete. A run whose harness has an
// adapter reports real token counts once, at the adapter's final result
// record (events.RunCostPayload with Metered set). A run without one -
// every PTY-only run - is recorded as unmetered when it reaches a
// terminal status: zero tokens because nobody measured them, never zero
// because the run was free. Rollups carry that distinction through, so a
// total over any unmetered run is a floor and every surface that shows it
// must say so.
//
// Budgets follow from the same honesty. A workspace's cap is checked
// against metered spend only, it refuses new runs and never stops a
// running one, and an admin can override it.
package cost

import (
	"cmp"
	"slices"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/store"
)

// Rollup is aggregated usage over a set of runs. Unmetered counts the
// runs whose usage was never measured; while it is non-zero the totals
// are a lower bound on real spend.
type Rollup struct {
	Runs         int     `json:"runs"`
	Metered      int     `json:"metered_runs"`
	Unmetered    int     `json:"unmetered_runs"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	CostUSD      float64 `json:"cost_usd"`
}

// Add folds one record into the rollup. A run that moved accounts has a
// record per segment; only the counted one, segment 0 on the account it
// launched on, adds to the run counts, while every metered record adds its
// usage.
func (r *Rollup) Add(c *store.RunCost, counted bool) {
	if counted {
		r.Runs++
		if c.Metered {
			r.Metered++
		} else {
			r.Unmetered++
		}
	}
	if !c.Metered {
		return
	}
	r.InputTokens += c.InputTokens
	r.OutputTokens += c.OutputTokens
	r.CostUSD += c.CostUSD
}

// Advisory reports whether the rollup covers unmetered runs, which makes
// its totals - and any budget decided from them - a floor rather than a
// measurement.
func (r Rollup) Advisory() bool { return r.Unmetered > 0 }

// AddSummary folds totals the store already aggregated into the rollup.
func (r *Rollup) AddSummary(s store.RunCostSummary) {
	r.Runs += s.Runs
	r.Metered += s.Metered
	r.Unmetered += s.Unmetered
	r.InputTokens += s.InputTokens
	r.OutputTokens += s.OutputTokens
	r.CostUSD += s.CostUSD
}

// MemberRollup is one member's share of a workspace's usage.
type MemberRollup struct {
	Member domain.MemberID `json:"member"`
	Rollup Rollup          `json:"rollup"`
}

// Report is a workspace's cost attribution: the workspace total, the
// per-member split, and the per-run records behind both.
type Report struct {
	Workspace domain.WorkspaceID
	Total     Rollup
	Members   []MemberRollup
	Runs      []*store.RunCost
}

// Roll aggregates a workspace's run records into a report: one row per
// run, its segments' usage summed under the account it is on now, metered as
// the rollups count it, by segment 0. Members
// are ordered by ID so the output is stable; runs keep the order of their
// first record (the store lists them oldest first).
func Roll(workspace domain.WorkspaceID, records []*store.RunCost) Report {
	rep := Report{Workspace: workspace}
	runs := map[domain.RunID]*store.RunCost{}
	byMember := map[domain.MemberID]*Rollup{}
	for _, c := range records {
		counted := c.Segment == 0
		rep.Total.Add(c, counted)
		m, ok := byMember[c.MemberID]
		if !ok {
			m = &Rollup{}
			byMember[c.MemberID] = m
		}
		m.Add(c, counted)
		run := runs[c.RunID]
		if run == nil {
			run = &store.RunCost{RunID: c.RunID, WorkspaceID: c.WorkspaceID, MemberID: c.RunAccountMemberID}
			if run.MemberID == "" {
				run.MemberID = c.MemberID
			}
			runs[c.RunID] = run
			rep.Runs = append(rep.Runs, run)
		}
		if counted {
			run.Metered = c.Metered
		}
		if c.Metered {
			run.InputTokens += c.InputTokens
			run.OutputTokens += c.OutputTokens
			run.CostUSD += c.CostUSD
		}
		if c.RecordedAt.After(run.RecordedAt) {
			run.RecordedAt = c.RecordedAt
		}
	}
	for member, roll := range byMember {
		rep.Members = append(rep.Members, MemberRollup{Member: member, Rollup: *roll})
	}
	slices.SortFunc(rep.Members, func(a, b MemberRollup) int {
		return cmp.Compare(a.Member, b.Member)
	})
	return rep
}

// foldDeleted adds the totals of deleted runs. A member whose runs are all
// deleted still gets an entry.
func (rep *Report) foldDeleted(deleted []*store.MemberCostSummary) {
	if len(deleted) == 0 {
		return
	}
	index := make(map[domain.MemberID]int, len(rep.Members))
	for i, m := range rep.Members {
		index[m.Member] = i
	}
	for _, d := range deleted {
		rep.Total.AddSummary(d.RunCostSummary)
		i, ok := index[d.MemberID]
		if !ok {
			rep.Members = append(rep.Members, MemberRollup{Member: d.MemberID})
			i = len(rep.Members) - 1
			index[d.MemberID] = i
		}
		rep.Members[i].Rollup.AddSummary(d.RunCostSummary)
	}
	slices.SortFunc(rep.Members, func(a, b MemberRollup) int {
		return cmp.Compare(a.Member, b.Member)
	})
}
