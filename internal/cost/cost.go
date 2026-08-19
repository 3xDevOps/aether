// Package cost is token metering, cost rollups, and session budgets.
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
// Budgets follow from the same honesty. A session's cap is checked
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

// Add folds one run's record into the rollup.
func (r *Rollup) Add(c *store.RunCost) {
	r.Runs++
	if !c.Metered {
		r.Unmetered++
		return
	}
	r.Metered++
	r.InputTokens += c.InputTokens
	r.OutputTokens += c.OutputTokens
	r.CostUSD += c.CostUSD
}

// Advisory reports whether the rollup covers unmetered runs, which makes
// its totals - and any budget decided from them - a floor rather than a
// measurement.
func (r Rollup) Advisory() bool { return r.Unmetered > 0 }

// MemberRollup is one member's share of a session's usage.
type MemberRollup struct {
	Member domain.MemberID `json:"member"`
	Rollup Rollup          `json:"rollup"`
}

// Report is a session's cost attribution: the session total, the
// per-member split, and the per-run records behind both.
type Report struct {
	Session domain.SessionID
	Total   Rollup
	Members []MemberRollup
	Runs    []*store.RunCost
}

// Roll aggregates a session's run records into a report. Members are
// ordered by ID so the output is stable; records are returned in the
// order given (the store lists them oldest first).
func Roll(session domain.SessionID, records []*store.RunCost) Report {
	rep := Report{Session: session, Runs: records}
	byMember := map[domain.MemberID]*Rollup{}
	for _, c := range records {
		rep.Total.Add(c)
		m, ok := byMember[c.MemberID]
		if !ok {
			m = &Rollup{}
			byMember[c.MemberID] = m
		}
		m.Add(c)
	}
	for member, roll := range byMember {
		rep.Members = append(rep.Members, MemberRollup{Member: member, Rollup: *roll})
	}
	slices.SortFunc(rep.Members, func(a, b MemberRollup) int {
		return cmp.Compare(a.Member, b.Member)
	})
	return rep
}
