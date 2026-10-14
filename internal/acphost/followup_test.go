package acphost

import (
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"
)

func shortenFollowUp(t *testing.T, d time.Duration) {
	t.Helper()
	old := followUpGrace
	followUpGrace = d
	t.Cleanup(func() { followUpGrace = old })
}

func (m *mockAgent) launchTool(id, taskID string) {
	m.update(map[string]any{
		"sessionUpdate": "tool_call", "toolCallId": id, "title": "Workflow", "kind": "other", "status": "completed",
		"_meta": map[string]any{"claudeCode": map[string]any{"toolResponse": map[string]any{"status": "async_launched", "taskId": taskID}}},
	})
}

// endedDuringPrompt starts wf-1 and ends it before the first prompt answers.
func endedDuringPrompt(t *testing.T) *mockAgent {
	m := newMockAgent(t, loadFixture(t, "claude"))
	m.onPrompt = func(m *mockAgent, _ promptCall) (any, *acp.RequestError) {
		if m.prompts() == 1 {
			m.liveTasks([2]string{"wf-1", "local_workflow"})
			m.workflowDone()
		}
		return map[string]any{"stopReason": "end_turn"}, nil
	}
	return m
}

func TestTaskEndingDuringAPromptKeepsTheRunWorkingForItsFollowUp(t *testing.T) {
	shortenFollowUp(t, 400*time.Millisecond)
	t.Run("follow-up", func(t *testing.T) {
		m := endedDuringPrompt(t)
		s, rec := startMock(t, m, Config{ClaudeCode: true})
		if _, err := s.Prompt(context.Background(), textPrompt("go"), false, nil); err != nil {
			t.Fatal(err)
		}
		rec.waitStates(t, "working:prompt", "working:background")
		rec.noMoreStates(t, "working:prompt", "working:background")
		m.chunk("report")
		rec.waitStates(t, "working:prompt", "working:background", "working:autonomous")
		m.marker()
		rec.waitStates(t, "working:prompt", "working:background", "working:autonomous", "idle:end_turn")
	})

	t.Run("none", func(t *testing.T) {
		m := endedDuringPrompt(t)
		s, rec := startMock(t, m, Config{ClaudeCode: true})
		if _, err := s.Prompt(context.Background(), textPrompt("go"), false, nil); err != nil {
			t.Fatal(err)
		}
		rec.waitStates(t, "working:prompt", "working:background")
		ended := time.Now()
		rec.waitStates(t, "working:prompt", "working:background", "idle:end_turn")
		if waited := time.Since(ended); waited > 3*followUpGrace {
			t.Fatalf("idle came after %v, want about the %v grace", waited, followUpGrace)
		}
	})
}

func TestConnectionClosingRightAfterAPromptAnswerEndsTheSession(t *testing.T) {
	fix := loadFixture(t, "claude")
	var w io.WriteCloser
	r, w := rawAgent(t, fix, func(id json.RawMessage, send func(any)) {
		send(map[string]any{"jsonrpc": "2.0", "method": acp.ClientMethodSessionUpdate, "params": map[string]any{"sessionId": fix.SessionID(), "update": map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "done"}}}})
		send(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{"stopReason": "end_turn"}})
		_ = w.Close()
	})
	s, err := Start(t.Context(), r, w, newRecorder().config(Config{ClaudeCode: true, LogPath: filepath.Join(t.TempDir(), "run.items.jsonl"), Cwd: "/workspace", Logger: discard}))
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, _ = s.Prompt(context.Background(), textPrompt("go"), false, nil)
	select {
	case <-s.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("session did not end")
	}
	if waited := time.Since(start); waited > 2*time.Second {
		t.Fatalf("session ended after %v, want it as soon as the connection closed", waited)
	}
}

func TestConnectionEndWithATaskHeldAndNoTurnClearsItFromTheLog(t *testing.T) {
	m := workflowAgent(t)
	s, rec := startWorkflow(t, m)
	_ = m.stdout.Close()
	<-s.Done()
	rec.waitStates(t, "working:prompt", "working:background", "idle:interrupted")
	log, err := OpenLog(s.cfg.LogPath)
	if err != nil {
		t.Fatal(err)
	}
	logged, err := log.ReadAfter(0, 0)
	interrupted := Interrupted(log)
	_ = log.Close()
	if err != nil {
		t.Fatal(err)
	}
	bgs := ofKind(logged, KindBackground)
	if bg := bgs[len(bgs)-1]; len(bg.Tasks) != 0 || bg.StopReason != "interrupted" {
		t.Fatalf("background item %+v, want no tasks and stop reason interrupted", bg)
	}
	if !interrupted {
		t.Fatal("log is not interrupted")
	}

	next := newMockAgent(t, loadFixture(t, "claude"))
	restored, rrec := startMock(t, next, Config{ClaudeCode: true, LogPath: s.cfg.LogPath, SessionID: next.sessionID()})
	if reason := rrec.waitIdle(t); reason != "interrupted" {
		t.Fatalf("restored idle reason %q, want interrupted", reason)
	}
	if bg := lastBackground(t, restored); len(bg.Tasks) != 0 {
		t.Fatalf("restored background item %+v lists tasks", bg)
	}
}

func TestRestoreClearsHeldTasksTheConnectionLeftInTheLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.items.jsonl")
	log, err := OpenLog(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range []Item{
		{Kind: KindTurnStart, Turn: 1},
		{Kind: KindTurnEnd, Turn: 1, StopReason: "end_turn"},
		{Kind: KindBackground, Tasks: []Task{{ID: "wf-1", Type: "local_workflow", Holds: true}}},
	} {
		if err := log.Append(&it); err != nil {
			t.Fatal(err)
		}
	}
	_ = log.Close()
	m := newMockAgent(t, loadFixture(t, "claude"))
	s, rec := startMock(t, m, Config{ClaudeCode: true, LogPath: path, SessionID: m.sessionID()})
	if reason := rec.waitIdle(t); reason != "interrupted" {
		t.Fatalf("restored idle reason %q, want interrupted", reason)
	}
	if bg := lastBackground(t, s); len(bg.Tasks) != 0 || bg.StopReason != "interrupted" {
		t.Fatalf("background item %+v, want an empty interrupted one", bg)
	}
	if st := s.State(); len(st.Tasks) != 0 || st.Background {
		t.Fatalf("state %+v", st)
	}
}

func TestToolCallArrivingAfterItsTaskEndedReadsCompleted(t *testing.T) {
	shortenFollowUp(t, 50*time.Millisecond)
	m := newMockAgent(t, loadFixture(t, "claude"))
	m.onPrompt = func(m *mockAgent, _ promptCall) (any, *acp.RequestError) {
		m.workflowDone()
		m.launchTool("t1", "wf-1")
		return map[string]any{"stopReason": "end_turn"}, nil
	}
	s, rec := startMock(t, m, Config{ClaudeCode: true})
	if _, err := s.Prompt(context.Background(), textPrompt("go"), false, nil); err != nil {
		t.Fatal(err)
	}
	rec.waitFinished(t)
	calls := ofKind(items(t, s), KindToolCall)
	if len(calls) == 0 {
		t.Fatal("no tool call logged")
	}
	if task := calls[len(calls)-1].ToolCall.Task; task == nil || task.ID != "wf-1" || task.Status != "completed" {
		t.Fatalf("tool call task %+v, want wf-1 completed", task)
	}
	s.mu.Lock()
	kept := len(s.taskCalls)
	s.mu.Unlock()
	if kept != 0 {
		t.Fatalf("%d tool calls still tracked as running tasks", kept)
	}
}

func TestOwedFollowUpIsLoggedAndExpires(t *testing.T) {
	shortenWaits(t, 300*time.Millisecond, 15*time.Minute)
	m := workflowAgent(t)
	s, rec := startWorkflow(t, m)
	m.workflowDone()
	waitFor(t, "owed follow-up", func() bool { return lastBackgroundOrZero(s, t).FollowUp })
	if bg := lastBackground(t, s); len(bg.Tasks) != 0 {
		t.Fatalf("background item %+v, want the ended task gone", bg)
	}
	log, err := OpenLog(s.cfg.LogPath)
	if err != nil {
		t.Fatal(err)
	}
	interrupted := Interrupted(log)
	_ = log.Close()
	if !interrupted {
		t.Fatal("a log waiting for the follow-up is not interrupted")
	}

	rec.waitStates(t, "working:prompt", "working:background", "working:background", "idle:end_turn")
	if bg := lastBackground(t, s); bg.FollowUp {
		t.Fatalf("background item %+v, want the follow-up dropped once it expired", bg)
	}
	log, err = OpenLog(s.cfg.LogPath)
	if err != nil {
		t.Fatal(err)
	}
	interrupted = Interrupted(log)
	_ = log.Close()
	if interrupted {
		t.Fatal("an expired follow-up reads as interrupted")
	}
}

func TestConnectionEndWhileAFollowUpIsOwedIsInterrupted(t *testing.T) {
	m := workflowAgent(t)
	s, rec := startWorkflow(t, m)
	m.workflowDone()
	waitFor(t, "owed follow-up", func() bool { return lastBackgroundOrZero(s, t).FollowUp })
	_ = m.stdout.Close()
	<-s.Done()
	rec.waitStates(t, "working:prompt", "working:background", "working:background", "idle:interrupted")

	next := newMockAgent(t, loadFixture(t, "claude"))
	_, rrec := startMock(t, next, Config{ClaudeCode: true, LogPath: s.cfg.LogPath, SessionID: next.sessionID()})
	if reason := rrec.waitIdle(t); reason != "interrupted" {
		t.Fatalf("restored idle reason %q, want interrupted", reason)
	}
}

func lastBackgroundOrZero(s *Session, t *testing.T) Item {
	bg := ofKind(items(t, s), KindBackground)
	if len(bg) == 0 {
		return Item{}
	}
	return bg[len(bg)-1]
}

func TestPromptIdleWaitsOutTheOwedFollowUp(t *testing.T) {
	m := workflowAgent(t)
	s, _ := startWorkflow(t, m)
	m.workflowDone()
	waitFor(t, "owed follow-up", func() bool { return lastBackgroundOrZero(s, t).FollowUp })
	if ok, err := s.PromptIdle(context.Background(), textPrompt("idle?"), nil); ok || err != nil {
		t.Fatalf("PromptIdle = %v, %v, want false, nil", ok, err)
	}
	if n := m.prompts(); n != 1 {
		t.Fatalf("%d prompts reached the agent, want only the first", n)
	}
}

func TestPromptIdleReportsDelivery(t *testing.T) {
	t.Run("accepted", func(t *testing.T) {
		m := newMockAgent(t, loadFixture(t, "claude"))
		s, rec := startMock(t, m, Config{})
		delivered := make(chan error, 2)
		if ok, err := s.PromptIdle(context.Background(), textPrompt("go"), func(err error) { delivered <- err }); !ok || err != nil {
			t.Fatalf("PromptIdle = %v, %v", ok, err)
		}
		rec.waitIdle(t)
		if err := <-delivered; err != nil {
			t.Fatalf("delivered with %v, want nil", err)
		}
		select {
		case err := <-delivered:
			t.Fatalf("delivered a second time with %v", err)
		case <-time.After(60 * time.Millisecond):
		}
	})

	t.Run("refused", func(t *testing.T) {
		m := newMockAgent(t, loadFixture(t, "claude"))
		m.onPrompt = func(*mockAgent, promptCall) (any, *acp.RequestError) {
			time.Sleep(promptAcceptGrace + 200*time.Millisecond)
			return nil, &acp.RequestError{Code: -32603, Message: "Internal error"}
		}
		s, rec := startMock(t, m, Config{})
		delivered := make(chan error, 2)
		if ok, err := s.PromptIdle(context.Background(), textPrompt("go"), func(err error) { delivered <- err }); !ok || err != nil {
			t.Fatalf("PromptIdle = %v, %v, want accepted after the grace", ok, err)
		}
		rec.waitIdle(t)
		if err := <-delivered; err == nil || !strings.Contains(err.Error(), "Internal error") {
			t.Fatalf("delivered with %v, want the agent's refusal", err)
		}
	})
}

func TestTaskProgressTouchesActivityOnly(t *testing.T) {
	m := workflowAgent(t)
	s, rec := startWorkflow(t, m)
	before := s.State().LastActivity
	time.Sleep(5 * time.Millisecond)
	m.sdk(map[string]any{"type": "system", "subtype": "task_progress", "task_id": "wf-1"})
	waitFor(t, "activity", func() bool { return s.State().LastActivity.After(before) })
	rec.noMoreStates(t, "working:prompt", "working:background")
}

func TestFollowUpTurnStartingDropsTheOwedFollowUpFromTheLog(t *testing.T) {
	shortenWaits(t, 5*time.Second, 15*time.Minute)
	m := workflowAgent(t)
	s, rec := startWorkflow(t, m)
	m.workflowDone()
	waitFor(t, "owed follow-up", func() bool { return lastBackgroundOrZero(s, t).FollowUp })

	m.chunk("report")
	waitFor(t, "follow-up dropped", func() bool { return !lastBackgroundOrZero(s, t).FollowUp })
	m.marker()
	waitFor(t, "follow-up turn ended", func() bool {
		states := rec.snapshot()
		return len(states) > 0 && states[len(states)-1] == "idle:end_turn"
	})
	if bg := lastBackground(t, s); bg.FollowUp || len(bg.Tasks) != 0 {
		t.Fatalf("background item %+v, want no tasks and no follow-up", bg)
	}

	_ = s.Close()
	_ = m.stdout.Close()
	<-s.Done()
	log, err := OpenLog(s.cfg.LogPath)
	if err != nil {
		t.Fatal(err)
	}
	interrupted := Interrupted(log)
	_ = log.Close()
	if interrupted {
		t.Fatal("a log whose follow-up turn ended reads as interrupted")
	}

	next := newMockAgent(t, loadFixture(t, "claude"))
	restored, rrec := startMock(t, next, Config{ClaudeCode: true, LogPath: s.cfg.LogPath, SessionID: next.sessionID()})
	if reason := rrec.waitIdle(t); reason != "" {
		t.Fatalf("restored idle reason %q, want none", reason)
	}
	for _, bg := range ofKind(items(t, restored), KindBackground) {
		if bg.StopReason == "interrupted" {
			t.Fatalf("restore appended background item %+v", bg)
		}
	}
}
