package acphost

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"
)

func shortenAnnounceWait(t *testing.T) {
	t.Helper()
	old := childAnnounceWait
	childAnnounceWait = 50 * time.Millisecond
	t.Cleanup(func() { childAnnounceWait = old })
}

// sendStream sends the notifications of a testdata stream, skipping its
// leading # provenance line.
func (m *mockAgent) sendStream(name string) {
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		m.t.Error(err)
		return
	}
	for line := range strings.Lines(string(data)) {
		if strings.HasPrefix(line, "#") {
			continue
		}
		var n struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal([]byte(line), &n); err != nil {
			m.t.Errorf("%s: %v", name, err)
			return
		}
		if err := m.conn.SendNotification(context.Background(), n.Method, n.Params); err != nil {
			m.t.Error(err)
		}
	}
}

func streamItems(t *testing.T, name string) []Item {
	t.Helper()
	m := newMockAgent(t, loadFixture(t, "codex"))
	m.onPrompt = func(m *mockAgent, _ promptCall) (any, *acp.RequestError) {
		m.sendStream(name)
		return map[string]any{"stopReason": "end_turn"}, nil
	}
	s, rec := startMock(t, m, Config{})
	if _, err := s.Prompt(context.Background(), textPrompt("delegate"), false, nil); err != nil {
		t.Fatal(err)
	}
	rec.waitIdle(t)
	its := items(t, s)
	if unknown := ofKind(its, KindUnknown); len(unknown) != 0 {
		t.Fatalf("updates kept as unknown: %+v", unknown)
	}
	return its
}

func lastCalls(its []Item) map[string]*ToolCall {
	last := map[string]*ToolCall{}
	for _, it := range ofKind(its, KindToolCall) {
		last[it.ToolCall.ID] = it.ToolCall
	}
	return last
}

type callWant struct{ parent, status string }

func checkCalls(t *testing.T, its []Item, want map[string]callWant) map[string]*ToolCall {
	t.Helper()
	last := lastCalls(its)
	if len(last) != len(want) {
		t.Fatalf("%d tool calls, want %d: %+v", len(last), len(want), last)
	}
	for id, w := range want {
		if tc := last[id]; tc == nil || tc.ParentID != w.parent || tc.Status != w.status {
			t.Fatalf("%s = %+v, want parent %q and status %q", id, tc, w.parent, w.status)
		}
	}
	return last
}

func textByParent(its []Item) map[string]string {
	text := map[string]string{}
	for _, it := range ofKind(its, KindMessage) {
		if it.Message.Role == "assistant" {
			text[it.Message.ParentID] += it.Message.Text
		}
	}
	return text
}

func TestSubagentSessionsNest(t *testing.T) {
	its := streamItems(t, "codex_subagents.jsonl")
	const (
		c1 = "subagent:019a2c3e-5f10-7c21-9d3b-1a2b3c4d5e01"
		c2 = "subagent:019a2c3e-7a44-7e02-8c5d-2b3c4d5e6f02"
		c3 = "subagent:019a2c3e-9b70-7a13-a6e4-3c4d5e6f7a03"
	)
	last := checkCalls(t, its, map[string]callWant{
		c1: {"", "completed"}, c2: {c1, "completed"}, c3: {"", "in_progress"},
		"call_c1_read": {c1, "completed"}, "call_c2_grep": {c2, "completed"},
	})
	if l := last[c1]; l.Title != "Explorer" || l.ToolKind != "think" || l.ToolName != "" ||
		!reflect.DeepEqual(l.Subagent, &Subagent{Description: "Explorer"}) || string(l.RawInput) != `{"prompt":"Map the billing module"}` {
		t.Fatalf("launch %+v", l)
	}
	want := map[string]string{"": "Delegating the audit.The audit is done.", c1: "Reading the billing module.", c2: "Two callers found."}
	if got := textByParent(its); !reflect.DeepEqual(got, want) {
		t.Fatalf("messages by parent %q, want %q", got, want)
	}
	if plans := ofKind(its, KindPlan); len(plans) != 1 || plans[0].PlanParentID != c1 {
		t.Fatalf("plans %+v", plans)
	}
	if n := len(ofKind(its, KindUsage)) + len(ofKind(its, KindSessionInfo)); n != 0 {
		t.Fatalf("a subagent's usage or title became %d items", n)
	}
}

func TestDraftSubagentUpdates(t *testing.T) {
	its := streamItems(t, "rfd_subagents.jsonl")
	const r1, r2, r3 = "subagent:rfd-child-1", "subagent:rfd-child-2", "subagent:rfd-child-3"
	last := checkCalls(t, its, map[string]callWant{
		r1: {"", "completed"}, r2: {r1, "failed"}, r3: {"", "in_progress"}, "r2-test": {r2, "completed"},
	})
	if l := last[r1]; l.Title != "Review the diff" || !reflect.DeepEqual(l.Subagent, &Subagent{Description: "Review the diff"}) ||
		string(l.RawInput) != `{"description":"Reviews the auth changes"}` {
		t.Fatalf("a state-only update lost the labels: %+v", l)
	}
	if l := last[r3]; l.Title != "" || !reflect.DeepEqual(l.Subagent, &Subagent{}) {
		t.Fatalf("a null title was not cleared: %+v", l)
	}
	if got := textByParent(its)[r1]; got != "Looking at auth.go." {
		t.Fatalf("subagent text %q", got)
	}
}

func TestSubagentRequestsAreTheRunsRequests(t *testing.T) {
	m := newMockAgent(t, loadFixture(t, "codex"))
	got := make(chan string, 1)
	m.onPrompt = func(m *mockAgent, call promptCall) (any, *acp.RequestError) {
		answered := make(chan struct{})
		go func() {
			defer close(answered)
			req := m.permissionRequest()
			req["sessionId"] = "child"
			res, err := m.request(call.ctx, acp.ClientMethodSessionRequestPermission, req)
			if err != nil {
				got <- err.Error()
			} else {
				got <- string(res)
			}
		}()
		// The request reaches the host before the update announcing its
		// session.
		time.Sleep(20 * time.Millisecond)
		m.update(map[string]any{"sessionUpdate": "subagent_spawned", "subagentSessionId": "child", "name": "Cleaner", "task": "Clean the build", "capabilities": map[string]any{}})
		<-answered
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
	if st := s.State(); len(st.Pending) != 1 || st.Pending[0].ToolCallID != "t1" {
		t.Fatalf("state pending %+v", st.Pending)
	}
	if err := s.Answer(pending[0].ID, "allow", nil); err != nil {
		t.Fatal(err)
	}
	if res := <-got; res != `{"outcome":{"optionId":"allow","outcome":"selected"}}` {
		t.Fatalf("agent got %s", res)
	}
	rec.waitIdle(t)
	if tc := lastCalls(items(t, s))["t1"]; tc == nil || tc.ParentID != "subagent:child" || tc.Title != "rm -rf build" {
		t.Fatalf("the permission's tool call %+v", tc)
	}
}

func TestRequestsFromAnUnannouncedSessionAreRejected(t *testing.T) {
	shortenAnnounceWait(t)
	m := newMockAgent(t, loadFixture(t, "codex"))
	m.onPrompt = func(m *mockAgent, call promptCall) (any, *acp.RequestError) {
		req := m.permissionRequest()
		req["sessionId"] = "stranger"
		_, perr := m.request(call.ctx, acp.ClientMethodSessionRequestPermission, req)
		_, eerr := m.request(call.ctx, acp.ClientMethodElicitationCreate, map[string]any{
			"sessionId": "stranger", "mode": "form", "message": "Which branch?", "requestedSchema": map[string]any{"type": "object"},
		})
		for _, err := range []error{perr, eerr} {
			var re *acp.RequestError
			if !errors.As(err, &re) || re.Code != -32602 {
				t.Errorf("request from an unannounced session: %v", err)
			}
		}
		return map[string]any{"stopReason": "end_turn"}, nil
	}
	s, rec := startMock(t, m, Config{})
	if _, err := s.Prompt(context.Background(), textPrompt("go"), false, nil); err != nil {
		t.Fatal(err)
	}
	rec.waitIdle(t)
	if reqs := ofKind(items(t, s), KindRequest); len(reqs) != 0 {
		t.Fatalf("requests recorded: %+v", reqs)
	}
}

func TestLoadRelearnsSubagentSessions(t *testing.T) {
	m := newMockAgent(t, loadFixture(t, "codex"))
	m.resume = func() *acp.RequestError { return &acp.RequestError{Code: -32603, Message: "Internal error"} }
	m.load = func() *acp.RequestError {
		m.update(map[string]any{"sessionUpdate": "subagent_spawned", "subagentSessionId": "old", "name": "Explorer", "task": "Map it", "capabilities": map[string]any{}})
		m.updateOn("old", map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "Earlier work."}})
		m.updateOn("old", map[string]any{"sessionUpdate": "tool_call", "toolCallId": "old-read", "title": "Read", "kind": "read", "status": "completed"})
		m.updateOn("old", map[string]any{"sessionUpdate": "usage_update", "used": 10, "size": 100})
		return nil
	}
	m.onPrompt = func(m *mockAgent, _ promptCall) (any, *acp.RequestError) {
		m.updateOn("old", map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "Later work."}})
		m.updateOn("old", map[string]any{"sessionUpdate": "tool_call", "toolCallId": "new-read", "title": "Read", "kind": "read", "status": "completed"})
		m.update(map[string]any{"sessionUpdate": "subagent_state_update", "subagentSessionId": "old", "state": "completed"})
		return map[string]any{"stopReason": "end_turn"}, nil
	}
	s, rec := startMock(t, m, Config{SessionID: m.sessionID()})
	rec.waitIdle(t)
	if !m.called(acp.AgentMethodSessionLoad) {
		t.Fatalf("methods %v", m.methods)
	}
	its := items(t, s)
	for _, k := range []Kind{KindToolCall, KindMessage, KindUsage, KindUnknown} {
		if n := len(ofKind(its, k)); n != 0 {
			t.Fatalf("replay recorded %d %s items: %+v", n, k, its)
		}
	}
	if _, err := s.Prompt(context.Background(), textPrompt("again"), false, nil); err != nil {
		t.Fatal(err)
	}
	rec.waitIdle(t)
	its = items(t, s)
	last := checkCalls(t, its, map[string]callWant{"new-read": {"subagent:old", "completed"}, "subagent:old": {"", "completed"}})
	if l := last["subagent:old"]; l.Title != "Explorer" || string(l.RawInput) != `{"prompt":"Map it"}` {
		t.Fatalf("the restored launch lost its labels: %+v", l)
	}
	if got := textByParent(its)["subagent:old"]; got != "Later work." {
		t.Fatalf("subagent text after load %q", got)
	}
}

func TestCancelStopsARequestWaitingForItsSession(t *testing.T) {
	m := newMockAgent(t, loadFixture(t, "codex"))
	got := make(chan string, 1)
	m.onPrompt = func(m *mockAgent, call promptCall) (any, *acp.RequestError) {
		m.update(map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "Delegating."}})
		answered := make(chan struct{})
		go func() {
			defer close(answered)
			req := m.permissionRequest()
			req["sessionId"] = "child"
			res, err := m.request(call.ctx, acp.ClientMethodSessionRequestPermission, req)
			if err != nil {
				got <- err.Error()
			} else {
				got <- string(res)
			}
		}()
		<-m.cancelled
		// The cancellation reaches the host before the announcement.
		time.Sleep(20 * time.Millisecond)
		m.update(map[string]any{"sessionUpdate": "subagent_spawned", "subagentSessionId": "child", "name": "Cleaner", "task": "Clean the build", "capabilities": map[string]any{}})
		select {
		case <-answered:
		case <-time.After(2 * time.Second):
		}
		return map[string]any{"stopReason": "cancelled"}, nil
	}
	s, rec := startMock(t, m, Config{})
	if _, err := s.Prompt(context.Background(), textPrompt("clean"), false, nil); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	if err := s.Cancel(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case res := <-got:
		if res != `{"outcome":{"outcome":"cancelled"}}` {
			t.Fatalf("agent got %s", res)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("the request opened after the cancel: %+v", s.State().Pending)
	}
	rec.waitIdle(t)
	its := items(t, s)
	if reqs := ofKind(its, KindRequest); len(reqs) != 0 || lastCalls(its)["t1"] != nil {
		t.Fatalf("a cancelled request was recorded: %+v", its)
	}
}

func TestCursorLaunchIsLabelledAtOnce(t *testing.T) {
	m := newMockAgent(t, loadFixture(t, "codex"))
	m.onPrompt = func(m *mockAgent, _ promptCall) (any, *acp.RequestError) {
		m.update(map[string]any{"sessionUpdate": "tool_call", "toolCallId": "task-1", "title": "Map the module", "kind": "other", "status": "in_progress",
			"rawInput": map[string]any{"description": "Map the module", "prompt": "Map billing", "subagentType": "explore"}})
		m.update(map[string]any{"sessionUpdate": "subagent_spawned", "subagentSessionId": "agent-1", "name": "explore", "task": "Map billing", "capabilities": map[string]any{},
			"_meta": map[string]any{"cursor": map[string]any{"toolCallId": "task-1", "agentId": "agent-1"}}})
		m.updateOn("agent-1", map[string]any{"sessionUpdate": "tool_call", "toolCallId": "child-read", "title": "Read", "kind": "read", "status": "completed"})
		m.update(map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "task-1", "status": "completed"})
		return map[string]any{"stopReason": "end_turn"}, nil
	}
	s, rec := startMock(t, m, Config{})
	if _, err := s.Prompt(context.Background(), textPrompt("map"), false, nil); err != nil {
		t.Fatal(err)
	}
	rec.waitIdle(t)
	its := items(t, s)
	last := checkCalls(t, its, map[string]callWant{"task-1": {"", "completed"}, "child-read": {"task-1", "completed"}})
	if l := last["task-1"]; !reflect.DeepEqual(l.Subagent, &Subagent{Type: "explore"}) || l.Title != "Map the module" {
		t.Fatalf("Cursor's task call %+v", l)
	}
	for _, it := range ofKind(its, KindToolCall) {
		switch it.ToolCall.ID {
		case "task-1":
			if it.ToolCall.Subagent != nil {
				return
			}
		case "child-read":
			t.Fatalf("the subagent's first item came before its launch was labelled: %+v", its)
		}
	}
}
