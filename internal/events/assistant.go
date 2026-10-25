package events

import "github.com/3xDevOps/Aether/internal/domain"

const TypeAssistantNotice Type = "assistant.notice"

const (
	AssistantNoticeFinished       = "finished"
	AssistantNoticeNeedsInput     = "needs_input"
	AssistantNoticeNeedsAttention = "needs_attention"
)

type AssistantNoticePayload struct {
	MemberID domain.MemberID `json:"member_id"`
	RunID    domain.RunID    `json:"run_id"`
	Kind     string          `json:"kind"`
	Title    string          `json:"title"`
	Body     string          `json:"body"`
}

func (AssistantNoticePayload) EventType() Type { return TypeAssistantNotice }

func init() { registerPayload[AssistantNoticePayload](TypeAssistantNotice) }
