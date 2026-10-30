package protocol

// MethodInsightsGet aggregates a member's or the whole server's run outcomes
// over a date range. See docs/insights.md.
const MethodInsightsGet = "insights.get"

const (
	InsightsScopeMine     = "mine"
	InsightsScopeEveryone = "everyone"
)

// InsightsParams selects the range [from, to), at most 400 days, the
// scope, and the IANA time zone days are bucketed in ("" is UTC). An empty
// workspace_id selects every workspace.
type InsightsParams struct {
	WorkspaceID string `json:"workspace_id"`
	Scope       string `json:"scope"`
	From        string `json:"from"`
	To          string `json:"to"`
	TimeZone    string `json:"time_zone"`
}

type InsightsResult struct {
	GeneratedAt  string                `json:"generated_at"`
	From         string                `json:"from"`
	To           string                `json:"to"`
	TimeZone     string                `json:"time_zone"`
	Bucket       string                `json:"bucket"`
	Totals       InsightsTotals        `json:"totals"`
	Previous     InsightsTotals        `json:"previous"`
	Series       []InsightsSeriesPoint `json:"series"`
	Heatmap      []InsightsHeatmapDay  `json:"heatmap"`
	ByWorkspace  []InsightsWorkspace   `json:"by_workspace"`
	ByAgent      []InsightsAgent       `json:"by_agent"`
	Durations    InsightsDurations     `json:"durations"`
	Cost         InsightsCost          `json:"cost"`
	PullRequests InsightsPullRequests  `json:"pull_requests"`
	Success      InsightsSuccess       `json:"success"`
	Outcomes     InsightsOutcomes      `json:"outcomes"`
	Recent       []InsightsRecentRun   `json:"recent"`
	NeedsYou     int                   `json:"needs_you"`
	Coverage     InsightsCoverage      `json:"coverage"`
}

type InsightsTotals struct {
	Launched    int `json:"launched"`
	Completed   int `json:"completed"`
	Merged      int `json:"merged"`
	Failed      int `json:"failed"`
	Interrupted int `json:"interrupted"`
	Abandoned   int `json:"abandoned"`
	Killed      int `json:"killed"`
	Active      int `json:"active"`
	Finished    int `json:"finished"`
}

// InsightsSeriesPoint is one bucket; Start is its local date.
type InsightsSeriesPoint struct {
	Start       string `json:"start"`
	Launched    int    `json:"launched"`
	Completed   int    `json:"completed"`
	Merged      int    `json:"merged"`
	Failed      int    `json:"failed"`
	Interrupted int    `json:"interrupted"`
	Abandoned   int    `json:"abandoned"`
}

type InsightsHeatmapDay struct {
	Date string `json:"date"`
	Runs int    `json:"runs"`
}

type InsightsWorkspace struct {
	WorkspaceID string `json:"workspace_id"`
	Name        string `json:"name"`
	Launched    int    `json:"launched"`
	Finished    int    `json:"finished"`
	Failed      int    `json:"failed"`
}

// InsightsAgent groups by agent and model; an empty model is the agent's
// default.
type InsightsAgent struct {
	Harness  string `json:"harness"`
	Model    string `json:"model"`
	Launched int    `json:"launched"`
	Finished int    `json:"finished"`
	Failed   int    `json:"failed"`
}

type InsightsDurations struct {
	RunsMeasured          int   `json:"runs_measured"`
	MedianRunSeconds      int64 `json:"median_run_seconds"`
	P90RunSeconds         int64 `json:"p90_run_seconds"`
	StateRunsMeasured     int   `json:"state_runs_measured"`
	MedianWorkingSeconds  int64 `json:"median_working_seconds"`
	MedianWaitingSeconds  int64 `json:"median_waiting_seconds"`
	MedianResponseSeconds int64 `json:"median_response_seconds"`
}

// InsightsCost is a floor: an unmetered run's cost is unknown, not zero.
type InsightsCost struct {
	MeteredRuns   int                 `json:"metered_runs"`
	UnmeteredRuns int                 `json:"unmetered_runs"`
	CostUSD       float64             `json:"cost_usd"`
	InputTokens   int64               `json:"input_tokens"`
	OutputTokens  int64               `json:"output_tokens"`
	TotalTokens   int64               `json:"total_tokens"`
	ByAgent       []InsightsCostAgent `json:"by_agent"`
}

// InsightsCostAgent is InsightsCost for one harness, largest TotalTokens
// first. A harness whose runs are all unmetered is listed with zero tokens.
type InsightsCostAgent struct {
	Harness       string  `json:"harness"`
	MeteredRuns   int     `json:"metered_runs"`
	UnmeteredRuns int     `json:"unmetered_runs"`
	CostUSD       float64 `json:"cost_usd"`
	InputTokens   int64   `json:"input_tokens"`
	OutputTokens  int64   `json:"output_tokens"`
	TotalTokens   int64   `json:"total_tokens"`
}

// InsightsOutcome is how runs finished. See docs/insights.md for
// which statuses count as which.
type InsightsOutcome struct {
	Done      int `json:"done"`
	Failed    int `json:"failed"`
	Abandoned int `json:"abandoned"`
}

// InsightsOutcomes is the outcome totals and the outcomes per Series bucket,
// in the same order. Members lists every member for scope "everyone" and is
// empty for "mine".
type InsightsOutcomes struct {
	Runs    InsightsOutcome         `json:"runs"`
	Series  []InsightsOutcomeBucket `json:"series"`
	Members []InsightsMemberOutcome `json:"members"`
}

type InsightsOutcomeBucket struct {
	Start string          `json:"start"`
	Runs  InsightsOutcome `json:"runs"`
}

// InsightsMemberOutcome is one member's share of InsightsOutcomes. Color is
// the member's profile color; MemberID never changes and is the key to
// assign chart colors by.
type InsightsMemberOutcome struct {
	MemberID    string                  `json:"member_id"`
	DisplayName string                  `json:"display_name"`
	Color       string                  `json:"color"`
	Runs        InsightsOutcome         `json:"runs"`
	Series      []InsightsOutcomeBucket `json:"series"`
}

// InsightsSuccess counts what finished in the range and how much of it ended
// with a merged pull request.
type InsightsSuccess struct {
	Runs InsightsSuccessCount `json:"runs"`
}

type InsightsSuccessCount struct {
	Succeeded int `json:"succeeded"`
	Finished  int `json:"finished"`
}

type InsightsPullRequests struct {
	Opened int `json:"opened"`
	Merged int `json:"merged"`
}

type InsightsRecentRun struct {
	RunID           string `json:"run_id"`
	Title           string `json:"title"`
	WorkspaceID     string `json:"workspace_id"`
	Status          string `json:"status"`
	Reason          string `json:"reason"`
	Harness         string `json:"harness"`
	Model           string `json:"model"`
	CreatedAt       string `json:"created_at"`
	FinishedAt      string `json:"finished_at"`
	DurationSeconds int64  `json:"duration_seconds"`
	PRURL           string `json:"pr_url"`
	PRState         string `json:"pr_state"`
}

// InsightsCoverage is when the server started recording what older
// versions did not; "" means nothing is recorded yet.
type InsightsCoverage struct {
	StateTimesSince   string `json:"state_times_since"`
	PullRequestsSince string `json:"pull_requests_since"`
}
