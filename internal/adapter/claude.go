package adapter

import (
	"encoding/json"

	"github.com/3xDevOps/Aether/internal/events"
)

// maxDetail bounds AgentEventPayload.Detail: it is a human-readable
// summary for the timeline, not a transcript (the PTY cast has the full
// output).
const maxDetail = 256

// claude translates Claude Code's stream-json output (claude -p
// --output-format stream-json) into typed payloads. The stream is
// newline-delimited JSON: an initial {"type":"system","subtype":"init"}
// message, assistant messages whose content blocks include tool_use, user
// messages carrying tool_result blocks, and a final {"type":"result"}
// message with usage and cost totals.
type claude struct{}

func newClaude() Adapter { return claude{} }

// claudeMsg is the envelope of one stream-json line.
type claudeMsg struct {
	Type         string        `json:"type"`
	Subtype      string        `json:"subtype"`
	SessionID    string        `json:"session_id"`
	Message      claudeMessage `json:"message"`
	TotalCostUSD float64       `json:"total_cost_usd"`
	Usage        claudeUsage   `json:"usage"`
}

// claudeMessage is the API message inside assistant and user envelopes.
// Content stays raw: it is a block array on tool-bearing messages but a
// plain string on some user messages, and either shape may be absent.
type claudeMessage struct {
	Content json.RawMessage `json:"content"`
}

// claudeUsage is the token accounting of a result message.
type claudeUsage struct {
	InputTokens              int64 `json:"input_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
}

// claudeBlock is one content block of an assistant or user message; only
// tool_use and tool_result blocks matter here.
type claudeBlock struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	IsError   bool            `json:"is_error"`
}

func (claude) ConsumeLine(line string) []events.Payload {
	if len(line) == 0 || line[0] != '{' {
		return nil // ordinary PTY output, not a stream-json record
	}
	var msg claudeMsg
	if err := json.Unmarshal([]byte(line), &msg); err != nil {
		return nil // unparseable lines are opaque output, never an error
	}
	switch msg.Type {
	case "system":
		if msg.Subtype == "init" && msg.SessionID != "" {
			// Resume metadata: relaunching with
			// claude --resume <session_id> continues this session.
			return []events.Payload{events.AgentEventPayload{
				Kind:             events.AgentSession,
				HarnessSessionID: msg.SessionID,
			}}
		}
	case "assistant":
		var out []events.Payload
		for _, b := range decodeBlocks(msg.Message.Content) {
			if b.Type != "tool_use" {
				continue
			}
			p := events.AgentEventPayload{
				Kind:      events.AgentToolCall,
				Tool:      b.Name,
				ToolUseID: b.ID,
				Detail:    detailFromInput(b.Input),
			}
			switch b.Name {
			case "Task":
				p.Kind = events.AgentSubagent
			case "ExitPlanMode":
				// The agent finished planning and is pausing for
				// plan review/approval.
				p.Kind = events.AgentPause
			}
			out = append(out, p)
		}
		return out
	case "user":
		var out []events.Payload
		for _, b := range decodeBlocks(msg.Message.Content) {
			if b.Type != "tool_result" {
				continue
			}
			out = append(out, events.AgentEventPayload{
				Kind:      events.AgentToolResult,
				ToolUseID: b.ToolUseID,
				IsError:   b.IsError,
			})
		}
		return out
	case "result":
		// Per-turn assistant usage is ignored; only the final result
		// message is emitted, so the run's cost is counted exactly once.
		// InputTokens folds cache creation and cache read tokens in: the
		// total prompt-side tokens the run consumed.
		return []events.Payload{events.RunCostPayload{
			InputTokens: msg.Usage.InputTokens +
				msg.Usage.CacheCreationInputTokens +
				msg.Usage.CacheReadInputTokens,
			OutputTokens: msg.Usage.OutputTokens,
			CostUSD:      msg.TotalCostUSD,
			Metered:      true,
		}}
	}
	return nil
}

// decodeBlocks decodes a message's content blocks; nil when content is
// absent or not the block-array shape (e.g. a plain string).
func decodeBlocks(raw json.RawMessage) []claudeBlock {
	if len(raw) == 0 {
		return nil
	}
	var blocks []claudeBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil
	}
	return blocks
}

// detailKeys are tried in order to summarize a tool_use input; the first
// string-valued key wins.
var detailKeys = [...]string{"command", "file_path", "path", "description", "prompt", "plan", "pattern", "url"}

// detailFromInput renders a short human-readable summary of a tool_use
// input: a well-known field when present, else the compact input JSON.
func detailFromInput(input json.RawMessage) string {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(input, &fields); err == nil {
		for _, k := range detailKeys {
			raw, ok := fields[k]
			if !ok {
				continue
			}
			var s string
			if err := json.Unmarshal(raw, &s); err == nil && s != "" {
				return truncate(s)
			}
		}
	}
	if len(input) == 0 {
		return ""
	}
	return truncate(string(input))
}

func truncate(s string) string {
	if len(s) <= maxDetail {
		return s
	}
	// Cut on a rune boundary so the result stays valid UTF-8.
	cut := maxDetail
	for cut > 0 && s[cut]&0xc0 == 0x80 {
		cut--
	}
	return s[:cut] + "..."
}
