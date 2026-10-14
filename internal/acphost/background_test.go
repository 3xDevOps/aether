package acphost

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"github.com/3xDevOps/Aether/internal/acphost/acpmock"
)

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func (r *recorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.states)
}

func (r *recorder) reports() []TurnState {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.turns)
}

func (r *recorder) waitStates(t *testing.T, want ...string) {
	t.Helper()
	waitFor(t, "states "+strings.Join(want, ", "), func() bool { return len(r.snapshot()) >= len(want) })
	if got := r.snapshot(); !slices.Equal(got, want) {
		t.Fatalf("states %v, want %v", got, want)
	}
}

func (r *recorder) noMoreStates(t *testing.T, want ...string) {
	t.Helper()
	time.Sleep(60 * time.Millisecond)
	if got := r.snapshot(); !slices.Equal(got, want) {
		t.Fatalf("states %v, want %v", got, want)
	}
}

// waitFinished waits for the first idle state after the run last worked and
// returns all states reported up to and including it.
func (r *recorder) waitFinished(t *testing.T) []string {
	t.Helper()
	waitFor(t, "an idle state", func() bool {
		states := r.snapshot()
		return len(states) > 0 && strings.HasPrefix(states[len(states)-1], "idle:")
	})
	return r.snapshot()
}

func shortenWaits(t *testing.T, quiet, stale time.Duration) {
	t.Helper()
	oldQuiet, oldStale := autonomousQuiet, autonomousStale
	autonomousQuiet, autonomousStale = quiet, stale
	t.Cleanup(func() { autonomousQuiet, autonomousStale = oldQuiet, oldStale })
}

func mockDelay(t *testing.T, d time.Duration) {
	t.Helper()
	old := acpmock.BackgroundDelay
	acpmock.BackgroundDelay = d
	t.Cleanup(func() { acpmock.BackgroundDelay = old })
}

func (m *mockAgent) prompts() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, method := range m.methods {
		if method == acp.AgentMethodSessionPrompt {
			n++
		}
	}
	return n
}

func (m *mockAgent) sdk(message map[string]any) {
	if err := m.conn.SendNotification(context.Background(), "_claude/sdkMessage", map[string]any{"sessionId": m.sessionID(), "message": message}); err != nil {
		m.t.Error(err)
	}
}

// liveTasks reports the full set of live tasks as id, type pairs.
func (m *mockAgent) liveTasks(tasks ...[2]string) {
	set := []map[string]any{}
	for _, task := range tasks {
		set = append(set, map[string]any{"task_id": task[0], "task_type": task[1], "description": task[0]})
	}
	m.sdk(map[string]any{"type": "system", "subtype": "background_tasks_changed", "tasks": set})
}

func (m *mockAgent) workflowDone() {
	m.sdk(map[string]any{"type": "system", "subtype": "task_notification", "task_id": "wf-1", "status": "completed"})
}

func (m *mockAgent) chunk(text string) {
	m.update(map[string]any{"sessionUpdate": "agent_message_chunk", "messageId": text, "content": map[string]any{"type": "text", "text": text}})
}

func (m *mockAgent) marker() {
	m.update(map[string]any{"sessionUpdate": "usage_update", "used": 1, "size": 2, "_meta": map[string]any{"_claude/origin": map[string]any{"kind": "task-notification"}}})
}

func (m *mockAgent) openTool(id string) {
	m.update(map[string]any{"sessionUpdate": "tool_call", "toolCallId": id, "title": "Bash", "kind": "execute", "status": "in_progress"})
}

// workflowAgent starts workflow wf-1 in the first prompt and ends every
// prompt plainly.
func workflowAgent(t *testing.T) *mockAgent {
	m := newMockAgent(t, loadFixture(t, "claude"))
	m.onPrompt = func(m *mockAgent, _ promptCall) (any, *acp.RequestError) {
		if m.prompts() == 1 {
			m.liveTasks([2]string{"wf-1", "local_workflow"})
		}
		return map[string]any{"stopReason": "end_turn"}, nil
	}
	return m
}

func startWorkflow(t *testing.T, m *mockAgent) (*Session, *recorder) {
	t.Helper()
	s, rec := startMock(t, m, Config{ClaudeCode: true})
	if _, err := s.Prompt(context.Background(), textPrompt("go"), false, nil); err != nil {
		t.Fatal(err)
	}
	rec.waitStates(t, "working:prompt", "working:background")
	return s, rec
}

func turnEnds(s *Session, t *testing.T) []Item { return ofKind(items(t, s), KindTurnEnd) }

func lastBackground(t *testing.T, s *Session) Item {
	t.Helper()
	bg := ofKind(items(t, s), KindBackground)
	if len(bg) == 0 {
		t.Fatal("no background item logged")
	}
	return bg[len(bg)-1]
}

func nonIncreasing(v []int) bool {
	return slices.IsSortedFunc(v, func(a, b int) int { return b - a })
}

func holding(it Item) int {
	n := 0
	for _, task := range it.Tasks {
		if task.Holds {
			n++
		}
	}
	return n
}

func startReal(t *testing.T, cfg Config) (*Session, *recorder) {
	t.Helper()
	agentIn, hostOut := io.Pipe()
	hostIn, agentOut := io.Pipe()
	agent := acpmock.New(loadFixture(t, "claude"))
	go func() {
		agent.Serve(agentIn, agentOut)
		_ = agentOut.Close()
	}()
	if cfg.LogPath == "" {
		cfg.LogPath = filepath.Join(t.TempDir(), "run.items.jsonl")
	}
	cfg.Cwd, cfg.Logger = "/workspace", discard
	rec := newRecorder()
	s, err := Start(t.Context(), hostIn, hostOut, rec.config(cfg))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(); <-s.Done() })
	return s, rec
}

func TestConcurrentWorkflowsKeepTheRunWorkingUntilTheLastFollowUp(t *testing.T) {
	mockDelay(t, 150*time.Millisecond)
	s, rec := startReal(t, Config{ClaudeCode: true})
	if _, err := s.Prompt(context.Background(), textPrompt(acpmock.PromptBackgroundPair), false, nil); err != nil {
		t.Fatal(err)
	}
	rec.waitStates(t, "working:prompt", "working:background")
	if st := rec.reports()[1]; st.Tasks != 2 {
		t.Fatalf("background state %+v, want 2 tasks", st)
	}
	states := rec.waitFinished(t)
	rec.noMoreStates(t, states...)
	if states[len(states)-1] != "idle:end_turn" || slices.Contains(states[:len(states)-1], "idle:end_turn") {
		t.Fatalf("states %v, want exactly one idle:end_turn, last", states)
	}
	var left []int
	for _, st := range rec.reports()[1 : len(states)-1] {
		if !st.Working || st.Reason != "background" {
			t.Fatalf("states %v: %+v is not background work", states, st)
		}
		left = append(left, st.Tasks)
	}
	if left[0] != 2 || !slices.Contains(left, 1) || !nonIncreasing(left) {
		t.Fatalf("task counts %v, want 2 then 1", left)
	}

	its := items(t, s)
	autonomous := func(it Item) bool { return it.Autonomous }
	if n := len(slices.DeleteFunc(ofKind(its, KindTurnStart), func(it Item) bool { return !autonomous(it) })); n != 2 {
		t.Fatalf("%d autonomous turn starts, want 2", n)
	}
	ends := ofKind(its, KindTurnEnd)
	if len(ends) != 3 || ends[0].Autonomous || !ends[1].Autonomous || !ends[2].Autonomous ||
		ends[1].Origin != "task-notification" || ends[2].Origin != "task-notification" {
		t.Fatalf("turn ends %+v", ends)
	}
	var holds []int
	for _, it := range ofKind(its, KindBackground) {
		holds = append(holds, holding(it))
	}
	peak := slices.Index(holds, 2)
	if peak < 0 || !nonIncreasing(holds[peak:]) || !slices.Contains(holds, 1) || holds[len(holds)-1] != 0 {
		t.Fatalf("held tasks over the background items %v, want 2 shrinking to 0", holds)
	}
	if got := messageText(its, "assistant"); !strings.Contains(got, "background done: "+acpmock.BackgroundTaskID+"-2") {
		t.Fatalf("assistant text %q", got)
	}
}

func TestWorkflowToolCallCarriesItsTask(t *testing.T) {
	mockDelay(t, 100*time.Millisecond)
	s, rec := startReal(t, Config{ClaudeCode: true})
	if _, err := s.Prompt(context.Background(), textPrompt(acpmock.PromptBackground), false, nil); err != nil {
		t.Fatal(err)
	}
	rec.waitFinished(t)
	call := acpmock.BackgroundTaskID + "-call"
	var snapshots []Item
	for _, it := range ofKind(items(t, s), KindToolCall) {
		if it.ToolCall.ID == call && it.ToolCall.Task != nil {
			snapshots = append(snapshots, it)
		}
	}
	if len(snapshots) < 2 {
		t.Fatalf("%d snapshots of %s carry a task, want its launch and its end", len(snapshots), call)
	}
	first, last := snapshots[0], snapshots[len(snapshots)-1]
	if *first.ToolCall.Task != (ToolTask{ID: acpmock.BackgroundTaskID, Type: "local_workflow", Status: "running"}) ||
		*last.ToolCall.Task != (ToolTask{ID: acpmock.BackgroundTaskID, Type: "local_workflow", Status: "completed"}) {
		t.Fatalf("task went from %+v to %+v", first.ToolCall.Task, last.ToolCall.Task)
	}
	last.ToolCall.Title = strings.Repeat("x", 8<<10)
	b, truncated, err := last.Wire(1024)
	if err != nil || !truncated || len(b) > 1024 {
		t.Fatalf("wire: %d bytes, truncated %v, %v", len(b), truncated, err)
	}
	var got Item
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.ToolCall.Task == nil || *got.ToolCall.Task != *last.ToolCall.Task {
		t.Fatalf("brief form lost the task: %s", b)
	}
}

func TestPromptsLeaveTheTrackedWorkflowAlone(t *testing.T) {
	mockDelay(t, 10*time.Second)
	s, rec := startReal(t, Config{ClaudeCode: true})
	if _, err := s.Prompt(context.Background(), textPrompt(acpmock.PromptBackground), false, nil); err != nil {
		t.Fatal(err)
	}
	rec.waitStates(t, "working:prompt", "working:background")

	if _, err := s.Prompt(context.Background(), textPrompt("also this"), false, nil); err != nil {
		t.Fatal(err)
	}
	rec.waitStates(t, "working:prompt", "working:background", "working:prompt", "working:background")
	if ok, err := s.PromptIdle(context.Background(), textPrompt("mail"), nil); !ok || err != nil {
		t.Fatalf("PromptIdle = %v, %v, want true, nil: tasks alone do not make the run busy", ok, err)
	}
	want := []string{"working:prompt", "working:background", "working:prompt", "working:background", "working:prompt", "working:background"}
	rec.waitStates(t, want...)
	rec.noMoreStates(t, want...)
	st := s.State()
	if !st.Background || len(st.Tasks) != 1 || st.Tasks[0].ID != acpmock.BackgroundTaskID || !st.Tasks[0].Holds {
		t.Fatalf("state %+v, want the workflow still tracked", st)
	}
	if got := rec.reports()[5].Tasks; got != 1 {
		t.Fatalf("background state after the last prompt counts %d tasks, want 1", got)
	}
}

func TestDevServerDoesNotHoldTheRun(t *testing.T) {
	s, rec := startReal(t, Config{ClaudeCode: true})
	if _, err := s.Prompt(context.Background(), textPrompt(acpmock.PromptDevServer), false, nil); err != nil {
		t.Fatal(err)
	}
	rec.waitStates(t, "working:prompt", "idle:end_turn")
	rec.noMoreStates(t, "working:prompt", "idle:end_turn")
	st := s.State()
	if st.Background || st.TurnInFlight || len(st.Tasks) != 1 || st.Tasks[0].ID != acpmock.DevServerTaskID || st.Tasks[0].Holds {
		t.Fatalf("state %+v, want the shell listed without holding", st)
	}
	if bg := lastBackground(t, s); len(bg.Tasks) != 1 || bg.Tasks[0].ID != acpmock.DevServerTaskID || bg.Tasks[0].Type != "local_bash" || bg.Tasks[0].Holds {
		t.Fatalf("background item %+v", bg)
	}
}

func TestOutOfTurnOutputFromOtherAgentsIsOnlyLogged(t *testing.T) {
	m := newMockAgent(t, loadFixture(t, "claude"))
	m.onPrompt = func(m *mockAgent, _ promptCall) (any, *acp.RequestError) {
		time.AfterFunc(100*time.Millisecond, func() { m.chunk("late words") })
		return map[string]any{"stopReason": "end_turn"}, nil
	}
	s, rec := startMock(t, m, Config{})
	if _, err := s.Prompt(context.Background(), textPrompt("go"), false, nil); err != nil {
		t.Fatal(err)
	}
	rec.waitStates(t, "working:prompt", "idle:end_turn")
	waitFor(t, "the late text", func() bool { return strings.Contains(messageText(items(t, s), "assistant"), "late words") })
	rec.noMoreStates(t, "working:prompt", "idle:end_turn")
	if starts := ofKind(items(t, s), KindTurnStart); len(starts) != 1 {
		t.Fatalf("turn starts %+v, want only the prompt's", starts)
	}
	if st := s.State(); st.TurnInFlight || st.Background {
		t.Fatalf("state %+v", st)
	}
	m.mu.Lock()
	params := string(m.received[acp.AgentMethodSessionNew])
	m.mu.Unlock()
	if strings.Contains(params, "emitRawSDKMessages") {
		t.Fatalf("session/new asked a non-Claude agent for task frames: %s", params)
	}
}

func TestCancelWithOnlyTasksPending(t *testing.T) {
	mockDelay(t, 10*time.Second)
	path := filepath.Join(t.TempDir(), "run.items.jsonl")
	s, rec := startReal(t, Config{ClaudeCode: true, LogPath: path})
	if _, err := s.Prompt(context.Background(), textPrompt(acpmock.PromptBackground), false, nil); err != nil {
		t.Fatal(err)
	}
	rec.waitStates(t, "working:prompt", "working:background")
	if err := s.Cancel(context.Background()); err != nil {
		t.Fatal(err)
	}
	rec.waitStates(t, "working:prompt", "working:background", "idle:cancelled")
	if st := s.State(); st.Background || st.TurnInFlight {
		t.Fatalf("state %+v", st)
	}
	if bg := lastBackground(t, s); len(bg.Tasks) != 1 || bg.Tasks[0].Holds {
		t.Fatalf("background item %+v, want the workflow listed without holding", bg)
	}
	_ = s.Close()
	<-s.Done()

	log, err := OpenLog(path)
	if err != nil {
		t.Fatal(err)
	}
	interrupted := Interrupted(log)
	_ = log.Close()
	if interrupted {
		t.Fatal("a cancelled wait reads as interrupted")
	}
	_, restored := startReal(t, Config{ClaudeCode: true, LogPath: path, SessionID: loadFixture(t, "claude").SessionID()})
	if reason := restored.waitIdle(t); reason != "" {
		t.Fatalf("restored idle reason %q, want none", reason)
	}
}

func TestCancelEndsAnAutonomousTurnAndTheWait(t *testing.T) {
	m := workflowAgent(t)
	s, rec := startWorkflow(t, m)
	m.chunk("working")
	waitFor(t, "autonomous turn", func() bool { return s.State().TurnInFlight })
	if err := s.Cancel(context.Background()); err != nil {
		t.Fatal(err)
	}
	rec.waitStates(t, "working:prompt", "working:background", "working:autonomous", "idle:cancelled")
	end := turnEnds(s, t)[1]
	if !end.Autonomous || end.StopReason != "cancelled" {
		t.Fatalf("turn end %+v", end)
	}
	if bg := lastBackground(t, s); holding(bg) != 0 || len(bg.Tasks) != 1 {
		t.Fatalf("background item %+v, want the workflow listed without holding", bg)
	}
	if st := s.State(); st.Background || st.TurnInFlight {
		t.Fatalf("state %+v", st)
	}
}

func TestCancelledPromptDismissesTheTasksItLaunched(t *testing.T) {
	m := newMockAgent(t, loadFixture(t, "claude"))
	m.onPrompt = func(m *mockAgent, _ promptCall) (any, *acp.RequestError) {
		m.liveTasks([2]string{"wf-1", "local_workflow"})
		return map[string]any{"stopReason": "cancelled"}, nil
	}
	s, rec := startMock(t, m, Config{ClaudeCode: true})
	if _, err := s.Prompt(context.Background(), textPrompt("go"), false, nil); err != nil {
		t.Fatal(err)
	}
	rec.waitStates(t, "working:prompt", "idle:cancelled")
	rec.noMoreStates(t, "working:prompt", "idle:cancelled")
	if st := s.State(); st.Background {
		t.Fatalf("state %+v", st)
	}
}

func TestFailedPromptWithTasksPendingWaitsForTheFollowUp(t *testing.T) {
	m := newMockAgent(t, loadFixture(t, "claude"))
	m.onPrompt = func(m *mockAgent, _ promptCall) (any, *acp.RequestError) {
		m.liveTasks([2]string{"wf-1", "local_workflow"})
		return nil, &acp.RequestError{Code: -32603, Message: "Internal error"}
	}
	s, rec := startMock(t, m, Config{ClaudeCode: true})
	if _, err := s.Prompt(context.Background(), textPrompt("go"), false, nil); err == nil || !strings.Contains(err.Error(), "Internal error") {
		t.Fatalf("Prompt error %v, want the agent's refusal", err)
	}
	rec.waitStates(t, "working:prompt", "idle:error")
	rec.noMoreStates(t, "working:prompt", "idle:error")
	if failed := rec.reports()[1].Failed; failed == nil || !strings.Contains(failed.Error(), "Internal error") {
		t.Fatalf("idle state failed with %v", failed)
	}

	m.chunk("follow-up")
	waitFor(t, "the working state", func() bool { return len(rec.snapshot()) == 3 })
	if got := rec.snapshot(); got[2] != "working:autonomous" {
		t.Fatalf("states %v, want working:autonomous after the follow-up began", got)
	}
	m.workflowDone()
	m.marker()
	states := rec.waitFinished(t)
	if states[len(states)-1] != "idle:end_turn" || slices.Contains(states[2:len(states)-1], "idle:error") {
		t.Fatalf("states %v", states)
	}
	if failed := rec.reports()[len(states)-1].Failed; failed != nil {
		t.Fatalf("the follow-up's idle still carries %v", failed)
	}
}

func TestShortStopKeepsItsReasonThroughTheBackgroundWait(t *testing.T) {
	m := newMockAgent(t, loadFixture(t, "claude"))
	m.onPrompt = func(m *mockAgent, _ promptCall) (any, *acp.RequestError) {
		m.liveTasks([2]string{"wf-1", "local_workflow"})
		return map[string]any{"stopReason": "max_tokens"}, nil
	}
	s, rec := startMock(t, m, Config{ClaudeCode: true})
	if _, err := s.Prompt(context.Background(), textPrompt("go"), false, nil); err != nil {
		t.Fatal(err)
	}
	rec.waitStates(t, "working:prompt", "working:background")
	m.workflowDone()
	m.chunk("follow-up")
	m.marker()
	if states := rec.waitFinished(t); states[len(states)-1] != "idle:max_tokens" {
		t.Fatalf("states %v, want the prompt's max_tokens kept", states)
	}

	n := len(rec.snapshot())
	m.chunk("on its own")
	m.marker()
	waitFor(t, "the autonomous turn's idle", func() bool {
		states := rec.snapshot()
		return len(states) > n && strings.HasPrefix(states[len(states)-1], "idle:")
	})
	if states := rec.snapshot(); states[len(states)-1] != "idle:end_turn" {
		t.Fatalf("states %v, want the autonomous turn's own end_turn", states)
	}
}

func TestConnectionEndWhileATaskHolds(t *testing.T) {
	m := workflowAgent(t)
	s, rec := startWorkflow(t, m)
	_ = m.stdout.Close()
	select {
	case <-s.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("session did not end")
	}
	rec.waitStates(t, "working:prompt", "working:background", "idle:interrupted")
	log, err := OpenLog(s.cfg.LogPath)
	if err != nil {
		t.Fatal(err)
	}
	its, err := log.ReadAfter(0, 0)
	interrupted := Interrupted(log)
	_ = log.Close()
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, it := range ofKind(its, KindNotice) {
		if it.Notice.Title == "Background work interrupted" && strings.Contains(it.Notice.Description, "wf-1") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no background notice in %+v", ofKind(its, KindNotice))
	}
	if !interrupted {
		t.Fatal("a log that ended with a task held is not interrupted")
	}
	next := newMockAgent(t, loadFixture(t, "claude"))
	_, restored := startMock(t, next, Config{ClaudeCode: true, LogPath: s.cfg.LogPath, SessionID: next.sessionID()})
	if reason := restored.waitIdle(t); reason != "interrupted" {
		t.Fatalf("restored idle reason %q, want interrupted", reason)
	}
}

func TestQueuedPromptWaitsForTheAutonomousTurn(t *testing.T) {
	m := workflowAgent(t)
	s, rec := startWorkflow(t, m)
	m.chunk("finished")
	waitFor(t, "autonomous turn", func() bool { return s.State().TurnInFlight })
	if ok, err := s.PromptIdle(context.Background(), textPrompt("idle?"), nil); ok || err != nil {
		t.Fatalf("PromptIdle = %v, %v, want false, nil", ok, err)
	}
	if r, err := s.Prompt(context.Background(), textPrompt("later"), false, nil); err != nil || r.Outcome != OutcomeQueued {
		t.Fatalf("Prompt = %+v, %v, want queued", r, err)
	}
	if m.prompts() != 1 {
		t.Fatal("a prompt reached the agent during its autonomous turn")
	}
	m.workflowDone()
	m.marker()
	states := rec.waitFinished(t)
	// wf-1 ended during the autonomous turn, so its follow-up may trail the
	// queued prompt's turn.
	if n := len(states); states[n-3] != "working:prompt" || states[n-2] != "working:background" || states[n-1] != "idle:end_turn" || m.prompts() != 2 {
		t.Fatalf("states %v after the queued prompt, %d prompts", states, m.prompts())
	}
}

func TestAutonomousTurnWithoutAMarker(t *testing.T) {
	shortenWaits(t, 100*time.Millisecond, 15*time.Minute)
	plain := func(t *testing.T) (*mockAgent, *Session, *recorder) {
		m := newMockAgent(t, loadFixture(t, "claude"))
		m.onPrompt = func(*mockAgent, promptCall) (any, *acp.RequestError) {
			return map[string]any{"stopReason": "end_turn"}, nil
		}
		s, rec := startMock(t, m, Config{ClaudeCode: true})
		if _, err := s.Prompt(context.Background(), textPrompt("go"), false, nil); err != nil {
			t.Fatal(err)
		}
		rec.waitStates(t, "working:prompt", "idle:end_turn")
		return m, s, rec
	}

	t.Run("goes quiet", func(t *testing.T) {
		m, _, rec := plain(t)
		m.chunk("hello")
		rec.waitStates(t, "working:prompt", "idle:end_turn", "working:autonomous", "idle:end_turn")
	})

	t.Run("an open tool call holds the turn", func(t *testing.T) {
		m, s, rec := plain(t)
		m.openTool("t9")
		rec.waitStates(t, "working:prompt", "idle:end_turn", "working:autonomous")
		time.Sleep(3 * autonomousQuiet)
		rec.noMoreStates(t, "working:prompt", "idle:end_turn", "working:autonomous")
		if !s.State().TurnInFlight {
			t.Fatal("turn ended with a tool call open")
		}
		m.update(map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "t9", "status": "completed"})
		rec.waitStates(t, "working:prompt", "idle:end_turn", "working:autonomous", "idle:end_turn")
	})

	t.Run("a call left running by an ended turn does not hold it", func(t *testing.T) {
		m := newMockAgent(t, loadFixture(t, "claude"))
		m.onPrompt = func(m *mockAgent, _ promptCall) (any, *acp.RequestError) {
			m.openTool("stale")
			return map[string]any{"stopReason": "end_turn"}, nil
		}
		s, rec := startMock(t, m, Config{ClaudeCode: true})
		if _, err := s.Prompt(context.Background(), textPrompt("go"), false, nil); err != nil {
			t.Fatal(err)
		}
		rec.waitStates(t, "working:prompt", "idle:end_turn")
		m.chunk("hello")
		rec.waitStates(t, "working:prompt", "idle:end_turn", "working:autonomous", "idle:end_turn")
	})

	t.Run("stragglers do not open a turn", func(t *testing.T) {
		m, s, rec := plain(t)
		m.update(map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "old", "status": "completed"})
		m.update(map[string]any{"sessionUpdate": "session_info_update", "title": "t"})
		m.update(map[string]any{"sessionUpdate": "usage_update", "used": 1, "size": 2})
		time.Sleep(2 * autonomousQuiet)
		rec.noMoreStates(t, "working:prompt", "idle:end_turn")
		if st := s.State(); st.TurnInFlight || st.Background {
			t.Fatalf("state %+v", st)
		}
		if starts := ofKind(items(t, s), KindTurnStart); len(starts) != 1 {
			t.Fatalf("turn starts %+v", starts)
		}
	})
}

func TestStaleToolCallEndsTheAutonomousTurn(t *testing.T) {
	shortenWaits(t, 50*time.Millisecond, 400*time.Millisecond)
	m := newMockAgent(t, loadFixture(t, "claude"))
	m.onPrompt = func(*mockAgent, promptCall) (any, *acp.RequestError) {
		return map[string]any{"stopReason": "end_turn"}, nil
	}
	s, rec := startMock(t, m, Config{ClaudeCode: true})
	if _, err := s.Prompt(context.Background(), textPrompt("go"), false, nil); err != nil {
		t.Fatal(err)
	}
	rec.waitStates(t, "working:prompt", "idle:end_turn")
	opened := time.Now()
	m.openTool("lost")
	rec.waitStates(t, "working:prompt", "idle:end_turn", "working:autonomous", "idle:end_turn")
	if elapsed := time.Since(opened); elapsed < 4*autonomousQuiet {
		t.Fatalf("the turn ended after %v, before the stale limit counted from its open call", elapsed)
	}
	var found bool
	for _, it := range ofKind(items(t, s), KindNotice) {
		found = found || it.Notice.Title == "Background turn ended" && it.Notice.Severity == "warning"
	}
	if !found {
		t.Fatalf("no stale notice in %+v", ofKind(items(t, s), KindNotice))
	}
	if ends := turnEnds(s, t); len(ends) != 2 || !ends[1].Autonomous {
		t.Fatalf("turn ends %+v", ends)
	}
}

// rawAgent answers each session/prompt through answer, which writes straight
// to the host so a test controls what follows the response on the wire.
func rawAgent(t *testing.T, fix fixture, answer func(id json.RawMessage, send func(any))) (io.Reader, io.WriteCloser) {
	t.Helper()
	hostIn, agentOut := io.Pipe()
	agentIn, hostOut := io.Pipe()
	t.Cleanup(func() { _ = agentOut.Close(); _ = hostOut.Close() })
	go func() {
		defer func() { _ = agentOut.Close() }()
		send := func(v any) {
			b, _ := json.Marshal(v)
			_, _ = agentOut.Write(append(b, '\n'))
		}
		scan := bufio.NewScanner(agentIn)
		scan.Buffer(nil, 1<<20)
		for scan.Scan() {
			var m rpcHead
			if json.Unmarshal(scan.Bytes(), &m) != nil {
				continue
			}
			result := func(r any) { send(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": r}) }
			switch m.Method {
			case acp.AgentMethodInitialize:
				result(fix.Initialize)
			case acp.AgentMethodSessionNew:
				result(fix.SessionNew)
			case acp.AgentMethodSessionPrompt:
				answer(m.ID, send)
			}
		}
	}()
	return hostIn, hostOut
}

func TestUpdatesRightBehindThePromptAnswerOpenAnAutonomousTurn(t *testing.T) {
	fix := loadFixture(t, "claude")
	var n int
	r, w := rawAgent(t, fix, func(id json.RawMessage, send func(any)) {
		n++
		update := func(u map[string]any) {
			send(map[string]any{"jsonrpc": "2.0", "method": acp.ClientMethodSessionUpdate, "params": map[string]any{"sessionId": fix.SessionID(), "update": u}})
		}
		send(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{"stopReason": "end_turn"}})
		update(map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "after-" + strconv.Itoa(n)}})
		update(map[string]any{"sessionUpdate": "usage_update", "used": 1, "size": 2, "_meta": map[string]any{"_claude/origin": map[string]any{"kind": "task-notification"}}})
	})
	rec := newRecorder()
	s, err := Start(t.Context(), r, w, rec.config(Config{ClaudeCode: true, LogPath: filepath.Join(t.TempDir(), "run.items.jsonl"), Cwd: "/workspace", Logger: discard}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(); <-s.Done() })

	const prompts = 25
	for i := range prompts {
		if _, err := s.Prompt(context.Background(), textPrompt("go"), false, nil); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "autonomous turn "+strconv.Itoa(i), func() bool {
			ends := turnEnds(s, t)
			return len(ends) == 2*(i+1) && !s.State().TurnInFlight
		})
	}
	its := items(t, s)
	autonomous := map[int64]bool{}
	for _, it := range ofKind(its, KindTurnStart) {
		autonomous[it.Turn] = it.Autonomous
	}
	var texts int
	for _, it := range ofKind(its, KindMessage) {
		if it.Message.Role == "assistant" && strings.HasPrefix(it.Message.Text, "after-") {
			texts++
			if !autonomous[it.Turn] {
				t.Fatalf("%q was logged under the prompt's turn %d", it.Message.Text, it.Turn)
			}
		}
	}
	if texts != prompts {
		t.Fatalf("%d texts after the answers, want %d", texts, prompts)
	}
}

func TestAnsweringAnAutonomousQuestionRestartsTheQuietWait(t *testing.T) {
	shortenWaits(t, 300*time.Millisecond, 15*time.Minute)
	m := newMockAgent(t, loadFixture(t, "claude"))
	m.onPrompt = func(*mockAgent, promptCall) (any, *acp.RequestError) {
		return map[string]any{"stopReason": "end_turn"}, nil
	}
	s, rec := startMock(t, m, Config{ClaudeCode: true})
	if _, err := s.Prompt(context.Background(), textPrompt("go"), false, nil); err != nil {
		t.Fatal(err)
	}
	rec.waitStates(t, "working:prompt", "idle:end_turn")
	m.chunk("hello")
	rec.waitStates(t, "working:prompt", "idle:end_turn", "working:autonomous")

	replied := make(chan string, 1)
	go func() {
		res, err := m.request(context.Background(), acp.ClientMethodElicitationCreate, map[string]any{
			"sessionId": m.sessionID(), "mode": "form", "message": "Which branch?",
			"requestedSchema": map[string]any{"type": "object", "properties": map[string]any{"branch": map[string]any{"type": "string"}}},
		})
		if err != nil {
			replied <- err.Error()
			return
		}
		replied <- string(res)
	}()
	q := rec.waitInputs(t, 1)[0]
	time.Sleep(2 * autonomousQuiet)
	if st := s.State(); !st.TurnInFlight {
		t.Fatal("the autonomous turn ended with a question open")
	}
	if err := s.Answer(q.ID, "accept", map[string]any{"branch": "main"}); err != nil {
		t.Fatal(err)
	}
	if res := <-replied; res != `{"action":"accept","content":{"branch":"main"}}` {
		t.Fatalf("agent got %s", res)
	}
	time.Sleep(autonomousQuiet / 3)
	if n := len(turnEnds(s, t)); n != 1 {
		t.Fatalf("%d turn ends %v after the answer, want only the first prompt's", n, autonomousQuiet/3)
	}
	rec.waitStates(t, "working:prompt", "idle:end_turn", "working:autonomous", "idle:end_turn")
}

func TestRestoreReportsAnInterruptedTurn(t *testing.T) {
	holds := []Task{{ID: "bg-1", Type: "local_workflow", Holds: true}}
	for _, tc := range []struct {
		name string
		log  []Item
		want string
	}{
		{"open turn", []Item{{Kind: KindTurnStart, Turn: 1}}, "interrupted"},
		{"ended turn", []Item{{Kind: KindTurnStart, Turn: 1}, {Kind: KindTurnEnd, Turn: 1, StopReason: "end_turn"}}, ""},
		{"ended turn with a task held", []Item{{Kind: KindTurnStart, Turn: 1}, {Kind: KindBackground, Turn: 1, Tasks: holds}, {Kind: KindTurnEnd, Turn: 1, StopReason: "end_turn"}}, "interrupted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "run.items.jsonl")
			log, err := OpenLog(path)
			if err != nil {
				t.Fatal(err)
			}
			for _, it := range tc.log {
				if err := log.Append(&it); err != nil {
					t.Fatal(err)
				}
			}
			_ = log.Close()
			m := newMockAgent(t, loadFixture(t, "claude"))
			_, rec := startMock(t, m, Config{LogPath: path, SessionID: m.sessionID()})
			if got := rec.waitIdle(t); got != tc.want {
				t.Fatalf("restored idle reason %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRestoreStopsTasksALostSessionLeftRunning(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.items.jsonl")
	log, err := OpenLog(path)
	if err != nil {
		t.Fatal(err)
	}
	launch := &ToolCall{ID: "call-1", Title: "Workflow", Status: "completed", Task: &ToolTask{ID: "bg-1", Type: "local_workflow", Status: "running"}}
	for _, it := range []Item{
		{Kind: KindTurnStart, Turn: 1},
		{Kind: KindToolCall, Turn: 1, ToolCall: launch},
		{Kind: KindBackground, Turn: 1, Tasks: []Task{{ID: "bg-1", Type: "local_workflow", Holds: true}}},
		{Kind: KindTurnEnd, Turn: 1, StopReason: "end_turn"},
		{Kind: KindTurnStart, Turn: 2},
		{Kind: KindTurnEnd, Turn: 2, StopReason: "end_turn"},
	} {
		if err := log.Append(&it); err != nil {
			t.Fatal(err)
		}
	}
	_ = log.Close()
	var statuses []string
	var turns []int64
	for range 2 {
		m := newMockAgent(t, loadFixture(t, "claude"))
		s, rec := startMock(t, m, Config{ClaudeCode: true, LogPath: path, SessionID: m.sessionID()})
		rec.waitIdle(t)
		statuses, turns = nil, nil
		for _, it := range ofKind(items(t, s), KindToolCall) {
			statuses = append(statuses, it.ToolCall.Task.Status)
			turns = append(turns, it.Turn)
		}
		_ = s.Close()
	}
	if !slices.Equal(statuses, []string{"running", "stopped"}) || !slices.Equal(turns, []int64{1, 1}) {
		t.Fatalf("snapshots of the launching call went %v in turns %v, want running then one stopped, both in turn 1", statuses, turns)
	}
}

func TestSessionMetaCarriesTheSystemPromptAndTaskFrames(t *testing.T) {
	failed := &acp.RequestError{Code: -32603, Message: "Internal error"}
	frames, err := json.Marshal(claudeTaskFrames)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(frames), `{"subtype":"task_progress","type":"system"}`) {
		t.Fatalf("task frames %s lack task_progress", frames)
	}
	for _, tc := range []struct {
		name   string
		cfg    Config
		fail   bool
		method string
	}{
		{"new", Config{}, false, acp.AgentMethodSessionNew},
		{"resume", Config{SessionID: "s1"}, false, acp.AgentMethodSessionResume},
		{"load", Config{SessionID: "s1"}, true, acp.AgentMethodSessionLoad},
	} {
		for _, prompt := range []string{"rules", ""} {
			for _, claude := range []bool{true, false} {
				t.Run(tc.name+" prompt="+prompt+" claude="+strconv.FormatBool(claude), func(t *testing.T) {
					m := newMockAgent(t, loadFixture(t, "claude"))
					if tc.fail {
						m.resume = func() *acp.RequestError { return failed }
					}
					cfg := tc.cfg
					cfg.SystemPrompt, cfg.ClaudeCode = prompt, claude
					startMock(t, m, cfg)
					m.mu.Lock()
					raw := m.received[tc.method]
					m.mu.Unlock()
					var params map[string]json.RawMessage
					if err := json.Unmarshal(raw, &params); err != nil {
						t.Fatalf("%s params %q: %v", tc.method, raw, err)
					}
					meta, has := params["_meta"]
					if prompt == "" && !claude {
						if has {
							t.Fatalf("%s sent _meta %s with nothing to ask for", tc.method, meta)
						}
						return
					}
					var got map[string]json.RawMessage
					if err := json.Unmarshal(meta, &got); err != nil {
						t.Fatalf("%s _meta %s: %v", tc.method, meta, err)
					}
					wantKeys := 0
					if prompt != "" {
						wantKeys++
						if string(got["systemPrompt"]) != `{"append":"rules"}` {
							t.Fatalf("%s _meta %s", tc.method, meta)
						}
					}
					if claude {
						wantKeys++
						if string(got["claudeCode"]) != `{"emitRawSDKMessages":`+string(frames)+`,"options":{"forwardSubagentText":true}}` {
							t.Fatalf("%s _meta %s", tc.method, meta)
						}
					}
					if len(got) != wantKeys {
						t.Fatalf("%s _meta %s has unexpected keys", tc.method, meta)
					}
				})
			}
		}
	}
}

func TestInterrupted(t *testing.T) {
	holds := []Task{{ID: "bg-1", Type: "local_workflow", Holds: true}}
	shell := []Task{{ID: "sh-1", Type: "local_bash"}}
	for _, tc := range []struct {
		name string
		log  []Item
		want bool
	}{
		{"empty", nil, false},
		{"open turn", []Item{{Kind: KindTurnStart, Turn: 1}}, false},
		{"end_turn", []Item{{Kind: KindTurnEnd, StopReason: "end_turn"}}, false},
		{"interrupted", []Item{{Kind: KindTurnEnd, StopReason: "interrupted"}}, true},
		{"task held", []Item{{Kind: KindTurnStart}, {Kind: KindBackground, Tasks: holds}}, true},
		{"task held across a turn end", []Item{{Kind: KindTurnStart}, {Kind: KindBackground, Tasks: holds}, {Kind: KindTurnEnd, StopReason: "end_turn"}}, true},
		{"shell only", []Item{{Kind: KindTurnStart}, {Kind: KindBackground, Tasks: shell}}, false},
		{"tasks ended", []Item{{Kind: KindTurnStart}, {Kind: KindBackground, Tasks: holds}, {Kind: KindBackground}}, false},
		{"turn started after", []Item{{Kind: KindBackground, Tasks: holds}, {Kind: KindTurnStart}}, false},
		{"reset after", []Item{{Kind: KindTurnStart}, {Kind: KindBackground, Tasks: holds}, {Kind: KindReset}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			log, err := OpenLog(filepath.Join(t.TempDir(), "run.items.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = log.Close() }()
			for _, it := range tc.log {
				if err := log.Append(&it); err != nil {
					t.Fatal(err)
				}
			}
			if got := Interrupted(log); got != tc.want {
				t.Fatalf("Interrupted = %v, want %v", got, tc.want)
			}
		})
	}
}
