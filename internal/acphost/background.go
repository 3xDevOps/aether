package acphost

import (
	"encoding/json"
	"maps"
	"slices"
	"time"
)

// autonomousQuiet ends an autonomous turn whose agent reports no end for it,
// once nothing arrived for this long and no tool call or request is open. It
// matches the two minutes the scheduler waits for a harness that cannot report
// a turn end.
var autonomousQuiet = 2 * time.Minute

type updateHead struct {
	Kind string `json:"sessionUpdate"`
	Meta struct {
		ClaudeCode struct {
			ToolResponse json.RawMessage `json:"toolResponse"`
		} `json:"claudeCode"`
		Origin *struct {
			Kind string `json:"kind"`
		} `json:"_claude/origin"`
	} `json:"_meta"`
}

// opensTurn reports whether an update is the agent working. A tool call
// update alone can trail a turn that already ended.
func opensTurn(kind string) bool {
	switch kind {
	case "agent_message_chunk", "agent_thought_chunk", "tool_call", "plan":
		return true
	}
	return false
}

// launchedTask is the id of the background task a Claude Code tool result
// reports starting: a backgrounded command's backgroundTaskId, or the taskId
// of a workflow launched in the background. Claude's adapter holds the prompt
// open for a background subagent itself.
func launchedTask(toolResponse json.RawMessage) string {
	var r struct {
		BackgroundTaskID string `json:"backgroundTaskId"`
		Status           string `json:"status"`
		TaskID           string `json:"taskId"`
	}
	if json.Unmarshal(toolResponse, &r) != nil {
		return ""
	}
	if r.BackgroundTaskID != "" {
		return r.BackgroundTaskID
	}
	if r.Status == "async_launched" {
		return r.TaskID
	}
	return ""
}

// backgroundLocked reports whether the agent works without an open prompt: an
// autonomous turn runs, or background tasks await their follow-up.
func (s *Session) backgroundLocked() bool { return s.autonomous || len(s.launched) > 0 }

func (s *Session) backgroundTasksLocked() []string {
	return slices.Sorted(maps.Keys(s.launched))
}

// trackLocked notes what an update says about work outside a prompt.
func (s *Session) trackLocked(head updateHead) {
	s.lastUpdate = time.Now()
	if id := launchedTask(head.Meta.ClaudeCode.ToolResponse); id != "" {
		if _, ok := s.launched[id]; !ok {
			s.launched[id] = s.turn
		}
	}
	// Claude's adapter tags the usage_update it sends for every result of an
	// autonomous cycle with the cycle's origin.
	if head.Kind != "usage_update" || head.Meta.Origin == nil {
		return
	}
	if s.autonomous {
		s.resulted, s.origin = true, head.Meta.Origin.Kind
	}
	if head.Meta.Origin.Kind != "task-notification" {
		return
	}
	// The follow-up does not say which task finished, so it settles every
	// task started before it; one started by this follow-up stays.
	for id, turn := range s.launched {
		if !s.autonomous || turn < s.turn {
			delete(s.launched, id)
		}
	}
}

// openAutonomousLocked starts an autonomous turn for agent work that arrived
// with no prompt open.
func (s *Session) openAutonomousLocked() {
	if s.turnActive || s.autonomous || s.closed {
		return
	}
	wasBackground := s.backgroundLocked()
	s.proj.closeText()
	s.turn++
	s.autonomous, s.resulted, s.origin = true, false, ""
	s.emitLocked(Item{Kind: KindTurnStart, Autonomous: true})
	if !wasBackground {
		s.waiting = true
		s.stateLocked(true, "background", nil)
	}
}

// settleLocked ends the autonomous turn once the agent reported its result,
// or went quiet for autonomousQuiet, with no tool call or request open, and
// reports idle once no background work is left.
func (s *Session) settleLocked() {
	if s.turnActive || s.closed {
		return
	}
	if s.autonomous {
		if s.proj.toolsOpen() || len(s.conn.pendingRequests()) > 0 {
			return
		}
		if !s.resulted {
			if wait := autonomousQuiet - time.Since(s.lastUpdate); wait > 0 {
				if !s.quietArmed {
					s.quietArmed = true
					time.AfterFunc(wait, s.quietCheck)
				}
				return
			}
		}
		s.endAutonomousLocked("end_turn")
		if s.turnActive {
			return
		}
	}
	if len(s.launched) == 0 && s.waiting {
		s.waiting = false
		s.stateLocked(false, s.idleStop, nil)
	}
}

func (s *Session) quietCheck() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.quietArmed = false
	s.settleLocked()
}

// endAutonomousLocked records the end of the autonomous turn and starts the
// first queued prompt.
func (s *Session) endAutonomousLocked(stop string) {
	s.proj.endTurn()
	s.emitLocked(Item{Kind: KindTurnEnd, StopReason: stop, Autonomous: true, Origin: s.origin, BackgroundTasks: s.backgroundTasksLocked()})
	s.autonomous, s.resulted, s.origin = false, false, ""
	s.idleStop = stop
	if len(s.queue) > 0 && !s.closingLocked() {
		next := s.queue[0]
		s.queue = s.queue[1:]
		s.startTurnLocked(next.blocks, next.delivered)
	}
}

func (s *Session) stateLocked(working bool, reason string, failed error) {
	if s.cfg.OnState != nil {
		s.callback(func() { s.cfg.OnState(working, reason, failed) })
	}
}

func (s *Session) cancelBackgroundLocked() {
	if s.turnActive || s.closed || !s.backgroundLocked() {
		return
	}
	clear(s.launched)
	if s.autonomous {
		s.endAutonomousLocked("cancelled")
		if s.turnActive {
			return
		}
	}
	s.idleStop = "cancelled"
	s.settleLocked()
}
