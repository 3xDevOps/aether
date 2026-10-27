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
	// WatchID is the watch that recorded the notice.
	WatchID string `json:"watch_id,omitempty"`
	Kind    string `json:"kind"`
	Title   string `json:"title"`
	Body    string `json:"body"`
}

func (AssistantNoticePayload) EventType() Type { return TypeAssistantNotice }

// Recipient is the one member a notice may be streamed to.
func (p AssistantNoticePayload) Recipient() domain.MemberID { return p.MemberID }

func init() { registerPayload[AssistantNoticePayload](TypeAssistantNotice) }
