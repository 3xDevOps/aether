package events

// TypeBudget reports a workspace budget's state: spend crossing the warning
// threshold, spend reaching the cap, a new run refused at the cap, and an
// admin changing the budget. It never means a run was stopped - budgets
// gate admission only.
const TypeBudget Type = "workspace.budget"

// BudgetState is where a workspace's spend sits relative to its budget.
type BudgetState string

const (
	// BudgetOK is spend below the warning threshold, or no budget set.
	BudgetOK BudgetState = "ok"
	// BudgetWarn is spend at or past the soft warning threshold.
	BudgetWarn BudgetState = "warn"
	// BudgetExceeded is spend at or past the cap: new runs are refused
	// unless the budget carries an admin override.
	BudgetExceeded BudgetState = "exceeded"
)

// BudgetPayload reports a workspace budget's state and the spend behind it.
//
// UnmeteredRuns counts the workspace's runs whose usage was never metered
// (no harness adapter). While it is non-zero SpendUSD is a floor, not the
// true spend, and the budget is advisory: consumers must say so rather
// than present the number as exact.
type BudgetPayload struct {
	State         BudgetState `json:"state"`
	SpendUSD      float64     `json:"spend_usd"`
	LimitUSD      float64     `json:"limit_usd"`
	WarnUSD       float64     `json:"warn_usd,omitempty"`
	Override      bool        `json:"override,omitempty"`
	UnmeteredRuns int         `json:"unmetered_runs,omitempty"`
	// Reason is a human-readable cause when the event marks something
	// other than a threshold crossing: a refused run, an admin edit.
	Reason string `json:"reason,omitempty"`
}

func (BudgetPayload) EventType() Type { return TypeBudget }

func init() { registerPayload[BudgetPayload](TypeBudget) }
