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
	ACPHistoryMaxLimit = 500

	// ErrorReasonAlreadyAnswered is the data.reason of the CodeConflict
	// run.input.answer returns when another answer won.
	ErrorReasonAlreadyAnswered = "already_answered"
)

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
	Frames []ACPFrame `json:"frames"`
}

type RunACPItemParams struct {
	RunID string `json:"run_id"`
	Seq   int64  `json:"seq"`
}

type RunACPItemResult struct {
	Item json.RawMessage `json:"item"`
}
