package cost

import (
	"errors"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/store"
)

// ErrBudgetExceeded is why a new run was refused: the workspace is at
// its cap and carries no admin override. Refusals wrap it together with
// permissions.ErrDenied, which is what maps the refusal to a denial on
// the wire - a cap is policy, not an internal failure.
var ErrBudgetExceeded = errors.New("workspace budget exceeded")

// Change is an admin's edit to a workspace budget. LimitUSD of zero or less
// clears the budget entirely; WarnUSD of zero means no warning threshold.
type Change struct {
	LimitUSD float64
	WarnUSD  float64
	Override bool
}

// Status is a workspace's budget and where its spend currently sits.
// Budget is nil when the workspace has no budget, in which case State is
// always BudgetOK.
type Status struct {
	Workspace domain.WorkspaceID
	Budget    *store.WorkspaceBudget
	State     events.BudgetState
	Spend     Rollup
}

// Evaluate places spend against a budget. A workspace with no budget, or
// a non-positive limit, is always ok. Only metered spend counts: unmetered
// runs contribute nothing to Rollup.CostUSD, so the state is a floor and
// Spend.Advisory reports when that matters.
func Evaluate(b *store.WorkspaceBudget, spend Rollup) events.BudgetState {
	if b == nil || b.LimitUSD <= 0 {
		return events.BudgetOK
	}
	switch {
	case spend.CostUSD >= b.LimitUSD:
		return events.BudgetExceeded
	case b.WarnUSD > 0 && spend.CostUSD >= b.WarnUSD:
		return events.BudgetWarn
	}
	return events.BudgetOK
}

// Admits reports whether a new run may start under this status: anything
// short of the cap, or the cap with an admin override on it.
func (s Status) Admits() bool {
	return s.State != events.BudgetExceeded || (s.Budget != nil && s.Budget.Override)
}

// payload renders the status as the bus event, with reason attached.
func (s Status) payload(reason string) events.BudgetPayload {
	p := events.BudgetPayload{
		State:         s.State,
		SpendUSD:      s.Spend.CostUSD,
		UnmeteredRuns: s.Spend.Unmetered,
		Reason:        reason,
	}
	if s.Budget != nil {
		p.LimitUSD = s.Budget.LimitUSD
		p.WarnUSD = s.Budget.WarnUSD
		p.Override = s.Budget.Override
	}
	return p
}
