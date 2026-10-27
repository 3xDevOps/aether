package protocol

import (
	"encoding/json"
	"time"
)

const (
	MethodAssistantStatus  = "assistant.status"
	MethodAssistantStart   = "assistant.start"
	MethodAssistantPrompt  = "assistant.prompt"
	MethodAssistantCancel  = "assistant.cancel"
	MethodAssistantAnswer  = "assistant.answer"
	MethodAssistantConfirm = "assistant.confirm"
	MethodAssistantReset   = "assistant.reset"
	MethodAssistantHistory = "assistant.history"
	MethodAssistantItem    = "assistant.item"
	// MethodAssistantSettings reads the caller's assistant settings, or
	// changes them with a field set. The assistant's tool socket does not
	// serve it.
	MethodAssistantSettings = "assistant.settings"
	// MethodAssistantDocs is served only on the assistant tool socket.
	MethodAssistantDocs = "assistant.docs"
	// MethodAssistantView offers a navigation update as the caller's held
	// view context.
	MethodAssistantView = "assistant.view"
	// MethodAssistantViewContext is served only on the assistant tool socket.
	MethodAssistantViewContext = "assistant.view_context"
)

const (
	AssistantStateSetup        = "setup"
	AssistantStateNeedsRestart = "needs_restart"
	AssistantStateIdle         = "idle"
	AssistantStateStarting     = "starting"
	AssistantStateRunning      = "running"
	AssistantStateFailed       = "failed"
)

type AssistantAgent struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Ready       bool   `json:"ready"`
	// Reason says why the agent is not ready, for example that its Enhanced
	// adapter is not installed or no login was found.
	Reason string `json:"reason,omitempty"`
}

type AssistantConfirmation struct {
	ID        string          `json:"id"`
	Method    string          `json:"method"`
	Summary   string          `json:"summary"`
	Params    json.RawMessage `json:"params"`
	CreatedAt time.Time       `json:"created_at"`
	ExpiresAt time.Time       `json:"expires_at"`
	// OtherMember means the run the call acts on belongs to another
	// member, named by OtherOwner. Such a call shows a card even with
	// auto-approval on.
	OtherMember bool   `json:"other_member,omitempty"`
	OtherOwner  string `json:"other_owner,omitempty"`
}

type AssistantStatus struct {
	State         string                  `json:"state"`
	Agent         string                  `json:"agent,omitempty"`
	Agents        []AssistantAgent        `json:"agents"`
	Error         string                  `json:"error,omitempty"`
	Confirmations []AssistantConfirmation `json:"confirmations"`
	AutoApprove   bool                    `json:"auto_approve"`
	ShareView     bool                    `json:"share_view"`
	// SuggestAutoApprove means the member approved enough cards by hand
	// that the dashboard may suggest turning auto-approval on.
	SuggestAutoApprove bool `json:"suggest_auto_approve"`
}

// Answers to the suggestion to turn auto-approval on.
const (
	// AssistantSuggestionLater snoozes it: 7 days the first time, 30 the
	// second; the third counts as never.
	AssistantSuggestionLater = "later"
	AssistantSuggestionNever = "never"
)

// AssistantSettingsParams changes each field that is set; none reads.
type AssistantSettingsParams struct {
	AutoApprove *bool `json:"auto_approve,omitempty"`
	ShareView   *bool `json:"share_view,omitempty"`
	// Suggestion answers the suggestion to turn auto-approval on.
	Suggestion string `json:"suggestion,omitempty"`
}

type AssistantSettings struct {
	// AutoApprove runs confirmed tools without a card, as if approved.
	AutoApprove        bool `json:"auto_approve"`
	SuggestAutoApprove bool `json:"suggest_auto_approve"`
	// SuggestionOff means the suggestion will not show again.
	SuggestionOff bool `json:"suggestion_off"`
	// ShareView sends the assistant the member's dashboard view context.
	ShareView bool `json:"share_view"`
}

type AssistantStartParams struct {
	Agent              string `json:"agent,omitempty"`
	RestartEnvironment bool   `json:"restart_environment,omitempty"`
}

type AssistantPromptParams struct {
	Text           string                `json:"text"`
	IdempotencyKey string                `json:"idempotency_key"`
	ViewContext    *AssistantViewContext `json:"view_context,omitempty"`
}

type AssistantViewParams struct {
	ViewContext *AssistantViewContext `json:"view_context,omitempty"`
}

// AssistantViewContext is what the member's dashboard shows: ids and view
// names only, never titles, file contents, drafts or terminal text. Its ids
// are hints for the assistant's member-scoped tools, never permissions. A
// prompt with the page removed has no route; Attachments are what the member
// pointed the message at. LaunchDefaults travel with the page.
type AssistantViewContext struct {
	Version        int                       `json:"version"`
	CapturedAt     time.Time                 `json:"captured_at"`
	Route          AssistantViewEntry        `json:"route,omitzero"`
	OpenFile       *AssistantViewFile        `json:"open_file,omitempty"`
	Recent         []AssistantViewEntry      `json:"recent,omitempty"`
	LaunchDefaults []AssistantLaunchDefault  `json:"launch_defaults,omitempty"`
	Attachments    []AssistantViewAttachment `json:"attachments,omitempty"`
}

// AssistantLaunchDefault is a mode and model the member's dashboard
// remembers for an agent from their own launches, newest first.
type AssistantLaunchDefault struct {
	Agent string `json:"agent"`
	Mode  string `json:"mode"`
	Model string `json:"model,omitempty"`
}

// AssistantViewAttachment names one thing a prompt is about. Kind is
// workspace, run, mission, pr, check, failure or task; the other fields say
// which.
type AssistantViewAttachment struct {
	Kind        string `json:"kind"`
	WorkspaceID string `json:"workspace_id,omitempty"`
	RunID       string `json:"run_id,omitempty"`
	MissionID   string `json:"mission_id,omitempty"`
	TaskID      string `json:"task_id,omitempty"`
	AttemptID   string `json:"attempt_id,omitempty"`
	PR          int    `json:"pr,omitempty"`
	Repository  string `json:"repository,omitempty"`
	Check       string `json:"check,omitempty"`
	Failure     string `json:"failure,omitempty"`
	Seq         int64  `json:"seq,omitempty"`
}

type AssistantViewEntry struct {
	Name        string    `json:"name"`
	WorkspaceID string    `json:"workspace_id,omitempty"`
	RunID       string    `json:"run_id,omitempty"`
	MissionID   string    `json:"mission_id,omitempty"`
	Subagent    string    `json:"subagent,omitempty"`
	View        string    `json:"view,omitempty"`
	At          time.Time `json:"at,omitzero"`
}

type AssistantViewFile struct {
	Kind        string `json:"kind"`
	Path        string `json:"path"`
	RunID       string `json:"run_id,omitempty"`
	WorkspaceID string `json:"workspace_id,omitempty"`
}

// AssistantViewContextResult is the member's held view context, shared by
// all their dashboards. A sent prompt's view always becomes it. A navigation
// update captured before the held view is dropped while that view is under
// 10 seconds old; otherwise the last one received wins.
type AssistantViewContextResult struct {
	ViewContext *AssistantViewContext `json:"view_context"`
	// Source is "prompt" when the member's last prompt carried the view,
	// "navigation" when a later page change replaced it.
	Source string `json:"source,omitempty"`
	// AgeSeconds counts from when the server received it.
	AgeSeconds int64 `json:"age_seconds,omitempty"`
	// Connected is true while a dashboard streams the member's assistant
	// conversation, the only time it sends view updates.
	Connected bool `json:"connected"`
}

type AssistantAnswerParams struct {
	RequestID string         `json:"request_id"`
	OptionID  string         `json:"option_id"`
	Values    map[string]any `json:"values,omitempty"`
}

type AssistantConfirmParams struct {
	ConfirmationID string `json:"confirmation_id"`
	Approve        bool   `json:"approve"`
}

type AssistantHistoryParams struct {
	BeforeSeq int64 `json:"before_seq,omitempty"`
	Limit     int   `json:"limit,omitempty"`
}

type AssistantItemParams struct {
	Seq int64 `json:"seq"`
}

type AssistantDocsParams struct {
	Topic string `json:"topic,omitempty"`
}

type AssistantDocsResult struct {
	Topics []string `json:"topics,omitempty"`
	Text   string   `json:"text,omitempty"`
}

// AssistantStreamRequest is the /ws/assistant header.
type AssistantStreamRequest struct {
	AfterSeq int64 `json:"after_seq"`
	// Epoch is the epoch of the last ack the viewer holds. A different
	// conversation than the viewer's replays from the start.
	Epoch int64 `json:"epoch,omitempty"`
}

// AssistantFrame is one stream frame after the ACPStreamResponse ack: an
// ACPFrame, or the whole status, sent right after the ack and whenever the
// state or the pending confirmations change.
type AssistantFrame struct {
	ACPFrame
	Status *AssistantStatus `json:"status,omitempty"`
}

// SubsystemAssistant streams the caller's own assistant session.
const SubsystemAssistant = "aether-assistant"

// Outcomes of an assistant action. Approved and auto-approved are written
// before the call runs; succeeded or failed follows under the same action
// id. A declined, expired or withdrawn card, or a failure to read or count
// the approval, is the action's only record.
const (
	AssistantActionApproved     = "approved"
	AssistantActionAutoApproved = "auto_approved"
	AssistantActionDeclined     = "declined"
	AssistantActionExpired      = "expired"
	AssistantActionWithdrawn    = "withdrawn"
	AssistantActionFailed       = "failed"
	AssistantActionSucceeded    = "succeeded"
)

// AssistantAction is one record of an action the assistant took or asked to
// take, carried by a notice item of the member's transcript. Records with
// the same ID describe one action; the latest replaces the earlier.
type AssistantAction struct {
	ID     string `json:"id"`
	Method string `json:"method"`
	// Summary is the line the member approved, or would have.
	Summary string                `json:"summary"`
	Target  AssistantActionTarget `json:"target"`
	Outcome string                `json:"outcome"`
	// Approval is how the call was allowed to run: "card" when the member
	// approved it, "auto" by auto-approval, empty for a reversible call
	// that needs none.
	Approval string `json:"approval,omitempty"`
	// ConfirmationID is the card's id, when the action showed one.
	ConfirmationID string `json:"confirmation_id,omitempty"`
	// IdempotencyKey is the call's, when its method takes one, so a client
	// can tell apart two calls with the same method and target.
	IdempotencyKey string `json:"idempotency_key,omitempty"`
	OtherOwner     string `json:"other_owner,omitempty"`
	// Error is the failure's text: the server's error, or why the approval
	// could not be read or counted.
	Error string `json:"error,omitempty"`
	// Result names what the action created, such as a launched run.
	Result     *AssistantActionTarget `json:"result,omitempty"`
	CreatedAt  time.Time              `json:"created_at"`
	DecidedAt  time.Time              `json:"decided_at,omitzero"`
	FinishedAt time.Time              `json:"finished_at,omitzero"`
}

type AssistantActionTarget struct {
	WorkspaceID string       `json:"workspace_id,omitempty"`
	RunID       string       `json:"run_id,omitempty"`
	WatchID     string       `json:"watch_id,omitempty"`
	Watch       *WatchTarget `json:"watch,omitempty"`
}
