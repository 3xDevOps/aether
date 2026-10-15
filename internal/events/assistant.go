package events

import "github.com/3xDevOps/Aether/internal/domain"

// TypeAssistantNotice reports that Aether told MemberID, in their assistant
// transcript, about a run their assistant launched or messaged. The envelope
// carries the run's workspace, the run, and the member as actor.
const TypeAssistantNotice Type = "assistant.notice"

// Kinds of AssistantNoticePayload.
const (
	AssistantNoticeFinished       = "finished"
	AssistantNoticeNeedsInput     = "needs_input"
	AssistantNoticeNeedsApproval  = "needs_approval"
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
