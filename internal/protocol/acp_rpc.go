package protocol

import "encoding/json"

// Methods on an enhanced run's agent session. Answer, cancel and set_option
// need Steer and the run's control lease; history and item need View.
const (
	MethodRunInputAnswer  = "run.input.answer"
	MethodRunACPCancel    = "run.acp.cancel"
	MethodRunACPSetOption = "run.acp.set_option"
	MethodRunACPHistory   = "run.acp.history"
	MethodRunACPItem      = "run.acp.item"

	// ACPHistoryMaxLimit caps one run.acp.history page.
	ACPHistoryMaxLimit = 1000

	// ErrorReasonAlreadyAnswered is the data.reason of the CodeConflict
	// run.input.answer returns when another answer won.
	ErrorReasonAlreadyAnswered = "already_answered"

	// MethodRunModeSwitch moves a live run between Standard (tui) and
	// Enhanced (acp). It needs Steer and, while anyone holds the run's
	// control, that lease.
	MethodRunModeSwitch = "run.mode.switch"
	// The data.reason of the CodeInvalidState run.mode.switch returns for an
	// agent that cannot switch, a run whose agent has not reported its
	// session yet, and a switch to Enhanced without the agent's ACP server.
	ErrorReasonNotSwitchable       = "not_switchable"
	ErrorReasonSessionNotReported  = "session_not_reported"
	ErrorReasonAdapterNotInstalled = "adapter_not_installed"
)

// RunModeSwitchParams.Mode is "tui" or "acp". The lease may be empty while
// nobody holds the run's control. The result is a RunResult.
type RunModeSwitchParams struct {
	RunID string `json:"run_id"`
	Mode  string `json:"mode"`
	ACPLease
}

// ACPLease is the control lease proof every enhanced-run input carries.
type ACPLease struct {
	ControlSessionID  string `json:"control_session_id"`
	ControlGeneration uint64 `json:"control_generation"`
}

// RunInputAnswerParams answers a pending permission request or question
// with one of its option ids.
type RunInputAnswerParams struct {
	RunID     string `json:"run_id"`
	RequestID string `json:"request_id"`
	OptionID  string `json:"option_id"`
	// Values is the form answer for an accepted question.
	Values map[string]any `json:"values,omitempty"`
	ACPLease
}

// RunACPCancelParams stops the agent's running turn.
type RunACPCancelParams struct {
	RunID string `json:"run_id"`
	ACPLease
}

// RunACPSetOptionParams sets a config option; Value is a value id string or
// a boolean.
type RunACPSetOptionParams struct {
	RunID    string          `json:"run_id"`
	OptionID string          `json:"option_id"`
	Value    json.RawMessage `json:"value"`
	ACPLease
}

// RunACPHistoryParams pages backwards: BeforeSeq zero reads from the newest
// item.
type RunACPHistoryParams struct {
	RunID     string `json:"run_id"`
	BeforeSeq int64  `json:"before_seq,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}

// RunACPHistoryResult is one page, oldest first, as stream frames.
type RunACPHistoryResult struct {
	Frames          []ACPFrame `json:"frames"`
	OldestSeq       int64      `json:"oldest_seq"`
	TruncatedBefore bool       `json:"truncated_before,omitempty"`
}

type RunACPItemParams struct {
	RunID string `json:"run_id"`
	Seq   int64  `json:"seq"`
}

type RunACPItemResult struct {
	Item json.RawMessage `json:"item"`
}
