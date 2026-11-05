package protocol

// MethodAssistantAsk is a connector's door to the member's assistant: it
// sends a prompt, reads the turn it started, or decides a card that turn
// raised. Only a connector may call it.
const MethodAssistantAsk = "assistant.ask"

// AssistantAskParams holds exactly one of Prompt, TurnID or CardID. A
// decision on CardID needs Decision and Card, the card's text echoed
// exactly.
type AssistantAskParams struct {
	Prompt         string `json:"prompt,omitempty"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
	TurnID         string `json:"turn_id,omitempty"`
	CardID         string `json:"card_id,omitempty"`
	Decision       string `json:"decision,omitempty"`
	Card           string `json:"card,omitempty"`
}

// Decisions on a card from a connector.
const (
	AssistantAskApprove = "approve"
	AssistantAskDecline = "decline"
)

// States of a connector turn.
const (
	AssistantAskDone        = "done"
	AssistantAskQueued      = "queued"
	AssistantAskRunning     = "running"
	AssistantAskWaitingCard = "waiting_for_approval"
	AssistantAskFailed      = "failed"
)

// AssistantAskWaitingInAether is the card text of a card a connector may
// not decide.
const AssistantAskWaitingInAether = "Waiting for your approval in Aether"

type AssistantAskCard struct {
	CardID string `json:"card_id"`
	Card   string `json:"card"`
}

// AssistantAskResult is a turn's state, or, for a decision, the card
// decided and the decision.
type AssistantAskResult struct {
	TurnID       string             `json:"turn_id,omitempty"`
	State        string             `json:"state,omitempty"`
	Reply        string             `json:"reply,omitempty"`
	Error        string             `json:"error,omitempty"`
	PendingCards []AssistantAskCard `json:"pending_cards,omitempty"`
	CardID       string             `json:"card_id,omitempty"`
	Decision     string             `json:"decision,omitempty"`
}
