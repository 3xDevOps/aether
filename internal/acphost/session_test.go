package acphost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"
)

func TestReplayRecordedAgents(t *testing.T) {
	for _, name := range []string{"claude", "codex"} {
		t.Run(name, func(t *testing.T) {
			m := newMockAgent(t, loadFixture(t, name))
			s, rec := startMock(t, m, Config{})
			if !s.Info().Steering || !s.Info().Resume || !s.Info().LoadSession {
				t.Fatalf("capabilities not read from initialize: %+v", s.Info())
			}
			if s.SessionID() != m.sessionID() {
				t.Fatalf("session id %q, want %q", s.SessionID(), m.sessionID())
			}
			r, err := s.Prompt(context.Background(), textPrompt("Reply with exactly the word pong"), false, nil)
			if err != nil || r.Outcome != OutcomeSent {
				t.Fatalf("Prompt = %+v, %v", r, err)
			}
			if reason := rec.waitIdle(t); reason != "end_turn" {
				t.Fatalf("turn ended with %q", reason)
			}
			its := items(t, s)
			if got := messageText(its, "assistant"); got != "pong" {
				t.Fatalf("assistant text %q", got)
			}
			if got := messageText(its, "user"); got != "Reply with exactly the word pong" {
				t.Fatalf("user text %q", got)
			}
			for _, k := range []Kind{KindModeChange, KindConfigOptions, KindTurnStart, KindTurnEnd, KindUsage} {
				if len(ofKind(its, k)) == 0 {
					t.Errorf("no %s item", k)
				}
			}
			if len(ofKind(its, KindUnknown)) != 0 {
				t.Errorf("recorded updates projected as unknown: %+v", ofKind(its, KindUnknown))
			}
			for i, it := range its {
				if it.Seq != int64(i+1) || it.Turn > 1 {
					t.Fatalf("item %d has seq %d turn %d", i, it.Seq, it.Turn)
				}
			}
			st := s.State()
			if st.Mode == "" || len(st.ConfigOptions) == 0 || st.TurnInFlight || !st.Steering || len(st.AuthMethods) == 0 {
				t.Fatalf("state %+v", st)
			}
		})
	}
}

func TestChunksCoalesce(t *testing.T) {
	m := newMockAgent(t, loadFixture(t, "codex"))
	const tokens = 400
	m.onPrompt = func(m *mockAgent, _ promptCall) (any, *acp.RequestError) {
		for range tokens {
			m.update(map[string]any{"sessionUpdate": "agent_message_chunk", "messageId": "m1", "content": map[string]any{"type": "text", "text": "tok "}})
		}
		m.update(map[string]any{"sessionUpdate": "agent_thought_chunk", "messageId": "t1", "content": map[string]any{"type": "text", "text": "hmm"}})
		m.update(map[string]any{"sessionUpdate": "agent_message_chunk", "messageId": "m2", "content": map[string]any{"type": "text", "text": "done"}})
		return map[string]any{"stopReason": "end_turn"}, nil
	}
	s, rec := startMock(t, m, Config{})
	if _, err := s.Prompt(context.Background(), textPrompt("count"), false, nil); err != nil {
		t.Fatal(err)
	}
	rec.waitIdle(t)
	its := items(t, s)
	var m1, m2 []Item
	for _, it := range ofKind(its, KindMessage) {
		switch it.Message.MessageID {
		case "m1":
			m1 = append(m1, it)
		case "m2":
			m2 = append(m2, it)
		}
	}
	if len(m1) == 0 || len(m1) >= tokens/4 {
		t.Fatalf("%d chunks became %d items", tokens, len(m1))
	}
	var text string
	for _, it := range m1 {
		text += it.Message.Text
		if len(it.Message.Text) > textFlushBytes+4 {
			t.Fatalf("segment of %d bytes exceeds the flush size", len(it.Message.Text))
		}
	}
	if text != strings.Repeat("tok ", tokens) {
		t.Fatalf("joined text lost chunks: %d bytes", len(text))
	}
	if !m1[len(m1)-1].Message.Complete || !m2[len(m2)-1].Message.Complete {
		t.Fatal("messages not completed")
	}
	if th := ofKind(its, KindThought); len(th) == 0 || th[len(th)-1].Message.Text+th[0].Message.Text == "" {
		t.Fatalf("thought not recorded: %+v", th)
	}
	if m1[len(m1)-1].Seq > ofKind(its, KindThought)[0].Seq {
		t.Fatal("the thought did not close the message before it")
	}
}

func TestToolCallMerge(t *testing.T) {
	m := newMockAgent(t, loadFixture(t, "claude"))
	const deltas = 40
	m.onPrompt = func(m *mockAgent, _ promptCall) (any, *acp.RequestError) {
		m.update(map[string]any{"sessionUpdate": "agent_message_chunk", "messageId": "m1", "content": map[string]any{"type": "text", "text": "Running the tests."}})
		m.update(map[string]any{"sessionUpdate": "tool_call", "toolCallId": "t1", "title": "go test", "kind": "execute", "status": "pending",
			"rawInput": map[string]any{"command": "go test ./..."}, "locations": []any{map[string]any{"path": "/workspace/main.go"}}})
		m.update(map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "t1", "status": "in_progress"})
		for i := range deltas {
			m.update(map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "t1",
				"_meta": map[string]any{"terminal_output": map[string]any{"terminal_id": "t1", "data": strings.Repeat(string(rune('a'+i%26)), 10)}}})
		}
		old := "package main\n\nfunc main() {}\n"
		m.update(map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "t1", "status": "completed",
			"content": []any{map[string]any{"type": "diff", "path": "/workspace/main.go", "oldText": old, "newText": "package main\n\nfunc main() { run() }\n"},
				map[string]any{"type": "diff", "path": "/workspace/new.go", "newText": "package main\n"}},
			"_meta": map[string]any{"terminal_exit": map[string]any{"terminal_id": "t1", "exit_code": 0}}})
		return map[string]any{"stopReason": "end_turn"}, nil
	}
	s, rec := startMock(t, m, Config{})
	if _, err := s.Prompt(context.Background(), textPrompt("test it"), false, nil); err != nil {
		t.Fatal(err)
	}
	rec.waitIdle(t)
	its := items(t, s)
	tools := ofKind(its, KindToolCall)
	if len(tools) < 3 || len(tools) > 2+deltas/5 {
		t.Fatalf("%d updates became %d tool items", deltas+3, len(tools))
	}
	var output string
	for _, it := range tools {
		output += it.ToolCall.Output
	}
	if len(output) != deltas*10 {
		t.Fatalf("output %d bytes, want %d", len(output), deltas*10)
	}
	last := tools[len(tools)-1].ToolCall
	if last.Title != "go test" || last.ToolKind != "execute" || last.Status != "completed" || len(last.Locations) != 1 ||
		!strings.Contains(string(last.RawInput), "go test ./...") || last.OutputBytes != deltas*10 || last.ExitCode == nil || *last.ExitCode != 0 {
		t.Fatalf("merged state lost fields: %+v", last)
	}
	if len(last.Diffs) != 2 ||
		!strings.Contains(last.Diffs[0].Patch, "-func main() {}\n+func main() { run() }\n") ||
		!strings.HasPrefix(last.Diffs[1].Patch, "--- /dev/null\n+++ b/workspace/new.go\n") {
		t.Fatalf("diffs: %+v", last.Diffs)
	}
	msgs := ofKind(its, KindMessage)
	closed := msgs[len(msgs)-1]
	if closed.Message.MessageID != "m1" || !closed.Message.Complete || closed.Seq > tools[0].Seq {
		t.Fatalf("the tool call did not close the open message: %+v", closed)
	}
	rec.mu.Lock()
	activity := rec.activity
	rec.mu.Unlock()
	if len(activity) == 0 || activity[0] != [2]string{"execute", "/workspace/main.go"} || len(activity) > 2 {
		t.Fatalf("activity not throttled to one a second: %v", activity)
	}
}

func TestPlanIsReplacedWhole(t *testing.T) {
	m := newMockAgent(t, loadFixture(t, "claude"))
	m.onPrompt = func(m *mockAgent, _ promptCall) (any, *acp.RequestError) {
		two := []any{map[string]any{"content": "a", "priority": "high", "status": "completed"}, map[string]any{"content": "b", "priority": "low", "status": "pending"}}
		m.update(map[string]any{"sessionUpdate": "plan", "entries": two})
		m.update(map[string]any{"sessionUpdate": "plan", "entries": two})
		m.update(map[string]any{"sessionUpdate": "plan", "entries": []any{map[string]any{"content": "c", "priority": "medium", "status": "in_progress"}}})
		return map[string]any{"stopReason": "end_turn"}, nil
	}
	s, rec := startMock(t, m, Config{})
	if _, err := s.Prompt(context.Background(), textPrompt("plan"), false, nil); err != nil {
		t.Fatal(err)
	}
	rec.waitIdle(t)
	plans := ofKind(items(t, s), KindPlan)
	if len(plans) != 2 || len(plans[0].Plan) != 2 || len(plans[1].Plan) != 1 || plans[1].Plan[0] != (PlanEntry{Content: "c", Priority: "medium", Status: "in_progress"}) {
		t.Fatalf("plans: %+v", plans)
	}
}

func (m *mockAgent) permissionRequest(toolCallID string) map[string]any {
	return map[string]any{
		"sessionId": m.sessionID(),
		"toolCall":  map[string]any{"toolCallId": toolCallID, "title": "rm -rf build", "kind": "execute", "status": "pending"},
		"options": []any{
			map[string]any{"optionId": "allow", "name": "Allow", "kind": "allow_once"},
			map[string]any{"optionId": "reject", "name": "Reject", "kind": "reject_once"},
		},
	}
}

func TestPermissionAnswer(t *testing.T) {
	m := newMockAgent(t, loadFixture(t, "claude"))
	got := make(chan string, 1)
	m.onPrompt = func(m *mockAgent, call promptCall) (any, *acp.RequestError) {
		res, err := m.request(call.ctx, acp.ClientMethodSessionRequestPermission, m.permissionRequest("t1"))
		if err != nil {
			got <- err.Error()
		} else {
			got <- string(res)
		}
		return map[string]any{"stopReason": "end_turn"}, nil
	}
	s, rec := startMock(t, m, Config{})
	if _, err := s.Prompt(context.Background(), textPrompt("clean"), false, nil); err != nil {
		t.Fatal(err)
	}
	pending := rec.waitInputs(t, 1)
	if pending[0].Kind != "permission" || pending[0].SessionID != s.SessionID() {
		t.Fatalf("input %+v", pending[0])
	}
	id := pending[0].ID
	if st := s.State(); len(st.Pending) != 1 || st.Pending[0].ToolCallID != "t1" || st.Pending[0].Options[0].Kind != "allow_once" {
		t.Fatalf("state pending %+v", st.Pending)
	}
	if err := s.Answer(id, "maybe", nil); !errors.Is(err, ErrUnknownOption) {
		t.Fatalf("unknown option: %v", err)
	}
	if err := s.Answer("nope", "allow", nil); !errors.Is(err, ErrUnknownRequest) {
		t.Fatalf("unknown request: %v", err)
	}
	if err := s.Answer(id, "reject", nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Answer(id, "allow", nil); !errors.Is(err, ErrAlreadyAnswered) {
		t.Fatalf("second answer: %v", err)
	}
	if res := <-got; res != `{"outcome":{"optionId":"reject","outcome":"selected"}}` {
		t.Fatalf("agent got %s", res)
	}
	rec.waitInputs(t, 0)
	rec.waitIdle(t)
	reqs := ofKind(items(t, s), KindRequest)
	if len(reqs) != 2 || reqs[0].Request.Status != RequestPending || reqs[1].Request.Status != RequestAnswered || reqs[1].Request.Answer != "reject" {
		t.Fatalf("request items %+v", reqs)
	}
	if tools := ofKind(items(t, s), KindToolCall); len(tools) == 0 || tools[0].ToolCall.Title != "rm -rf build" {
		t.Fatalf("the permission's tool call was not recorded: %+v", tools)
	}
}

func TestCancelAnswersPendingRequests(t *testing.T) {
	m := newMockAgent(t, loadFixture(t, "claude"))
	got := make(chan string, 2)
	m.onPrompt = func(m *mockAgent, call promptCall) (any, *acp.RequestError) {
		res, _ := m.request(call.ctx, acp.ClientMethodSessionRequestPermission, m.permissionRequest("t1"))
		got <- string(res)
		<-m.cancelled
		return map[string]any{"stopReason": "cancelled"}, nil
	}
	s, rec := startMock(t, m, Config{})
	if _, err := s.Prompt(context.Background(), textPrompt("clean"), false, nil); err != nil {
		t.Fatal(err)
	}
	pending := rec.waitInputs(t, 1)
	if err := s.Cancel(context.Background()); err != nil {
		t.Fatal(err)
	}
	if res := <-got; res != `{"outcome":{"outcome":"cancelled"}}` {
		t.Fatalf("agent got %s", res)
	}
	if reason := rec.waitIdle(t); reason != "cancelled" {
		t.Fatalf("turn ended with %q", reason)
	}
	if err := s.Answer(pending[0].ID, "allow", nil); !errors.Is(err, ErrAlreadyAnswered) {
		t.Fatalf("answer after cancel: %v", err)
	}
	reqs := ofKind(items(t, s), KindRequest)
	if len(reqs) != 2 || reqs[1].Request.Status != RequestCancelled {
		t.Fatalf("request items %+v", reqs)
	}
}

func TestElicitations(t *testing.T) {
	m := newMockAgent(t, loadFixture(t, "claude"))
	got := make(chan string, 2)
	m.onPrompt = func(m *mockAgent, call promptCall) (any, *acp.RequestError) {
		_, err := m.request(call.ctx, acp.ClientMethodElicitationCreate, map[string]any{
			"sessionId": "another", "mode": "form", "message": "Which branch?", "requestedSchema": map[string]any{"type": "object"},
		})
		var re *acp.RequestError
		if !errors.As(err, &re) || re.Code != -32602 {
			t.Errorf("elicitation for another session: %v", err)
		}
		res, _ := m.request(call.ctx, acp.ClientMethodElicitationCreate, map[string]any{
			"sessionId": m.sessionID(), "mode": "form", "message": "Which branch?",
			"requestedSchema": map[string]any{"type": "object", "properties": map[string]any{"branch": map[string]any{"type": "string"}}},
		})
		got <- string(res)
		res, _ = m.request(call.ctx, acp.ClientMethodElicitationCreate, map[string]any{
			"requestId": 1, "mode": "url", "message": "Sign in", "url": "https://example.com/login", "elicitationId": "e1",
		})
		got <- string(res)
		_, err = m.request(call.ctx, acp.ClientMethodFsReadTextFile, map[string]any{"sessionId": "s", "path": "/etc/hostname"})
		if !errors.As(err, &re) || re.Code != -32601 {
			t.Errorf("fs/read_text_file: %v", err)
		}
		return map[string]any{"stopReason": "end_turn"}, nil
	}
	s, rec := startMock(t, m, Config{})
	if _, err := s.Prompt(context.Background(), textPrompt("ship it"), false, nil); err != nil {
		t.Fatal(err)
	}
	q := rec.waitInputs(t, 1)[0]
	if q.Kind != "question" || s.State().Pending[0].Kind != RequestQuestion || len(s.State().Pending[0].Schema) == 0 {
		t.Fatalf("question %+v", s.State().Pending)
	}
	if err := s.Answer(q.ID, "accept", map[string]any{"branch": "main"}); err != nil {
		t.Fatal(err)
	}
	if res := <-got; res != `{"action":"accept","content":{"branch":"main"}}` {
		t.Fatalf("agent got %s", res)
	}
	link := rec.waitInputs(t, 1)[0]
	if p := s.State().Pending[0]; p.Kind != RequestLink || p.URL != "https://example.com/login" {
		t.Fatalf("link %+v", p)
	}
	if err := s.Answer(link.ID, "accept", map[string]any{"x": 1}); err == nil {
		t.Fatal("a link answer accepted content")
	}
	if err := s.Answer(link.ID, "decline", nil); err != nil {
		t.Fatal(err)
	}
	if res := <-got; res != `{"action":"decline"}` {
		t.Fatalf("agent got %s", res)
	}
	rec.waitIdle(t)
}

func TestRestore(t *testing.T) {
	fix := loadFixture(t, "codex")
	failed := &acp.RequestError{Code: -32603, Message: "Internal error"}

	t.Run("resume replays nothing", func(t *testing.T) {
		m := newMockAgent(t, fix)
		s, rec := startMock(t, m, Config{SessionID: m.sessionID()})
		if !m.called(acp.AgentMethodSessionResume) || m.called(acp.AgentMethodSessionLoad) || m.called(acp.AgentMethodSessionNew) {
			t.Fatalf("methods %v", m.methods)
		}
		if inputs := <-rec.inputsCh; len(inputs) != 0 {
			t.Fatalf("restored inputs %+v", inputs)
		}
		if reason := rec.waitIdle(t); reason != "" {
			t.Fatalf("restored idle reason %q", reason)
		}
		if s.SessionID() != m.sessionID() || len(ofKind(items(t, s), KindMessage)) != 0 {
			t.Fatalf("resume recorded history: %+v", items(t, s))
		}
	})

	t.Run("load drops the replayed history", func(t *testing.T) {
		m := newMockAgent(t, fix)
		m.resume = func() *acp.RequestError { return failed }
		s, rec := startMock(t, m, Config{SessionID: m.sessionID()})
		if !m.called(acp.AgentMethodSessionLoad) || m.called(acp.AgentMethodSessionNew) {
			t.Fatalf("methods %v", m.methods)
		}
		rec.waitIdle(t)
		its := items(t, s)
		if len(ofKind(its, KindMessage)) != 0 || len(ofKind(its, KindCommands)) != 1 || len(ofKind(its, KindReset)) != 0 {
			t.Fatalf("load items %+v", its)
		}
		if _, err := s.Prompt(context.Background(), textPrompt("again"), false, nil); err != nil {
			t.Fatal(err)
		}
		rec.waitIdle(t)
		if got := messageText(items(t, s), "assistant"); got != "pong" {
			t.Fatalf("assistant text after load %q", got)
		}
	})

	t.Run("a lost session starts a new epoch", func(t *testing.T) {
		m := newMockAgent(t, fix)
		m.resume = func() *acp.RequestError { return failed }
		m.load = func() *acp.RequestError { return failed }
		s, _ := startMock(t, m, Config{SessionID: "gone"})
		its := items(t, s)
		resets := ofKind(its, KindReset)
		if !m.called(acp.AgentMethodSessionNew) || len(resets) != 1 || resets[0].Epoch != 1 || its[len(its)-1].Epoch != 1 {
			t.Fatalf("items %+v", its)
		}
		notices := ofKind(its, KindNotice)
		if len(notices) != 1 || !strings.Contains(notices[0].Notice.Description, "Internal error") {
			t.Fatalf("notice %+v", notices)
		}
	})
}

func TestUnknownUpdateKindsAreKeptRaw(t *testing.T) {
	m := newMockAgent(t, loadFixture(t, "claude"))
	m.onPrompt = func(m *mockAgent, _ promptCall) (any, *acp.RequestError) {
		m.update(map[string]any{"sessionUpdate": "compaction_update", "phase": "started"})
		m.update(map[string]any{"sessionUpdate": "tool_call", "toolCallId": 7})
		return map[string]any{"stopReason": "end_turn"}, nil
	}
	s, rec := startMock(t, m, Config{})
	if _, err := s.Prompt(context.Background(), textPrompt("go"), false, nil); err != nil {
		t.Fatal(err)
	}
	rec.waitIdle(t)
	unknown := ofKind(items(t, s), KindUnknown)
	if len(unknown) != 2 || !strings.Contains(string(unknown[0].Raw), `"compaction_update"`) {
		t.Fatalf("unknown items %+v", unknown)
	}
}

// Codex answers a second session/prompt sent during a turn by folding it
// into the running turn and never answering the first request.
func TestPromptsAreSerialized(t *testing.T) {
	m := newMockAgent(t, loadFixture(t, "codex"))
	release := make(chan struct{})
	var prompts int
	m.onPrompt = func(m *mockAgent, call promptCall) (any, *acp.RequestError) {
		m.mu.Lock()
		prompts++
		first, swallowed := prompts == 1, m.inflight > 1
		m.mu.Unlock()
		if swallowed {
			<-call.ctx.Done()
			return nil, acp.NewRequestCancelled(nil)
		}
		if first {
			<-release
		}
		m.update(map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "ok"}})
		return map[string]any{"stopReason": "end_turn"}, nil
	}
	steered := make(chan struct{}, 1)
	m.onSteer = func(m *mockAgent, _ json.RawMessage) (any, *acp.RequestError) {
		steered <- struct{}{}
		return map[string]any{"outcome": "injected"}, nil
	}
	s, rec := startMock(t, m, Config{})
	if r, err := s.Prompt(context.Background(), textPrompt("first"), false, nil); err != nil || r.Outcome != OutcomeSent {
		t.Fatalf("first: %+v %v", r, err)
	}
	if r, err := s.Prompt(context.Background(), textPrompt("second"), false, nil); err != nil || r.Outcome != OutcomeQueued {
		t.Fatalf("second: %+v %v", r, err)
	}
	if r, err := s.Prompt(context.Background(), textPrompt("steer"), true, nil); err != nil || r.Outcome != OutcomeInjected {
		t.Fatalf("steer: %+v %v", r, err)
	}
	<-steered
	if st := s.State(); !st.TurnInFlight || st.Queued != 1 {
		t.Fatalf("state %+v", st)
	}
	close(release)
	rec.waitIdle(t)
	m.mu.Lock()
	maxInflight, n := m.maxInflight, prompts
	m.mu.Unlock()
	if maxInflight != 1 || n != 2 {
		t.Fatalf("%d prompts with up to %d in flight", n, maxInflight)
	}
	its := items(t, s)
	if len(ofKind(its, KindTurnStart)) != 2 || len(ofKind(its, KindTurnEnd)) != 2 || ofKind(its, KindTurnEnd)[1].Turn != 2 {
		t.Fatalf("turns %+v", its)
	}
	if got := messageText(its, "user"); got != "firststeersecond" {
		t.Fatalf("user messages in order %q", got)
	}
	rec.mu.Lock()
	states := rec.states
	rec.mu.Unlock()
	if strings.Join(states, ",") != "working:prompt,working:prompt,idle:end_turn" {
		t.Fatalf("states %v", states)
	}
}

func TestUnknownSteerOutcomeIsAnError(t *testing.T) {
	m := newMockAgent(t, loadFixture(t, "codex"))
	release := make(chan struct{})
	m.onPrompt = func(*mockAgent, promptCall) (any, *acp.RequestError) {
		<-release
		return map[string]any{"stopReason": "end_turn"}, nil
	}
	m.onSteer = func(*mockAgent, json.RawMessage) (any, *acp.RequestError) {
		return map[string]any{"outcome": "somethingNew"}, nil
	}
	s, rec := startMock(t, m, Config{})
	if _, err := s.Prompt(context.Background(), textPrompt("first"), false, nil); err != nil {
		t.Fatal(err)
	}
	if r, err := s.Prompt(context.Background(), textPrompt("steer"), true, nil); err == nil || !strings.Contains(err.Error(), "somethingNew") {
		t.Fatalf("got %+v %v, want an error naming the outcome", r, err)
	}
	close(release)
	rec.waitIdle(t)
	if got := messageText(items(t, s), "user"); got != "first" {
		t.Fatalf("user messages %q", got)
	}
}

// A steer that races the end of the host's turn reaches an idle agent. One
// that then starts a turn of its own has the input, so the host must not
// send it again, and must stop that turn: it cannot see it end, and its next
// session/prompt would reach a busy agent.
func TestSteerStartingAnAgentTurnIsCancelled(t *testing.T) {
	m := newMockAgent(t, loadFixture(t, "codex"))
	release := make(chan struct{})
	var prompts int
	m.onPrompt = func(m *mockAgent, _ promptCall) (any, *acp.RequestError) {
		m.mu.Lock()
		prompts++
		first := prompts == 1
		m.mu.Unlock()
		if first {
			<-release
		}
		return map[string]any{"stopReason": "end_turn"}, nil
	}
	turnEnded := make(chan struct{})
	m.onSteer = func(*mockAgent, json.RawMessage) (any, *acp.RequestError) {
		close(release)
		<-turnEnded
		return map[string]any{"outcome": "startedNewTurn"}, nil
	}
	s, rec := startMock(t, m, Config{})
	if _, err := s.Prompt(context.Background(), textPrompt("first"), false, nil); err != nil {
		t.Fatal(err)
	}
	go func() {
		rec.waitIdle(t)
		close(turnEnded)
	}()
	if r, err := s.Prompt(context.Background(), textPrompt("steer"), true, nil); err == nil || !strings.Contains(err.Error(), "startedNewTurn") {
		t.Fatalf("steer: %+v %v, want an error naming startedNewTurn", r, err)
	}
	select {
	case <-m.cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("the agent's own turn was not cancelled")
	}
	notices := ofKind(items(t, s), KindNotice)
	if len(notices) != 1 || notices[0].Notice.Title != "Steered message stopped" {
		t.Fatalf("notices %+v", notices)
	}
	if r, err := s.Prompt(context.Background(), textPrompt("next"), false, nil); err != nil || r.Outcome != OutcomeSent {
		t.Fatalf("next: %+v %v", r, err)
	}
	rec.waitIdle(t)
	m.mu.Lock()
	n := prompts
	m.mu.Unlock()
	if n != 2 {
		t.Fatalf("%d prompts sent, want 2", n)
	}
	if got := messageText(items(t, s), "user"); got != "firststeernext" {
		t.Fatalf("user messages %q", got)
	}
}

func TestConnectionLossMidTurn(t *testing.T) {
	m := newMockAgent(t, loadFixture(t, "claude"))
	m.onPrompt = func(m *mockAgent, call promptCall) (any, *acp.RequestError) {
		_, _ = m.request(call.ctx, acp.ClientMethodSessionRequestPermission, m.permissionRequest("t1"))
		return nil, nil
	}
	s, rec := startMock(t, m, Config{})
	if _, err := s.Prompt(context.Background(), textPrompt("work"), false, nil); err != nil {
		t.Fatal(err)
	}
	rec.waitInputs(t, 1)
	_ = m.stdout.Close()
	select {
	case <-s.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("session did not end")
	}
	if reason := rec.waitIdle(t); reason != "interrupted" {
		t.Fatalf("idle reason %q", reason)
	}
	rec.mu.Lock()
	lastInputs := rec.inputs[len(rec.inputs)-1]
	rec.mu.Unlock()
	if len(lastInputs) != 0 {
		t.Fatalf("inputs not cleared: %v", lastInputs)
	}
	if _, err := s.Prompt(context.Background(), textPrompt("more"), false, nil); !errors.Is(err, ErrClosed) {
		t.Fatalf("prompt after close: %v", err)
	}
	log, err := OpenLog(s.cfg.LogPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close() }()
	its, err := log.ReadAfter(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	notices, ends, reqs := ofKind(its, KindNotice), ofKind(its, KindTurnEnd), ofKind(its, KindRequest)
	if len(notices) != 1 || notices[0].Notice.Title != "Turn interrupted" || len(ends) != 1 || ends[0].StopReason != "interrupted" ||
		reqs[len(reqs)-1].Request.Status != RequestCancelled {
		t.Fatalf("items %+v", its)
	}
}

func TestQueuedPromptFailsWhenTheConnectionCloses(t *testing.T) {
	m := newMockAgent(t, loadFixture(t, "codex"))
	started := make(chan struct{})
	m.onPrompt = func(m *mockAgent, call promptCall) (any, *acp.RequestError) {
		m.update(map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "working"}})
		close(started)
		<-call.ctx.Done()
		return nil, acp.NewRequestCancelled(nil)
	}
	s, _ := startMock(t, m, Config{})
	if _, err := s.Prompt(context.Background(), textPrompt("first"), false, nil); err != nil {
		t.Fatal(err)
	}
	<-started
	if r, err := s.Prompt(context.Background(), textPrompt("second"), false, nil); err != nil || r.Outcome != OutcomeQueued {
		t.Fatalf("second: %+v %v", r, err)
	}
	_ = m.stdout.Close()
	select {
	case <-s.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("session did not end")
	}
	if _, err := s.Prompt(context.Background(), textPrompt("third"), false, nil); !errors.Is(err, ErrClosed) || !strings.Contains(err.Error(), "agent connection closed") {
		t.Fatalf("prompt after close: %v", err)
	}
	log, err := OpenLog(s.cfg.LogPath)
	if err != nil {
		t.Fatal(err)
	}
	its, err := log.ReadAfter(0, 0)
	_ = log.Close()
	if err != nil {
		t.Fatal(err)
	}
	var lost []*Notice
	for _, n := range ofKind(its, KindNotice) {
		if n.Notice.Title == "Message not delivered: agent connection closed" {
			lost = append(lost, n.Notice)
		}
	}
	if len(lost) != 1 || lost[0].Description != "second" || messageText(its, "user") != "first" {
		t.Fatalf("items %+v", its)
	}

	resumed := newMockAgent(t, loadFixture(t, "codex"))
	_, rec := startMock(t, resumed, Config{LogPath: s.cfg.LogPath, SessionID: s.SessionID()})
	rec.waitIdle(t)
	if resumed.called(acp.AgentMethodSessionPrompt) {
		t.Fatalf("resume sent a prompt: %v", resumed.methods)
	}
}

// The SDK still hands queued notifications over after the agent's output
// ends; every update the agent sent before exiting must be recorded, ahead of
// the interruption.
func TestUpdatesBeforeExitAreKept(t *testing.T) {
	const n = 1000
	m := newMockAgent(t, loadFixture(t, "claude"))
	m.onPrompt = func(m *mockAgent, call promptCall) (any, *acp.RequestError) {
		for i := range n {
			m.update(map[string]any{"sessionUpdate": "plan", "entries": []any{map[string]any{"content": fmt.Sprint(i), "priority": "high", "status": "pending"}}})
		}
		_ = m.stdout.Close()
		<-call.ctx.Done()
		return nil, nil
	}
	s, _ := startMock(t, m, Config{})
	if _, err := s.Prompt(context.Background(), textPrompt("go"), false, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case <-s.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("session did not end")
	}
	log, err := OpenLog(s.cfg.LogPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close() }()
	its, err := log.ReadAfter(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(ofKind(its, KindPlan)); got != n {
		t.Fatalf("recorded %d of %d plan updates sent before the agent exited", got, n)
	}
	if last := its[len(its)-1]; last.Kind != KindTurnEnd || last.StopReason != "interrupted" {
		t.Fatalf("last item %+v, want the interrupted turn end", last)
	}
}

// An agent may answer the running prompt when its input closes; that turn
// was interrupted by the host, not cancelled or failed by the agent.
func TestTurnAnsweredAfterCloseIsInterrupted(t *testing.T) {
	for _, tc := range []struct {
		name   string
		answer func() (any, *acp.RequestError)
	}{
		{"cancelled", func() (any, *acp.RequestError) { return map[string]any{"stopReason": "cancelled"}, nil }},
		{"error", func() (any, *acp.RequestError) { return nil, acp.NewRequestCancelled(nil) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newMockAgent(t, loadFixture(t, "claude"))
			m.onPrompt = func(m *mockAgent, call promptCall) (any, *acp.RequestError) {
				m.update(map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "working"}})
				<-call.ctx.Done()
				return tc.answer()
			}
			s, rec := startMock(t, m, Config{})
			if _, err := s.Prompt(context.Background(), textPrompt("work"), false, nil); err != nil {
				t.Fatal(err)
			}
			_ = s.Close()
			if reason := rec.waitIdle(t); reason != "interrupted" {
				t.Fatalf("idle reason %q, want interrupted", reason)
			}
			_ = m.stdout.Close()
			<-s.Done()
			log, err := OpenLog(s.cfg.LogPath)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = log.Close() }()
			its, err := log.ReadAfter(0, 0)
			if err != nil {
				t.Fatal(err)
			}
			if ends := ofKind(its, KindTurnEnd); len(ends) != 1 || ends[0].StopReason != "interrupted" {
				t.Fatalf("items %+v", its)
			}
		})
	}
}

// The running turn may end as the host closes the agent's input, before the
// connection itself ends; the queued prompt must not start a turn then.
func TestQueuedPromptIsNotSentAfterClose(t *testing.T) {
	m := newMockAgent(t, loadFixture(t, "claude"))
	m.onPrompt = func(m *mockAgent, call promptCall) (any, *acp.RequestError) {
		m.update(map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "working"}})
		<-call.ctx.Done()
		return map[string]any{"stopReason": "end_turn"}, nil
	}
	s, rec := startMock(t, m, Config{})
	if _, err := s.Prompt(context.Background(), textPrompt("first"), false, nil); err != nil {
		t.Fatal(err)
	}
	delivered := make(chan error, 1)
	if r, err := s.Prompt(context.Background(), textPrompt("second"), false, func(err error) { delivered <- err }); err != nil || r.Outcome != OutcomeQueued {
		t.Fatalf("second: %+v %v", r, err)
	}
	_ = s.Close()
	rec.waitIdle(t)
	_ = m.stdout.Close()
	<-s.Done()
	if err := <-delivered; !errors.Is(err, ErrClosed) {
		t.Fatalf("queued prompt delivered with %v, want ErrClosed", err)
	}
	if _, err := s.Prompt(context.Background(), textPrompt("third"), false, nil); !errors.Is(err, ErrClosed) {
		t.Fatalf("prompt after close: %v", err)
	}
	log, err := OpenLog(s.cfg.LogPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close() }()
	its, err := log.ReadAfter(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ofKind(its, KindTurnStart)) != 1 || messageText(its, "user") != "first" {
		t.Fatalf("items %+v", its)
	}
}

func TestRestartClosesAnOpenTurn(t *testing.T) {
	path := t.TempDir() + "/run.items.jsonl"
	log, err := OpenLog(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range []Item{{Kind: KindTurnStart, Turn: 3}, {Kind: KindMessage, Turn: 3, Message: &Message{Role: "assistant", MessageID: "m", Text: "half"}}} {
		if err := log.Append(&it); err != nil {
			t.Fatal(err)
		}
	}
	_ = log.Close()
	m := newMockAgent(t, loadFixture(t, "claude"))
	s, _ := startMock(t, m, Config{LogPath: path, SessionID: m.sessionID()})
	its := items(t, s)
	if its[2].Kind != KindNotice || its[3].Kind != KindTurnEnd || its[3].Turn != 3 || its[3].StopReason != "interrupted" {
		t.Fatalf("items %+v", its)
	}
}

func TestOptionsModesAndAuth(t *testing.T) {
	m := newMockAgent(t, loadFixture(t, "claude"))
	m.onPrompt = func(m *mockAgent, _ promptCall) (any, *acp.RequestError) {
		_ = m.conn.SendNotification(context.Background(), "_auth/status_update", map[string]any{"authStatus": map[string]any{"kind": "api_key", "label": "Anthropic API key"}})
		_ = m.conn.SendNotification(context.Background(), "_vendor/whatever", map[string]any{})
		m.update(map[string]any{"sessionUpdate": "current_mode_update", "currentModeId": "plan"})
		return map[string]any{"stopReason": "end_turn"}, nil
	}
	s, rec := startMock(t, m, Config{})
	if err := s.SetOption(context.Background(), "mode", "plan"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetOption(context.Background(), "fast", 3); err == nil {
		t.Fatal("a non-string, non-bool value was sent")
	}
	if err := s.SetMode(context.Background(), "default"); err != nil {
		t.Fatal(err)
	}
	sessions, _, err := s.ListSessions(context.Background(), "", "")
	if err != nil || len(sessions) != 1 || sessions[0].Title != "Earlier" {
		t.Fatalf("list: %+v %v", sessions, err)
	}
	if _, err := s.Prompt(context.Background(), textPrompt("hi"), false, nil); err != nil {
		t.Fatal(err)
	}
	rec.waitIdle(t)
	st := s.State()
	if st.Mode != "plan" || !strings.Contains(string(st.ConfigOptions), `"currentValue":"plan"`) || !strings.Contains(string(st.Auth), "Anthropic API key") {
		t.Fatalf("state %+v", st)
	}
}

// A prompt the agent refuses at once is not sent, and the refusal shows.
func TestPromptErrorIsShown(t *testing.T) {
	m := newMockAgent(t, loadFixture(t, "claude"))
	m.onPrompt = func(*mockAgent, promptCall) (any, *acp.RequestError) {
		return nil, acp.NewAuthRequired(nil)
	}
	s, rec := startMock(t, m, Config{})
	receipt, err := s.Prompt(context.Background(), textPrompt("hi"), false, nil)
	if err == nil || !strings.Contains(err.Error(), "Authentication required") || receipt.Outcome != "" {
		t.Fatalf("Prompt = %+v, %v; want the refusal", receipt, err)
	}
	if reason := rec.waitIdle(t); reason != "error" {
		t.Fatalf("reason %q", reason)
	}
	notices := ofKind(items(t, s), KindNotice)
	if len(notices) != 1 || !strings.Contains(notices[0].Notice.Description, "Authentication required") {
		t.Fatalf("notices %+v", notices)
	}
}

// A prompt counts as sent once the agent streams its first update, without
// waiting for the turn to end.
func TestPromptIsSentOnceTheAgentStreams(t *testing.T) {
	m := newMockAgent(t, loadFixture(t, "claude"))
	release := make(chan struct{})
	m.onPrompt = func(m *mockAgent, _ promptCall) (any, *acp.RequestError) {
		m.update(map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "on it"}})
		<-release
		return map[string]any{"stopReason": "end_turn"}, nil
	}
	s, rec := startMock(t, m, Config{})
	start := time.Now()
	receipt, err := s.Prompt(context.Background(), textPrompt("hi"), false, nil)
	if err != nil || receipt.Outcome != OutcomeSent {
		t.Fatalf("Prompt = %+v, %v", receipt, err)
	}
	if waited := time.Since(start); waited >= promptAcceptGrace {
		t.Fatalf("Prompt waited %s, want it back on the first update", waited)
	}
	close(release)
	rec.waitIdle(t)
}

func TestStartFailsWithTheAgentError(t *testing.T) {
	m := newMockAgent(t, loadFixture(t, "codex"))
	m.newErr = acp.NewAuthRequired(nil)
	r, w := m.pipes()
	_, err := Start(context.Background(), r, w, Config{LogPath: t.TempDir() + "/x.jsonl", Cwd: "/workspace", Logger: discard})
	if err == nil || !strings.Contains(err.Error(), "session/new") || !strings.Contains(err.Error(), "Authentication required") {
		t.Fatalf("Start: %v", err)
	}

	// When the stored session could not be restored either, both reasons show.
	m = newMockAgent(t, loadFixture(t, "codex"))
	m.newErr = acp.NewAuthRequired(nil)
	m.resume = func() *acp.RequestError { return &acp.RequestError{Code: -32603, Message: "resume broke"} }
	m.load = func() *acp.RequestError { return &acp.RequestError{Code: -32603, Message: "load broke"} }
	r, w = m.pipes()
	_, err = Start(context.Background(), r, w, Config{LogPath: t.TempDir() + "/x.jsonl", Cwd: "/workspace", SessionID: m.sessionID(), Logger: discard})
	if err == nil || !strings.Contains(err.Error(), "load broke") || !strings.Contains(err.Error(), "Authentication required") {
		t.Fatalf("Start after a failed restore: %v", err)
	}
}

// A fresh or far-behind viewer gets only the newest ReplayWindow items; one
// inside the window gets exactly its gap.
func TestSubscribeReplaysAtMostTheWindow(t *testing.T) {
	m := newMockAgent(t, loadFixture(t, "codex"))
	m.onPrompt = func(m *mockAgent, _ promptCall) (any, *acp.RequestError) {
		for i := range ReplayWindow + 100 {
			m.update(map[string]any{"sessionUpdate": "tool_call", "toolCallId": fmt.Sprint("t", i), "title": "read", "status": "completed"})
		}
		return map[string]any{"stopReason": "end_turn"}, nil
	}
	s, rec := startMock(t, m, Config{})
	if _, err := s.Prompt(context.Background(), textPrompt("go"), false, nil); err != nil {
		t.Fatal(err)
	}
	rec.waitIdle(t)
	last := s.Log().LastSeq()
	for _, after := range []int64{0, last - ReplayWindow - 50, last - 10} {
		replay, _, cancel, err := s.Subscribe(after)
		if err != nil {
			t.Fatal(err)
		}
		cancel()
		want := min(last-after, ReplayWindow)
		if int64(len(replay)) != want || replay[0].Seq != last-want+1 || replay[len(replay)-1].Seq != last {
			t.Fatalf("Subscribe(%d) replayed %d items from %d, want the %d up to %d", after, len(replay), replay[0].Seq, want, last)
		}
	}
}

// A viewer that subscribes while the agent streams sees every item once, in
// order, split between the replay and the channel.
func TestSubscribeHasNoGap(t *testing.T) {
	m := newMockAgent(t, loadFixture(t, "codex"))
	started := make(chan struct{})
	m.onPrompt = func(m *mockAgent, _ promptCall) (any, *acp.RequestError) {
		for i := range 600 {
			if i == 200 {
				close(started)
			}
			m.update(map[string]any{"sessionUpdate": "tool_call", "toolCallId": fmt.Sprint("t", i), "title": "read", "status": "completed"})
		}
		return map[string]any{"stopReason": "end_turn"}, nil
	}
	s, rec := startMock(t, m, Config{})
	if _, err := s.Prompt(context.Background(), textPrompt("go"), false, nil); err != nil {
		t.Fatal(err)
	}
	<-started
	after := s.Log().LastSeq() - 5
	replay, ch, cancel, err := s.Subscribe(after)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	got := seqs(replay)
	for it := range ch {
		got = append(got, it.Seq)
		if it.Kind == KindTurnEnd {
			break
		}
	}
	rec.waitIdle(t)
	want := seqs(items(t, s))[after:]
	if !slices.Equal(got, want) {
		t.Fatalf("subscriber saw %d items, log has %d after seq %d", len(got), len(want), after)
	}
}
