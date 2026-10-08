package domain

import (
	"errors"
	"strings"
)

// ErrPromptNotSent marks a delivery failure before the prompt reached the
// agent transport. It must not wrap failures after delivery was attempted.
var ErrPromptNotSent = errors.New("agent prompt was not sent")

// MaxImageBytes bounds decoded image uploads and previews on every transport.
const MaxImageBytes = 8 << 20

// AgentPrompt preserves validated attachment references and the persisted room
// message identity until the delivery driver selects its wire representation.
type AgentPrompt struct {
	Text        string
	Attachments []string
	MessageID   string
}

// TextWithAttachments renders references for terminal delivery. ACP delivery
// uses native image blocks instead of this text-only representation.
func (p AgentPrompt) TextWithAttachments() string {
	if len(p.Attachments) == 0 {
		return p.Text
	}
	const prefix = "\n\n--- AETHER ATTACHMENTS ---\n"
	const suffix = "--- END AETHER ATTACHMENTS ---"
	size := len(p.Text) + len(prefix) + len(suffix)
	for _, ref := range p.Attachments {
		size += len(ref) + 3
	}
	var b strings.Builder
	b.Grow(size)
	b.WriteString(p.Text)
	b.WriteString(prefix)
	for _, ref := range p.Attachments {
		b.WriteString("- ")
		b.WriteString(ref)
		b.WriteByte('\n')
	}
	b.WriteString(suffix)
	return b.String()
}
