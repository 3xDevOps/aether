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

// RunCloseCheck.MergedPullRequest reports that a merged pull request of
// the run's own repository holds its last commit, so a close can record it
// as merged; IdleSince is when its status, reason or activity last changed.
type RunCloseCheck struct {
	RunID             string         `json:"run_id"`
	Warnings          []CloseWarning `json:"warnings"`
	MergedPullRequest bool           `json:"merged_pull_request"`
	IdleSince         string         `json:"idle_since,omitempty"`
	Error             *Error         `json:"error,omitempty"`
}

// CloseWarning.Subsession names the subsession a warning is about, when
// it is one that closes with the checked run; Detail does not repeat it.
// A pull_request warning also carries the pull request's Number and when
// Aether last observed it.
type CloseWarning struct {
	Kind       string   `json:"kind"`
	Detail     string   `json:"detail"`
	URL        string   `json:"url,omitempty"`
	Count      int      `json:"count,omitempty"`
	RunIDs     []string `json:"run_ids,omitempty"`
	Subsession string   `json:"subsession,omitempty"`
	Number     int      `json:"number,omitempty"`
	ObservedAt string   `json:"observed_at,omitempty"`
}

type RunCloseCheckResult struct {
	Runs []RunCloseCheck `json:"runs"`
}
