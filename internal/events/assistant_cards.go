package events

import "github.com/3xDevOps/Aether/internal/domain"

// TypeAssistantCards says a member's assistant approval cards changed: one
// was raised, decided, expired or withdrawn. It carries no card; a reader
// asks for the member's open cards.
const TypeAssistantCards Type = "assistant.cards"

type AssistantCardsPayload struct {
	MemberID domain.MemberID `json:"member_id"`
}

func (AssistantCardsPayload) EventType() Type { return TypeAssistantCards }

// Recipient is the one member the change may be streamed to.
func (p AssistantCardsPayload) Recipient() domain.MemberID { return p.MemberID }

func init() { registerPayload[AssistantCardsPayload](TypeAssistantCards) }
