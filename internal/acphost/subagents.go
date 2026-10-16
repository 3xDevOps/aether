package acphost

import (
	"bytes"
	"cmp"
	"encoding/json"
	"strings"
)

// subagentChild is what one subagent_spawned, subagent_state_update or
// draft subagent_update says about a child session. A nil label and an
// empty Status leave the recorded value unchanged.
type subagentChild struct {
	ID string
	// Launch is the tool call id the child's items name as their parent.
	Launch string
	// ToolCall says Launch is the agent's own tool call (Cursor's task
	// call), not an id Aether made up.
	ToolCall bool
	// Announces is set for an update that can introduce the child.
	Announces bool

	Title, Type, Description, Prompt *string
	Status                           string
}

// parseSubagent reads a subagent update. It reports false for any other
// update and for one whose launch id would not fit a link.
func parseSubagent(raw json.RawMessage) (subagentChild, bool) {
	var c subagentChild
	if !bytes.Contains(raw, []byte(`"subagent_`)) {
		return c, false
	}
	var u struct {
		Kind              string          `json:"sessionUpdate"`
		SessionID         string          `json:"sessionId"`
		SubagentSessionID string          `json:"subagentSessionId"`
		Name              *string         `json:"name"`
		Task              *string         `json:"task"`
		Prompt            *string         `json:"prompt"`
		Title             json.RawMessage `json:"title"`
		Description       json.RawMessage `json:"description"`
		State             json.RawMessage `json:"state"`
		Meta              struct {
			Cursor struct {
				ToolCallID string `json:"toolCallId"`
			} `json:"cursor"`
		} `json:"_meta"`
	}
	if json.Unmarshal(raw, &u) != nil {
		return c, false
	}
	switch u.Kind {
	case "subagent_spawned":
		c.ID, c.Announces, c.Status = u.SubagentSessionID, true, "in_progress"
		c.Prompt = cmp.Or(u.Prompt, u.Task)
		// Cursor sends the subagent type as the name.
		if u.Meta.Cursor.ToolCallID != "" {
			c.Type = u.Name
		} else {
			c.Title = u.Name
		}
	case "subagent_state_update":
		c.ID = u.SubagentSessionID
		var state string
		if json.Unmarshal(u.State, &state) == nil {
			c.Status = subagentStatus(state, "")
		}
	case "subagent_update":
		c.ID, c.Announces = u.SessionID, true
		c.Title, c.Description = patchedString(u.Title), patchedString(u.Description)
		var state struct {
			State      string `json:"state"`
			StopReason any    `json:"stopReason"`
		}
		if json.Unmarshal(u.State, &state) == nil {
			stop, _ := state.StopReason.(string)
			c.Status = subagentStatus(state.State, stop)
		}
	default:
		return c, false
	}
	c.Launch = "subagent:" + c.ID
	if id := u.Meta.Cursor.ToolCallID; id != "" {
		c.Launch, c.ToolCall = id, true
	}
	return c, c.ID != "" && len(c.Launch) <= maxLinkBytes
}

// replayedSubagent restores the labels of a subagent update session/load
// replays. The item log already holds its launch call; a later state change
// writes the call again.
func (s *Session) replayedSubagent(c subagentChild) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.proj.label(c)
}

// patchedString reads a draft patch field: nil when omitted, empty when
// null, which clears it.
func patchedString(raw json.RawMessage) *string {
	var s *string
	if raw == nil || json.Unmarshal(raw, &s) != nil {
		return nil
	}
	if s == nil {
		s = new(string)
	}
	return s
}

// subagentStatus maps a child's state to its launch call's status. A
// cancelled or disconnected child leaves it in progress: a call still open
// when its turn ended reads as stopped.
func subagentStatus(state, stop string) string {
	switch state {
	case "running", "requires_action", "unknown":
		return "in_progress"
	case "completed":
		return "completed"
	case "failed":
		return "failed"
	case "idle":
		switch {
		case stop == "cancelled":
			return ""
		case stop == "error" || stop == "refusal" || strings.HasPrefix(stop, "max_"):
			return "failed"
		}
		return "completed"
	}
	return ""
}
