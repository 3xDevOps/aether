package acphost

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/3xDevOps/Aether/internal/agentstatus"
)

// autonomousQuiet ends an autonomous turn whose agent reports no result for
// it, once nothing arrived for this long and no tool call or request is open.
// It matches the two minutes the scheduler waits for a harness that cannot
// report a turn end.
var autonomousQuiet = 2 * time.Minute

// autonomousStale ends an autonomous turn that sent nothing for this long
// while a tool call stayed open: Claude Code's Bash tool runs at most ten
// minutes, so such a call lost its end update.
var autonomousStale = 15 * time.Minute

// followUpGrace keeps a run working after a turn during which a task it
// waited for ended: Claude Code starts the turn that reports back on the task
// about 100 ms after the previous turn's result.
var followUpGrace = 2 * time.Second

// endedTaskMemory bounds the task ends kept for launching tool calls that
// have not arrived yet.
const endedTaskMemory = 32

// claudeTaskFrames are the raw Claude Code SDK messages Claude's adapter
// forwards as _claude/sdkMessage when session meta lists them in
// claudeCode.emitRawSDKMessages. task_progress changes no state; it arrives
// on every tool use of a task, which keeps a long task's run active.
var claudeTaskFrames = []map[string]string{
	{"type": "system", "subtype": "task_notification"},
	{"type": "system", "subtype": "task_updated"},
	{"type": "system", "subtype": "background_tasks_changed"},
	{"type": "system", "subtype": "task_progress"},
}

// Task is one live Claude Code background task.
type Task struct {
	ID string `json:"id"`
	// Type is Claude Code's task type, such as local_bash, local_agent or
	// local_workflow.
	Type        string `json:"type,omitempty"`
	Description string `json:"description,omitempty"`
	// Holds says the run stays working until the task ends.
	Holds bool `json:"holds,omitempty"`
}

type liveTask struct {
	typ, description string
	ambient          bool
	// dismissed marks a task the user cancelled the wait for.
	dismissed bool
}

// holds reports whether the run waits for the task (agentstatus.HeldTask).
// Claude Code is idle while only a shell runs.
func (t *liveTask) holds() bool {
	held, _ := agentstatus.HeldTask(t.typ)
	return !t.ambient && !t.dismissed && held
}

type sdkMessage struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`
	TaskID  string `json:"task_id"`
	Status  string `json:"status"`
	Patch   struct {
		Status string `json:"status"`
	} `json:"patch"`
	Tasks []struct {
		TaskID      string `json:"task_id"`
		TaskType    string `json:"task_type"`
		Description string `json:"description"`
		Ambient     bool   `json:"ambient"`
	} `json:"tasks"`
}

type updateHead struct {
	Kind   string `json:"sessionUpdate"`
	Status string `json:"status"`
	Meta   struct {
		Origin *struct {
			Kind string `json:"kind"`
		} `json:"_claude/origin"`
		ClaudeCode struct {
			ParentToolUseID string `json:"parentToolUseId"`
		} `json:"claudeCode"`
	} `json:"_meta"`
}

// opensTurn reports whether an update is the main agent working: a
// subagent's output carries its parent tool call, and a tool call update
// alone can trail a turn that already ended.
func (h updateHead) opensTurn() bool {
	if h.Meta.ClaudeCode.ParentToolUseID != "" {
		return false
	}
	switch h.Kind {
	case "agent_message_chunk", "agent_thought_chunk", "tool_call", "plan":
		return true
	}
	return false
}

// progress reports whether an update is the agent doing work, as opposed to
// echoing the prompt or announcing modes, commands, options or usage.
func (h updateHead) progress() bool {
	switch h.Kind {
	case "agent_message_chunk", "agent_thought_chunk", "tool_call", "tool_call_update", "plan",
		"subagent_spawned", "subagent_state_update", "subagent_update", "compaction_update", "compaction_summary_chunk":
		return true
	}
	return false
}

func (s *Session) sdkMessage(raw json.RawMessage) {
	var m sdkMessage
	if json.Unmarshal(raw, &m) != nil || m.Type != "system" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	held := s.heldLocked()
	switch m.Subtype {
	case "task_notification":
		s.taskEndedLocked(m.TaskID, m.Status)
		delete(s.tasks, m.TaskID)
	case "task_updated":
		switch m.Patch.Status {
		case "completed", "failed":
			s.taskEndedLocked(m.TaskID, m.Patch.Status)
			delete(s.tasks, m.TaskID)
		case "killed":
			s.taskEndedLocked(m.TaskID, "stopped")
			delete(s.tasks, m.TaskID)
		}
	case "background_tasks_changed":
		// The full live set, which replaces the one held.
		live := make(map[string]bool, len(m.Tasks))
		for _, lt := range m.Tasks {
			live[lt.TaskID] = true
			t := s.tasks[lt.TaskID]
			if t == nil {
				description, _ := cutTail(lt.Description, maxShrunkString)
				t = &liveTask{typ: lt.TaskType, description: description}
				s.tasks[lt.TaskID] = t
			}
			t.ambient = lt.Ambient
		}
		maps.DeleteFunc(s.tasks, func(id string, _ *liveTask) bool { return !live[id] })
	default:
		return
	}
	if s.heldLocked() < held {
		// Claude Code reports back on the task in a turn of its own, which
		// starts right after the running turn, if any, ends.
		if s.turnActive || s.autonomous {
			s.endedInTurn = true
		} else {
			s.oweLocked(autonomousQuiet)
		}
	}
	s.logTasksLocked()
	s.settleLocked()
}

// trackTaskCallLocked fills in the type of the task a tool call started and
// keeps the call's snapshot while the task runs.
func (s *Session) trackTaskCallLocked(it *Item) {
	task := it.ToolCall.Task
	if task == nil || task.Status != "running" {
		return
	}
	if status, ok := s.endedTasks[task.ID]; ok {
		ended := *task
		ended.Status = status
		it.ToolCall.Task = &ended
		s.proj.setTask(it.ToolCall.ID, &ended)
		return
	}
	if t := s.tasks[task.ID]; t != nil && task.Type == "" {
		typed := *task
		typed.Type = t.typ
		it.ToolCall.Task = &typed
		s.proj.setTask(it.ToolCall.ID, &typed)
	}
	s.taskCalls[task.ID] = *it.ToolCall
}

// taskEndedLocked writes the snapshot of the tool call that started task id
// with the task's final status.
func (s *Session) taskEndedLocked(id, status string) {
	call, ok := s.taskCalls[id]
	if !ok {
		// The task may end before the tool result that launched it.
		if _, seen := s.endedTasks[id]; !seen {
			if len(s.endedOrder) == endedTaskMemory {
				delete(s.endedTasks, s.endedOrder[0])
				s.endedOrder = s.endedOrder[1:]
			}
			s.endedTasks[id] = status
			s.endedOrder = append(s.endedOrder, id)
		}
		return
	}
	delete(s.taskCalls, id)
	task := *call.Task
	task.Status = status
	if t := s.tasks[id]; t != nil && task.Type == "" {
		task.Type = t.typ
	}
	// Output is a delta the launch snapshot already carried.
	call.Task, call.Output = &task, ""
	s.proj.setTask(call.ID, &task)
	s.emitLocked(Item{Kind: KindToolCall, ToolCall: &call})
}

func (s *Session) tasksLocked() []Task {
	tasks := make([]Task, 0, len(s.tasks))
	for _, id := range slices.Sorted(maps.Keys(s.tasks)) {
		t := s.tasks[id]
		if !t.ambient {
			tasks = append(tasks, Task{ID: id, Type: t.typ, Description: t.description, Holds: t.holds()})
		}
	}
	return tasks
}

func (s *Session) heldLocked() int { return len(s.heldTasksLocked()) }

// heldAgentsLocked counts the held tasks that are the agent's own subagents
// or workflows; the rest are tools it waits on, such as a monitor.
func (s *Session) heldAgentsLocked() int {
	n := 0
	for _, id := range s.heldTasksLocked() {
		if _, agent := agentstatus.HeldTask(s.tasks[id].typ); agent {
			n++
		}
	}
	return n
}

func (s *Session) heldTasksLocked() []string {
	var ids []string
	for _, id := range slices.Sorted(maps.Keys(s.tasks)) {
		if s.tasks[id].holds() {
			ids = append(ids, id)
		}
	}
	return ids
}

// logTasksLocked records the live task set and the owed follow-up when they
// differ from what the log holds last.
func (s *Session) logTasksLocked() {
	tasks, owing := s.tasksLocked(), s.owingLocked()
	if slices.Equal(tasks, s.loggedTasks) && owing == s.loggedOwed {
		return
	}
	s.loggedTasks, s.loggedOwed = tasks, owing
	s.emitLocked(Item{Kind: KindBackground, Tasks: tasks, FollowUp: owing})
}

// relogTasksLocked records the live task set again after a turn start, which
// fences the set logged before it out of Interrupted, and drops a follow-up
// the log still owes: the turn starting is that follow-up.
func (s *Session) relogTasksLocked() {
	tasks := s.tasksLocked()
	if len(tasks) > 0 || s.loggedOwed {
		s.emitLocked(Item{Kind: KindBackground, Tasks: tasks})
	}
	s.loggedTasks, s.loggedOwed = tasks, false
}

// oweLocked waits up to d for the turn that reports back on an ended task.
func (s *Session) oweLocked(d time.Duration) {
	s.owedUntil = time.Now().Add(d)
	time.AfterFunc(d, s.quietCheck)
	s.logTasksLocked()
}

func (s *Session) owingLocked() bool { return time.Now().Before(s.owedUntil) }

// followUpLocked owes the turn reporting back on a task that ended during
// the turn that just ended.
func (s *Session) followUpLocked() {
	if s.endedInTurn {
		s.endedInTurn = false
		s.oweLocked(followUpGrace)
	}
}

// interruptTasksLocked records that no task outlived the agent connection
// whose tasks the log lists last: interrupted when the run waited for one.
func (s *Session) interruptTasksLocked(last Item) {
	if len(last.Tasks) == 0 && !last.FollowUp {
		return
	}
	marker := Item{Kind: KindBackground}
	if last.FollowUp || slices.ContainsFunc(last.Tasks, func(t Task) bool { return t.Holds }) {
		marker.StopReason = "interrupted"
	}
	s.loggedTasks, s.loggedOwed = nil, false
	s.emitLocked(marker)
}

// backgroundLocked reports whether the agent works with no prompt open: an
// autonomous turn runs, tasks it reports back on are live, or one ended and
// the turn reporting back on it is still owed.
func (s *Session) backgroundLocked() bool {
	return s.autonomous || s.heldLocked() > 0 || s.owingLocked()
}

func (s *Session) trackLocked(head updateHead) {
	s.lastUpdate = time.Now()
	// Claude's adapter tags the usage_update it sends for every result of an
	// autonomous cycle with the cycle's origin.
	if s.autonomous && head.Kind == "usage_update" && head.Meta.Origin != nil {
		s.resulted, s.origin = true, head.Meta.Origin.Kind
	}
}

// openAutonomousLocked starts an autonomous turn for agent work that arrived
// with no prompt open, from an agent whose adapter runs turns of its own.
func (s *Session) openAutonomousLocked() {
	if !s.cfg.ClaudeCode || s.turnActive || s.autonomous || s.closed {
		return
	}
	s.proj.closeText()
	s.turn++
	s.autonomous, s.resulted, s.origin, s.holdIdle = true, false, "", false
	s.turnAt = time.Now()
	s.lastUpdate, s.owedUntil = time.Now(), time.Time{}
	s.emitLocked(Item{Kind: KindTurnStart, Autonomous: true})
	s.relogTasksLocked()
}

// settleLocked ends the autonomous turn once the agent reported its result,
// or went quiet, and reports the state the background work leaves.
func (s *Session) settleLocked() {
	if s.turnActive || s.closed {
		return
	}
	if s.autonomous && s.autonomousOverLocked() {
		s.endAutonomousLocked("end_turn")
		if s.turnActive {
			return
		}
	}
	if s.backgroundLocked() && (s.autonomous || !s.holdIdle) {
		st := TurnState{Working: true, Reason: "background", Tasks: s.heldLocked(), Agents: s.heldAgentsLocked(), Autonomous: s.autonomous}
		if s.reported != st {
			s.stateLocked(st)
		}
		return
	}
	if s.reported.Working {
		s.stateLocked(TurnState{Reason: s.idleStop})
		s.idleStop = ""
	}
}

// autonomousOverLocked reports whether the autonomous turn ended, and arms a
// check for when its quiet wait runs out.
func (s *Session) autonomousOverLocked() bool {
	if s.resulted {
		return true
	}
	if len(s.conn.pendingRequests()) > 0 {
		return false
	}
	limit := autonomousQuiet
	if s.proj.toolsOpen() {
		limit = autonomousStale
	}
	quiet := time.Since(s.lastUpdate)
	if wait := limit - quiet; wait > 0 {
		// A tool call that ended shortens the wait already armed.
		if at := time.Now().Add(wait); s.quietAt.IsZero() || at.Before(s.quietAt) {
			s.quietAt = at
			time.AfterFunc(wait, s.quietCheck)
		}
		return false
	}
	if limit == autonomousStale {
		s.emitLocked(Item{Kind: KindNotice, Notice: &Notice{Severity: "warning", Title: "Background turn ended",
			Description: fmt.Sprintf("The agent sent nothing for %s while a tool call stayed open, so Aether stopped waiting for the turn's end.", limit)}})
	}
	return true
}

func (s *Session) quietCheck() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.quietAt.After(time.Now()) {
		s.quietAt = time.Time{}
	}
	// Drops the follow-up from the log once it is no longer owed.
	s.logTasksLocked()
	s.settleLocked()
}

func (s *Session) endAutonomousLocked(stop string) {
	s.proj.endTurn()
	s.emitLocked(Item{Kind: KindTurnEnd, StopReason: stop, Autonomous: true, Origin: s.origin})
	s.autonomous, s.resulted, s.origin, s.compacting = false, false, "", false
	// A prompt that stopped short before its background wait keeps its
	// reason through the follow-ups.
	if s.idleStop == "" || s.idleStop == "end_turn" {
		s.idleStop = stop
	}
	if len(s.queue) > 0 && !s.closingLocked() {
		next := s.queue[0]
		s.queue = s.queue[1:]
		s.startTurnLocked(next.blocks, next.delivered)
		return
	}
	s.followUpLocked()
}

func (s *Session) stateLocked(st TurnState) {
	s.reported = st
	s.reported.Failed = nil
	if s.cfg.OnState != nil {
		s.callback(func() { s.cfg.OnState(st) })
	}
}

// dismissLocked stops waiting for the live tasks; they still end and report
// back on their own.
func (s *Session) dismissLocked() {
	for _, t := range s.tasks {
		t.dismissed = true
	}
	s.logTasksLocked()
}

// cancelBackgroundLocked ends the autonomous turn and the wait for tasks when
// no prompt is open.
func (s *Session) cancelBackgroundLocked() {
	if s.turnActive || s.closed || !s.backgroundLocked() {
		return
	}
	s.owedUntil, s.endedInTurn, s.idleStop = time.Time{}, false, "cancelled"
	s.dismissLocked()
	if s.autonomous {
		s.endAutonomousLocked("cancelled")
	}
	s.settleLocked()
}
