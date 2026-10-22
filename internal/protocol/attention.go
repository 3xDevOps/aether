package protocol

import "encoding/json"

const (
	// MethodAttentionList lists what needs the caller: the dashboard's Needs
	// you rows. It needs View.
	MethodAttentionList = "attention.list"
	// MethodRunInputRespond answers an enhanced run's pending permission
	// request or question without a control lease. It needs Steer, and is
	// refused while another member holds the run's control.
	MethodRunInputRespond = "run.input.respond"

	// The data.reason of the CodeConflict run.input.respond returns while
	// another member holds the run's control.
	ErrorReasonControlHeld = "control_held"

	RunInputRespondAnswered        = "answered"
	RunInputRespondAlreadyAnswered = "already_answered"
)

// Attention item kinds, in the order a run is checked: a run is listed
// under the first kind that holds for it.
const (
	AttentionKindApproval       = "approval"
	AttentionKindPermission     = "permission"
	AttentionKindQuestion       = "question"
	AttentionKindQueuedMessage  = "queued-message"
	AttentionKindRoomQuestion   = "room-question"
	AttentionKindSwarmQuestion  = "swarm-question"
	AttentionKindIntegratorDown = "integrator-down"
	AttentionKindControlHold    = "control-hold"
	AttentionKindDisconnected   = "disconnected"
	AttentionKindBlocked        = "blocked"
	AttentionKindNeedsInput     = "needs-input"
	AttentionKindReady          = "ready"
	AttentionKindFailed         = "failed"
)

// AttentionListParams narrows the list to one workspace; empty lists every
// workspace.
type AttentionListParams struct {
	WorkspaceID string `json:"workspace_id,omitempty"`
}

type AttentionListResult struct {
	Items []AttentionItem `json:"items"`
}

// AttentionItem is one run that needs the caller. Reason is worded as the
// dashboard's Needs you row. Since is RFC 3339, empty when unknown. The
// field matching Kind carries what answering needs: Request for permission
// and question, Approval for approval, Message for swarm-question,
// room-question and queued-message.
type AttentionItem struct {
	Kind        string             `json:"kind"`
	RunID       string             `json:"run_id"`
	MissionID   string             `json:"mission_id,omitempty"`
	WorkspaceID string             `json:"workspace_id"`
	Title       string             `json:"title"`
	Harness     string             `json:"harness"`
	Reason      string             `json:"reason"`
	Since       string             `json:"since,omitempty"`
	Request     *AttentionRequest  `json:"request,omitempty"`
	Approval    *AttentionApproval `json:"approval,omitempty"`
	Message     *AttentionMessage  `json:"message,omitempty"`
}

// AttentionRequest is an enhanced run's pending request, answered with
// run.input.respond. A standard run's request is answered in its terminal
// and has no Options.
type AttentionRequest struct {
	ID      string            `json:"id"`
	Kind    string            `json:"kind"`
	Title   string            `json:"title,omitempty"`
	Options []AttentionOption `json:"options,omitempty"`
	// Schema is the form a question's accept option fills, as values.
	Schema json.RawMessage `json:"schema,omitempty"`
	// URL is the page a link request asks the person to open; it is set
	// only on a link request, which is otherwise a question.
	URL string `json:"url,omitempty"`
}

// AttentionOption.Kind is the ACP permission option kind: allow_once,
// allow_always, reject_once or reject_always; empty for a question.
type AttentionOption struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Kind  string `json:"kind,omitempty"`
}

// AttentionApproval is decided with approval.decide.
type AttentionApproval struct {
	ID     string `json:"id"`
	Action string `json:"action"`
	Detail string `json:"detail,omitempty"`
}

// AttentionMessage is a swarm question (answered with
// mission.question.answer), a room question to the run's owner (answered
// with a run.room.post reply), or a queued message its controller decides
// with run.room.decide. From is the sender's member id; empty for a swarm
// question, which the integrator asks.
type AttentionMessage struct {
	ID   string `json:"id"`
	Body string `json:"body"`
	From string `json:"from,omitempty"`
}

// RunInputRespondParams is RunInputAnswerParams without the lease.
type RunInputRespondParams struct {
	RunID     string         `json:"run_id"`
	RequestID string         `json:"request_id"`
	OptionID  string         `json:"option_id"`
	Values    map[string]any `json:"values,omitempty"`
}

// RunInputRespondResult.Status is RunInputRespondAnswered, or
// RunInputRespondAlreadyAnswered when an earlier answer won.
type RunInputRespondResult struct {
	Status string `json:"status"`
}
