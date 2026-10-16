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
	// MethodAssistantView replaces the caller's latest view context, or
	// clears it when none is sent.
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
}

type AssistantStatus struct {
	State         string                  `json:"state"`
	Agent         string                  `json:"agent,omitempty"`
	Agents        []AssistantAgent        `json:"agents"`
	Error         string                  `json:"error,omitempty"`
	Confirmations []AssistantConfirmation `json:"confirmations"`
	AutoApprove   bool                    `json:"auto_approve"`
}

// AssistantSettingsParams changes each field that is set; none reads.
type AssistantSettingsParams struct {
	AutoApprove *bool `json:"auto_approve,omitempty"`
}

type AssistantSettings struct {
	// AutoApprove runs confirmed tools without a card, as if approved.
	AutoApprove bool `json:"auto_approve"`
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
// are hints for the assistant's member-scoped tools, never permissions.
type AssistantViewContext struct {
	Version    int                   `json:"version"`
	CapturedAt time.Time             `json:"captured_at"`
	Route      AssistantViewEntry    `json:"route"`
	OpenFile   *AssistantViewFile    `json:"open_file,omitempty"`
	Layout     map[string]bool       `json:"layout,omitempty"`
	Recent     []AssistantViewEntry  `json:"recent,omitempty"`
	Pending    *AssistantViewPending `json:"pending,omitempty"`
}

type AssistantViewEntry struct {
	Name        string    `json:"name"`
	WorkspaceID string    `json:"workspace_id,omitempty"`
	RunID       string    `json:"run_id,omitempty"`
	MissionID   string    `json:"mission_id,omitempty"`
	Subagent    string    `json:"subagent,omitempty"`
	View        string    `json:"view,omitempty"`
	Path        string    `json:"path,omitempty"`
	At          time.Time `json:"at,omitzero"`
}

type AssistantViewFile struct {
	Kind        string `json:"kind"`
	Path        string `json:"path"`
	RunID       string `json:"run_id,omitempty"`
	WorkspaceID string `json:"workspace_id,omitempty"`
}

type AssistantViewPending struct {
	Approvals        int                     `json:"approvals"`
	AssistantNotices int                     `json:"assistant_notices"`
	NeedsYou         []AssistantViewNeedsYou `json:"needs_you,omitempty"`
}

type AssistantViewNeedsYou struct {
	RunID  string `json:"run_id"`
	Reason string `json:"reason,omitempty"`
}

// AssistantViewContextResult is the member's latest view context. Several
// dashboards share one: the latest navigation wins.
type AssistantViewContextResult struct {
	ViewContext *AssistantViewContext `json:"view_context"`
	AgeSeconds  int64                 `json:"age_seconds,omitempty"`
	// Connected is true while a dashboard streams the member's assistant.
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
