package protocol

// MethodRunCloseCheck lists what closing each run would affect. Closing
// stays allowed; the caller shows the warnings before it closes.
const MethodRunCloseCheck = "run.close_check"

const MaxCloseCheckRuns = 50

const (
	CloseWarningProtected      = "protected"
	CloseWarningPullRequest    = "pull_request"
	CloseWarningUnmergedWork   = "unmerged_work"
	CloseWarningMessages       = "messages"
	CloseWarningAwaited        = "awaited"
	CloseWarningSubsessions    = "subsessions"
	CloseWarningCleanupPending = "cleanup_pending"
)

// RunCloseCheckParams.Outcome is the close the caller means: "merged",
// "abandoned", "completed", or empty when not yet chosen. A merged close
// asserts the work is merged, so it skips unmerged_work.
type RunCloseCheckParams struct {
	RunIDs  []string `json:"run_ids"`
	Outcome string   `json:"outcome,omitempty"`
}

type RunCloseCheck struct {
	RunID    string         `json:"run_id"`
	Warnings []CloseWarning `json:"warnings"`
	Error    *Error         `json:"error,omitempty"`
}

// CloseWarning.Subsession names the subsession a warning is about, when
// it is one that closes with the checked run; Detail does not repeat it.
type CloseWarning struct {
	Kind       string   `json:"kind"`
	Detail     string   `json:"detail"`
	URL        string   `json:"url,omitempty"`
	Count      int      `json:"count,omitempty"`
	RunIDs     []string `json:"run_ids,omitempty"`
	Subsession string   `json:"subsession,omitempty"`
}

type RunCloseCheckResult struct {
	Runs []RunCloseCheck `json:"runs"`
}
