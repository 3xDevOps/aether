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
	Text           string `json:"text"`
	IdempotencyKey string `json:"idempotency_key"`
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
