package acphost

import (
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"slices"
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

func shortenQuiet(t *testing.T, d time.Duration) {
	t.Helper()
	old := autonomousQuiet
	autonomousQuiet = d
	t.Cleanup(func() { autonomousQuiet = old })
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

func (m *mockAgent) toolResult(id string, toolResponse any) {
	m.update(map[string]any{"sessionUpdate": "tool_call", "toolCallId": id, "title": "Bash", "kind": "execute", "status": "in_progress"})
	m.update(map[string]any{
		"sessionUpdate": "tool_call_update", "toolCallId": id, "status": "completed",
		"_meta": map[string]any{"claudeCode": map[string]any{"toolName": "Bash", "toolResponse": toolResponse}},
	})
}

func (m *mockAgent) chunk(text string) {
	m.update(map[string]any{"sessionUpdate": "agent_message_chunk", "messageId": text, "content": map[string]any{"type": "text", "text": text}})
}

func (m *mockAgent) marker() {
	m.update(map[string]any{"sessionUpdate": "usage_update", "used": 1, "size": 2, "_meta": map[string]any{"_claude/origin": map[string]any{"kind": "task-notification"}}})
}

// backgroundAgent launches toolResponse in the first prompt and ends later
// prompts plainly.
func backgroundAgent(t *testing.T, toolResponse any) *mockAgent {
	m := newMockAgent(t, loadFixture(t, "claude"))
	m.onPrompt = func(m *mockAgent, _ promptCall) (any, *acp.RequestError) {
		if m.prompts() == 1 {
			m.toolResult("t1", toolResponse)
		}
		return map[string]any{"stopReason": "end_turn"}, nil
	}
	return m
}

func startBackground(t *testing.T, m *mockAgent) (*Session, *recorder) {
	t.Helper()
	s, rec := startMock(t, m, Config{})
	if _, err := s.Prompt(context.Background(), textPrompt("go"), false, nil); err != nil {
		t.Fatal(err)
	}
	rec.waitStates(t, "working:prompt", "working:background")
	return s, rec
}

func turnEnds(s *Session, t *testing.T) []Item { return ofKind(items(t, s), KindTurnEnd) }

func TestBackgroundTaskKeepsTheSessionWorkingAfterTheTurn(t *testing.T) {
	for _, tc := range []struct {
		name  string
		resp  map[string]any
		tasks []string
	}{
		{"background command", map[string]any{"stdout": "", "backgroundTaskId": "bg-1"}, []string{"bg-1"}},
		{"workflow", map[string]any{"status": "async_launched", "taskId": "wf-1"}, []string{"wf-1"}},
		{"subagent", map[string]any{"status": "async_launched", "agentId": "x"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := backgroundAgent(t, tc.resp)
			s, rec := startMock(t, m, Config{})
			if _, err := s.Prompt(context.Background(), textPrompt("go"), false, nil); err != nil {
				t.Fatal(err)
			}
			want := []string{"working:prompt", "working:background"}
			if tc.tasks == nil {
				want = []string{"working:prompt", "idle:end_turn"}
			}
			rec.waitStates(t, want...)
			rec.noMoreStates(t, want...)
			st := s.State()
			if st.Background != (tc.tasks != nil) || st.TurnInFlight {
				t.Fatalf("state %+v", st)
			}
			ends := turnEnds(s, t)
			if len(ends) != 1 || ends[0].StopReason != "end_turn" || !slices.Equal(ends[0].BackgroundTasks, tc.tasks) {
				t.Fatalf("turn ends %+v, want tasks %v", ends, tc.tasks)
			}
		})
	}
}

func TestAutonomousFollowUp(t *testing.T) {
	t.Run("blocks prompts and settles on the task notification", func(t *testing.T) {
		m := backgroundAgent(t, map[string]any{"backgroundTaskId": "bg-1"})
		s, rec := startBackground(t, m)
		m.chunk("finished")
		waitFor(t, "autonomous turn", func() bool { return s.State().TurnInFlight })
		starts := ofKind(items(t, s), KindTurnStart)
		if len(starts) != 2 || starts[0].Autonomous || !starts[1].Autonomous {
			t.Fatalf("turn starts %+v", starts)
		}
		if ok, err := s.PromptIdle(context.Background(), textPrompt("idle?")); ok || err != nil {
			t.Fatalf("PromptIdle = %v, %v, want false, nil", ok, err)
		}
		if m.prompts() != 1 {
			t.Fatalf("PromptIdle sent a prompt")
		}
		if r, err := s.Prompt(context.Background(), textPrompt("later"), false, nil); err != nil || r.Outcome != OutcomeQueued {
			t.Fatalf("Prompt = %+v, %v, want queued", r, err)
		}
		m.marker()
		rec.waitStates(t, "working:prompt", "working:background", "working:prompt", "idle:end_turn")
		first := turnEnds(s, t)[1]
		if !first.Autonomous || first.Origin != "task-notification" || first.StopReason != "end_turn" || len(first.BackgroundTasks) != 0 {
			t.Fatalf("autonomous turn end %+v", first)
		}
	})

	t.Run("reports idle once without a queued prompt", func(t *testing.T) {
		m := backgroundAgent(t, map[string]any{"backgroundTaskId": "bg-1"})
		s, rec := startBackground(t, m)
		m.chunk("finished")
		m.marker()
		rec.waitStates(t, "working:prompt", "working:background", "idle:end_turn")
		rec.noMoreStates(t, "working:prompt", "working:background", "idle:end_turn")
		if st := s.State(); st.Background || st.TurnInFlight {
			t.Fatalf("state %+v", st)
		}
	})

	t.Run("a task launched by the follow-up outlives its notification", func(t *testing.T) {
		m := backgroundAgent(t, map[string]any{"backgroundTaskId": "bg-1"})
		s, rec := startBackground(t, m)
		m.chunk("again")
		m.toolResult("t2", map[string]any{"backgroundTaskId": "bg-2"})
		m.marker()
		waitFor(t, "first follow-up end", func() bool { return len(turnEnds(s, t)) == 2 })
		if end := turnEnds(s, t)[1]; !end.Autonomous || !slices.Equal(end.BackgroundTasks, []string{"bg-2"}) {
			t.Fatalf("turn end %+v, want bg-2 pending", end)
		}
		rec.noMoreStates(t, "working:prompt", "working:background")
		if !s.State().Background {
			t.Fatal("background ended while bg-2 runs")
		}
		m.chunk("last")
		m.marker()
		rec.waitStates(t, "working:prompt", "working:background", "idle:end_turn")
		if end := turnEnds(s, t)[2]; len(end.BackgroundTasks) != 0 {
			t.Fatalf("turn end %+v", end)
		}
	})
}

func TestAutonomousTurnWithoutAMarker(t *testing.T) {
	shortenQuiet(t, 100*time.Millisecond)
	plain := func(t *testing.T) (*mockAgent, *Session, *recorder) {
		m := newMockAgent(t, loadFixture(t, "claude"))
		m.onPrompt = func(*mockAgent, promptCall) (any, *acp.RequestError) {
			return map[string]any{"stopReason": "end_turn"}, nil
		}
		s, rec := startMock(t, m, Config{})
		if _, err := s.Prompt(context.Background(), textPrompt("go"), false, nil); err != nil {
			t.Fatal(err)
		}
		rec.waitStates(t, "working:prompt", "idle:end_turn")
		return m, s, rec
	}

	t.Run("goes quiet", func(t *testing.T) {
		m, _, rec := plain(t)
		m.chunk("hello")
		rec.waitStates(t, "working:prompt", "idle:end_turn", "working:background", "idle:end_turn")
	})

	t.Run("an open tool call holds the turn", func(t *testing.T) {
		m, s, rec := plain(t)
		m.update(map[string]any{"sessionUpdate": "tool_call", "toolCallId": "t9", "title": "Bash", "kind": "execute", "status": "in_progress"})
		rec.waitStates(t, "working:prompt", "idle:end_turn", "working:background")
		rec.noMoreStates(t, "working:prompt", "idle:end_turn", "working:background")
		time.Sleep(3 * autonomousQuiet)
		rec.noMoreStates(t, "working:prompt", "idle:end_turn", "working:background")
		if !s.State().TurnInFlight {
			t.Fatal("turn ended with a tool call open")
		}
		m.update(map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "t9", "status": "completed"})
		rec.waitStates(t, "working:prompt", "idle:end_turn", "working:background", "idle:end_turn")
	})

	t.Run("a call left running by an ended turn does not hold it", func(t *testing.T) {
		m := newMockAgent(t, loadFixture(t, "claude"))
		m.onPrompt = func(m *mockAgent, _ promptCall) (any, *acp.RequestError) {
			m.update(map[string]any{"sessionUpdate": "tool_call", "toolCallId": "stale", "title": "Bash", "kind": "execute", "status": "in_progress"})
			return map[string]any{"stopReason": "end_turn"}, nil
		}
		s, rec := startMock(t, m, Config{})
		if _, err := s.Prompt(context.Background(), textPrompt("go"), false, nil); err != nil {
			t.Fatal(err)
		}
		rec.waitStates(t, "working:prompt", "idle:end_turn")
		m.chunk("hello")
		rec.waitStates(t, "working:prompt", "idle:end_turn", "working:background", "idle:end_turn")
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

func TestCancelWithBackgroundWork(t *testing.T) {
	t.Run("only pending tasks", func(t *testing.T) {
		m := backgroundAgent(t, map[string]any{"backgroundTaskId": "bg-1"})
		s, rec := startBackground(t, m)
		if err := s.Cancel(context.Background()); err != nil {
			t.Fatal(err)
		}
		rec.waitStates(t, "working:prompt", "working:background", "idle:cancelled")
		if st := s.State(); st.Background || st.TurnInFlight {
			t.Fatalf("state %+v", st)
		}
	})

	t.Run("during an autonomous turn", func(t *testing.T) {
		m := backgroundAgent(t, map[string]any{"backgroundTaskId": "bg-1"})
		s, rec := startBackground(t, m)
		m.chunk("working")
		waitFor(t, "autonomous turn", func() bool { return s.State().TurnInFlight })
		if err := s.Cancel(context.Background()); err != nil {
			t.Fatal(err)
		}
		rec.waitStates(t, "working:prompt", "working:background", "idle:cancelled")
		end := turnEnds(s, t)[1]
		if !end.Autonomous || end.StopReason != "cancelled" || len(end.BackgroundTasks) != 0 {
			t.Fatalf("turn end %+v", end)
		}
		if st := s.State(); st.Background || st.TurnInFlight {
			t.Fatalf("state %+v", st)
		}
	})

	t.Run("a cancelled prompt that launched a task", func(t *testing.T) {
		m := newMockAgent(t, loadFixture(t, "claude"))
		m.onPrompt = func(m *mockAgent, _ promptCall) (any, *acp.RequestError) {
			m.toolResult("t1", map[string]any{"backgroundTaskId": "bg-1"})
			return map[string]any{"stopReason": "cancelled"}, nil
		}
		s, rec := startMock(t, m, Config{})
		if _, err := s.Prompt(context.Background(), textPrompt("go"), false, nil); err != nil {
			t.Fatal(err)
		}
		rec.waitStates(t, "working:prompt", "idle:cancelled")
		rec.noMoreStates(t, "working:prompt", "idle:cancelled")
		if end := turnEnds(s, t)[0]; len(end.BackgroundTasks) != 0 || s.State().Background {
			t.Fatalf("turn end %+v, state %+v", end, s.State())
		}
	})
}

func TestConnectionEndWithBackgroundTasks(t *testing.T) {
	m := backgroundAgent(t, map[string]any{"backgroundTaskId": "bg-1"})
	s, rec := startBackground(t, m)
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
	_ = log.Close()
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, it := range ofKind(its, KindNotice) {
		if it.Notice.Title == "Background work interrupted" && strings.Contains(it.Notice.Description, "bg-1") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no background notice in %+v", ofKind(its, KindNotice))
	}
}

func TestRestoreReportsAnInterruptedTurn(t *testing.T) {
	for _, tc := range []struct {
		name string
		log  []Item
		want string
	}{
		{"open turn", []Item{{Kind: KindTurnStart, Turn: 1}}, "interrupted"},
		{"ended turn", []Item{{Kind: KindTurnStart, Turn: 1}, {Kind: KindTurnEnd, Turn: 1, StopReason: "end_turn"}}, ""},
		{"ended turn with background tasks", []Item{{Kind: KindTurnStart, Turn: 1}, {Kind: KindTurnEnd, Turn: 1, StopReason: "end_turn", BackgroundTasks: []string{"bg-1"}}}, "interrupted"},
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

func TestSystemPromptIsSentWithTheSession(t *testing.T) {
	failed := &acp.RequestError{Code: -32603, Message: "Internal error"}
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
			t.Run(tc.name+" "+prompt, func(t *testing.T) {
				m := newMockAgent(t, loadFixture(t, "claude"))
				if tc.fail {
					m.resume = func() *acp.RequestError { return failed }
				}
				cfg := tc.cfg
				cfg.SystemPrompt = prompt
				startMock(t, m, cfg)
				m.mu.Lock()
				raw := m.received[tc.method]
				m.mu.Unlock()
				var params map[string]json.RawMessage
				if err := json.Unmarshal(raw, &params); err != nil {
					t.Fatalf("%s params %q: %v", tc.method, raw, err)
				}
				meta, has := params["_meta"]
				if prompt == "" {
					if has {
						t.Fatalf("%s sent _meta %s with no system prompt", tc.method, meta)
					}
					return
				}
				if string(meta) != `{"systemPrompt":{"append":"rules"}}` {
					t.Fatalf("%s _meta %s", tc.method, meta)
				}
			})
		}
	}
}

func TestMockBackgroundPromptRunsEndToEnd(t *testing.T) {
	delay := acpmock.BackgroundDelay
	acpmock.BackgroundDelay = 50 * time.Millisecond
	t.Cleanup(func() { acpmock.BackgroundDelay = delay })
	s, rec := startRealMock(t)
	if _, err := s.Prompt(context.Background(), textPrompt(acpmock.PromptBackground), false, nil); err != nil {
		t.Fatal(err)
	}
	rec.waitStates(t, "working:prompt", "working:background", "idle:end_turn")
	ends := turnEnds(s, t)
	if len(ends) != 2 || !slices.Equal(ends[0].BackgroundTasks, []string{acpmock.BackgroundTaskID}) ||
		!ends[1].Autonomous || ends[1].Origin != "task-notification" || len(ends[1].BackgroundTasks) != 0 {
		t.Fatalf("turn ends %+v", ends)
	}
	if got := messageText(items(t, s), "assistant"); !strings.Contains(got, "background done") {
		t.Fatalf("assistant text %q", got)
	}
}

func startRealMock(t *testing.T) (*Session, *recorder) {
	t.Helper()
	agentIn, hostOut := io.Pipe()
	hostIn, agentOut := io.Pipe()
	agent := acpmock.New(loadFixture(t, "claude"))
	go func() {
		agent.Serve(agentIn, agentOut)
		_ = agentOut.Close()
	}()
	rec := newRecorder()
	s, err := Start(t.Context(), hostIn, hostOut, rec.config(Config{LogPath: filepath.Join(t.TempDir(), "run.items.jsonl"), Cwd: "/workspace", Logger: discard}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(); <-s.Done() })
	return s, rec
}

func TestAnsweringAnAutonomousQuestionRestartsTheQuietWait(t *testing.T) {
	shortenQuiet(t, 300*time.Millisecond)
	m := newMockAgent(t, loadFixture(t, "claude"))
	m.onPrompt = func(*mockAgent, promptCall) (any, *acp.RequestError) {
		return map[string]any{"stopReason": "end_turn"}, nil
	}
	s, rec := startMock(t, m, Config{})
	if _, err := s.Prompt(context.Background(), textPrompt("go"), false, nil); err != nil {
		t.Fatal(err)
	}
	rec.waitStates(t, "working:prompt", "idle:end_turn")
	m.chunk("hello")
	rec.waitStates(t, "working:prompt", "idle:end_turn", "working:background")

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
	rec.waitStates(t, "working:prompt", "idle:end_turn", "working:background", "idle:end_turn")
}

func TestInterrupted(t *testing.T) {
	for _, tc := range []struct {
		name string
		log  []Item
		want bool
	}{
		{"empty", nil, false},
		{"open turn", []Item{{Kind: KindTurnStart, Turn: 1}}, false},
		{"end_turn", []Item{{Kind: KindTurnEnd, StopReason: "end_turn"}}, false},
		{"interrupted", []Item{{Kind: KindTurnEnd, StopReason: "interrupted"}}, true},
		{"background tasks", []Item{{Kind: KindTurnEnd, StopReason: "end_turn", BackgroundTasks: []string{"bg-1"}}}, true},
		{"reset after", []Item{{Kind: KindTurnEnd, StopReason: "end_turn", BackgroundTasks: []string{"bg-1"}}, {Kind: KindReset}}, false},
		{"later turn ended plainly", []Item{
			{Kind: KindTurnEnd, StopReason: "end_turn", BackgroundTasks: []string{"bg-1"}},
			{Kind: KindTurnEnd, StopReason: "end_turn"},
		}, false},
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
