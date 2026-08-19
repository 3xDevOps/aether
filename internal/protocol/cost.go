package protocol

// Wave 4 cost-attribution and session-budget methods.
const (
	// MethodCostReport rolls a session's recorded token usage up per run
	// and per member.
	MethodCostReport = "cost.report"
	// MethodBudgetGet reports a session's budget and current spend.
	MethodBudgetGet = "budget.get"
	// MethodBudgetSet sets, changes, or clears a session's budget
	// (session administration).
	MethodBudgetSet = "budget.set"
)

// CostRollup is aggregated usage over a set of runs. Unmetered counts
// runs whose usage was never measured - a harness with no adapter reports
// nothing - so while it is non-zero every total here is a floor, not the
// real spend.
type CostRollup struct {
	Runs         int     `json:"runs"`
	Metered      int     `json:"metered_runs"`
	Unmetered    int     `json:"unmetered_runs"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	CostUSD      float64 `json:"cost_usd"`
}

// RunCost is one run's usage record. Metered false means the run's usage
// is unknown, not zero.
type RunCost struct {
	RunID        string  `json:"run_id"`
	MemberID     string  `json:"member_id"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	CostUSD      float64 `json:"cost_usd"`
	Metered      bool    `json:"metered"`
	RecordedAt   string  `json:"recorded_at"`
}

// MemberCost is one member's share of a session's usage.
type MemberCost struct {
	MemberID string     `json:"member_id"`
	Rollup   CostRollup `json:"rollup"`
}

// CostReportParams selects the session to report on.
type CostReportParams struct {
	SessionID string `json:"session_id"`
}

// CostReportResult is the session total, the per-member split, and the
// per-run records behind them.
type CostReportResult struct {
	SessionID string       `json:"session_id"`
	Total     CostRollup   `json:"total"`
	Members   []MemberCost `json:"members"`
	Runs      []RunCost    `json:"runs"`
}

// Budget is a session's spend cap. WarnUSD of 0 means no soft warning;
// Override is an admin's standing permission for new runs to start past
// the cap.
type Budget struct {
	SessionID string  `json:"session_id"`
	LimitUSD  float64 `json:"limit_usd"`
	WarnUSD   float64 `json:"warn_usd,omitempty"`
	Override  bool    `json:"override,omitempty"`
	UpdatedBy string  `json:"updated_by,omitempty"`
	UpdatedAt string  `json:"updated_at,omitempty"`
}

// BudgetGetParams selects the session whose budget to report.
type BudgetGetParams struct {
	SessionID string `json:"session_id"`
}

// BudgetSetParams changes a session's budget. A LimitUSD of zero or less
// clears it.
type BudgetSetParams struct {
	SessionID string  `json:"session_id"`
	LimitUSD  float64 `json:"limit_usd"`
	WarnUSD   float64 `json:"warn_usd,omitempty"`
	Override  bool    `json:"override,omitempty"`
}

// BudgetResult is a session's budget with its current state ("ok",
// "warn", "exceeded") and the spend behind it. Budget is absent when the
// session has no budget. Advisory is true when part of the spend is
// unmetered, which makes the state a floor rather than a measurement.
type BudgetResult struct {
	SessionID string     `json:"session_id"`
	Budget    *Budget    `json:"budget,omitempty"`
	State     string     `json:"state"`
	Spend     CostRollup `json:"spend"`
	Advisory  bool       `json:"advisory,omitempty"`
}
