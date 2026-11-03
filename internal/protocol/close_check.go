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

type RunCloseCheckParams struct {
	RunIDs []string `json:"run_ids"`
}

// RunCloseCheck is one run's warnings, or Error when the run cannot be
// read.
type RunCloseCheck struct {
	RunID    string         `json:"run_id"`
	Warnings []CloseWarning `json:"warnings"`
	Error    *Error         `json:"error,omitempty"`
}

type CloseWarning struct {
	Kind   string   `json:"kind"`
	Detail string   `json:"detail"`
	URL    string   `json:"url,omitempty"`
	Count  int      `json:"count,omitempty"`
	RunIDs []string `json:"run_ids,omitempty"`
}

type RunCloseCheckResult struct {
	Runs []RunCloseCheck `json:"runs"`
}
