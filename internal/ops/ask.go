package ops

import (
	"context"
	"fmt"

	"github.com/3xDevOps/Aether/internal/protocol"
)

type askInput struct {
	Prompt         string `json:"prompt,omitempty"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
	TurnID         string `json:"turn_id,omitempty" jsonschema:"poll this turn"`
}

type decideCardInput struct {
	CardID   string `json:"card_id"`
	Decision string `json:"decision"`
	Card     string `json:"card" jsonschema:"the card's text, exactly"`
}

// Deciding a card is its own tool, so a host's "always allow" for asking
// never also allows approvals.
var askOps = []Op{
	opOf("aether_ask_assistant", "Ask the Aether assistant", ScopeWrite, false, "",
		"Use this when the user asks their Aether assistant; each call costs it a turn, so read with other tools. "+
			"Pass prompt, or turn_id to poll."+untrustedRule, ask).
		hints(true, true).calling(protocol.MethodAssistantAsk),
	opOf("aether_decide_assistant_card", "Decide assistant card", ScopeWrite, false, "",
		"Use this when the user, shown a pending card's text from aether_ask_assistant, tells you to approve or decline it; never decide on your own.", decideCard).
		enum("decision", protocol.AssistantAskApprove, protocol.AssistantAskDecline).hints(true, true).
		calling(protocol.MethodAssistantAsk),
}

// ask polls with read, which a connector's write budget does not count;
// the server still requires write for every call.
func ask(ctx context.Context, c calls, in askInput) (string, any, error) {
	params := protocol.AssistantAskParams{Prompt: in.Prompt, IdempotencyKey: in.IdempotencyKey, TurnID: in.TurnID}
	call := c.act
	if in.TurnID != "" {
		call = c.read
	}
	var res protocol.AssistantAskResult
	if err := call(ctx, protocol.MethodAssistantAsk, params, &res); err != nil {
		return "", nil, err
	}
	untrusted := map[string]any{"reply": res.Reply}
	if len(res.PendingCards) > 0 {
		untrusted["pending_cards"] = res.PendingCards
	}
	if res.Error != "" {
		untrusted["error"] = res.Error
	}
	out := map[string]any{"turn_id": res.TurnID, "state": res.State, "untrusted": untrusted}
	summary := fmt.Sprintf("Turn %s is %s.", res.TurnID, res.State)
	switch res.State {
	case protocol.AssistantAskDone, protocol.AssistantAskFailed:
	default:
		out["write"] = writePending
		summary += " Poll with turn_id."
	}
	return summary, out, nil
}

func decideCard(ctx context.Context, c calls, in decideCardInput) (string, any, error) {
	var res protocol.AssistantAskResult
	if err := c.act(ctx, protocol.MethodAssistantAsk, protocol.AssistantAskParams{CardID: in.CardID, Decision: in.Decision, Card: in.Card}, &res); err != nil {
		return "", nil, err
	}
	return fmt.Sprintf("Card %s: %s.", res.CardID, res.Decision), map[string]any{"card_id": res.CardID, "decision": res.Decision}, nil
}
