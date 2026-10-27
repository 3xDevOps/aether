package acphost

import (
	"context"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"
)

// TestProgressCountsOnlyAgentWork: a session/load replay, Aether's own calls
// and usage or command updates leave LastProgress alone; a thought, a tool
// call or a compaction moves it, and the open tool and compaction show.
func TestProgressCountsOnlyAgentWork(t *testing.T) {
	m := newMockAgent(t, loadFixture(t, "codex"))
	m.resume = func() *acp.RequestError { return &acp.RequestError{Code: -32603, Message: "Internal error"} }
	steps := make(chan any)
	m.onPrompt = func(m *mockAgent, call promptCall) (any, *acp.RequestError) {
		for u := range steps {
			m.update(u)
		}
		return map[string]any{"stopReason": "end_turn"}, nil
	}
	s, rec := startMock(t, m, Config{SessionID: m.sessionID()})
	if !m.called(acp.AgentMethodSessionLoad) {
		t.Fatalf("methods %v, want a session/load replay", m.methods)
	}
	rec.waitIdle(t)
	st := s.State()
	if !st.LastProgress.IsZero() || st.OpenedAt.IsZero() {
		t.Fatalf("after the replay LastProgress = %v, OpenedAt = %v; want none since the open", st.LastProgress, st.OpenedAt)
	}

	if _, err := s.Prompt(context.Background(), textPrompt("work"), false, nil); err != nil {
		t.Fatal(err)
	}
	transport := s.State().LastActivity
	steps <- map[string]any{"sessionUpdate": "usage_update", "used": 1, "size": 100}
	steps <- map[string]any{"sessionUpdate": "available_commands_update", "availableCommands": []any{}}
	if _, _, err := s.ListSessions(context.Background(), "/workspace", ""); err != nil {
		t.Fatal(err)
	}
	st = s.State()
	if !st.TurnInFlight || st.TurnStartedAt.IsZero() || !st.LastProgress.IsZero() || !st.LastActivity.After(transport) {
		t.Fatalf("after chatter: %+v; want a turn with transport activity and no progress", st)
	}

	steps <- map[string]any{"sessionUpdate": "agent_thought_chunk", "content": map[string]any{"type": "text", "text": "hm"}}
	waitFor(t, "progress", func() bool { return s.State().LastProgress.After(s.State().TurnStartedAt) })
	steps <- map[string]any{"sessionUpdate": "tool_call", "toolCallId": "t1", "kind": "execute", "title": "make", "status": "in_progress"}
	waitFor(t, "an open tool", func() bool { return s.State().ToolsOpen })
	steps <- map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "t1", "status": "completed"}
	waitFor(t, "the tool to close", func() bool { return !s.State().ToolsOpen })
	before := s.State().LastProgress
	time.Sleep(time.Millisecond)
	steps <- map[string]any{"sessionUpdate": "compaction_update", "compactionId": "c1", "status": "in_progress"}
	waitFor(t, "compacting", func() bool { st := s.State(); return st.Compacting && st.LastProgress.After(before) })
	steps <- map[string]any{"sessionUpdate": "compaction_update", "compactionId": "c1", "status": "completed"}
	waitFor(t, "compaction to end", func() bool { return !s.State().Compacting })
	close(steps)
	rec.waitIdle(t)
	if st := s.State(); st.TurnInFlight || st.ToolsOpen || st.Compacting {
		t.Fatalf("after the turn: %+v", st)
	}
}
