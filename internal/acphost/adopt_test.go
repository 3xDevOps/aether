package acphost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"github.com/3xDevOps/Aether/internal/acpkeeper"
)

// adoptLog is an item log whose turn 2 is open after turn 1 ended.
func adoptLog(t *testing.T) string {
	t.Helper()
	path := t.TempDir() + "/run.items.jsonl"
	log, err := OpenLog(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range []Item{{Kind: KindTurnStart, Turn: 1}, {Kind: KindTurnEnd, Turn: 1, StopReason: "end_turn"}, {Kind: KindTurnStart, Turn: 2}} {
		if err := log.Append(&it); err != nil {
			t.Fatal(err)
		}
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAdoptionTakesOverTheOpenTurnsPrompt(t *testing.T) {
	m := newMockAgent(t, loadFixture(t, "claude"))
	s, _ := startMock(t, m, Config{LogPath: adoptLog(t), Adopt: &Adoption{
		Initialize: m.fix.Initialize, Session: m.fix.SessionNew, SessionID: m.sessionID(), PromptTurn: 2,
	}})
	waitFor(t, "the adopted prompt", func() bool { return m.called(acpkeeper.AdoptMethod) })
	if m.called(acp.AgentMethodInitialize) || m.called(acp.AgentMethodSessionPrompt) {
		t.Fatal("the adoption initialized the agent or prompted it again")
	}
	_ = s
}

func TestAdoptionInterruptsATurnWhosePromptNeverReachedTheAgent(t *testing.T) {
	m := newMockAgent(t, loadFixture(t, "claude"))
	// The keeper still holds turn 1's prompt, answered but not acked.
	s, _ := startMock(t, m, Config{LogPath: adoptLog(t), Adopt: &Adoption{
		Initialize: m.fix.Initialize, Session: m.fix.SessionNew, SessionID: m.sessionID(), PromptTurn: 1,
	}})
	its := items(t, s)
	if !slices.ContainsFunc(its, func(it Item) bool {
		return it.Kind == KindTurnEnd && it.Turn == 2 && it.StopReason == "interrupted"
	}) {
		t.Fatalf("turn 2 did not end interrupted: %+v", its)
	}
	if m.called(acpkeeper.AdoptMethod) {
		t.Fatal("turn 1's answer was adopted as turn 2's")
	}
}

// pendingLog is an item log whose open turn 1 left a permission request
// unanswered.
func pendingLog(t *testing.T) string {
	t.Helper()
	path := t.TempDir() + "/run.items.jsonl"
	log, err := OpenLog(path)
	if err != nil {
		t.Fatal(err)
	}
	req := &Request{ID: "req-1", Kind: RequestPermission, Title: "rm -rf build", ToolCallID: "t1", Status: RequestPending, RPCID: "1",
		Options: []Option{{ID: "allow", Name: "Allow", Kind: "allow_once"}, {ID: "reject", Name: "Reject", Kind: "reject_once"}}}
	for _, it := range []Item{{Kind: KindTurnStart, Turn: 1}, {Kind: KindRequest, Turn: 1, Request: req}} {
		if err := log.Append(&it); err != nil {
			t.Fatal(err)
		}
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAnAnswerBeforeTheKeepersReplayReachesTheAgent(t *testing.T) {
	m := newMockAgent(t, loadFixture(t, "claude"))
	s, _ := startMock(t, m, Config{LogPath: pendingLog(t), Adopt: &Adoption{
		Initialize: m.fix.Initialize, Session: m.fix.SessionNew, SessionID: m.sessionID(), PromptTurn: 1, ReplayWait: time.Minute,
	}})
	if err := s.Answer("req-1", "allow", nil); !errors.Is(err, ErrUnknownRequest) {
		t.Fatalf("answer without waiting for the replay = %v", err)
	}
	answered := make(chan error, 1)
	go func() {
		<-s.Replayed()
		answered <- s.Answer("req-1", "allow", nil)
	}()
	select {
	case err := <-answered:
		t.Fatalf("the answer did not wait for the replay: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	got := make(chan string, 1)
	go func() {
		res, err := m.request(context.Background(), acp.ClientMethodSessionRequestPermission, m.permissionRequest())
		if err != nil {
			got <- err.Error()
			return
		}
		got <- string(res)
	}()
	if err := <-answered; err != nil {
		t.Fatalf("answer after the replay: %v", err)
	}
	if res := <-got; res != `{"outcome":{"optionId":"allow","outcome":"selected"}}` {
		t.Fatalf("agent got %s", res)
	}
}

func TestReplayedRequestsKeepTheirIDsByTheAgentsRequestID(t *testing.T) {
	m := newMockAgent(t, loadFixture(t, "claude"))
	schema := func(field string) json.RawMessage {
		return json.RawMessage(`{"type":"object","properties":{"` + field + `":{"type":"string"}}}`)
	}
	path := t.TempDir() + "/run.items.jsonl"
	log, err := OpenLog(path)
	if err != nil {
		t.Fatal(err)
	}
	// Two questions alike but for their schemas, logged in the opposite order
	// to the agent's request ids.
	for _, it := range []Item{{Kind: KindTurnStart, Turn: 1},
		{Kind: KindRequest, Turn: 1, Request: &Request{ID: "req-1", Kind: RequestQuestion, Title: "Which?", Schema: schema("tag"), Options: elicitationOptions, Status: RequestPending, RPCID: "2"}},
		{Kind: KindRequest, Turn: 1, Request: &Request{ID: "req-2", Kind: RequestQuestion, Title: "Which?", Schema: schema("branch"), Options: elicitationOptions, Status: RequestPending, RPCID: "1"}},
	} {
		if err := log.Append(&it); err != nil {
			t.Fatal(err)
		}
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	s, _ := startMock(t, m, Config{LogPath: path, Adopt: &Adoption{
		Initialize: m.fix.Initialize, Session: m.fix.SessionNew, SessionID: m.sessionID(), PromptTurn: 1, ReplayWait: time.Minute,
	}})
	replies := map[string]chan string{"branch": make(chan string, 1), "tag": make(chan string, 1)}
	for i, field := range []string{"branch", "tag"} {
		go func() {
			res, err := m.request(context.Background(), acp.ClientMethodElicitationCreate, map[string]any{
				"sessionId": m.sessionID(), "mode": "form", "message": "Which?", "requestedSchema": schema(field),
			})
			if err != nil {
				replies[field] <- err.Error()
				return
			}
			replies[field] <- string(res)
		}()
		waitFor(t, "the replayed question", func() bool {
			s.conn.mu.Lock()
			defer s.conn.mu.Unlock()
			return len(s.conn.pending) == i+1
		})
	}
	if err := s.Answer("req-2", "accept", map[string]any{"branch": "main"}); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-replies["branch"]:
		if got != `{"action":"accept","content":{"branch":"main"}}` {
			t.Fatalf("the branch question got %s", got)
		}
	case got := <-replies["tag"]:
		t.Fatalf("the branch question's answer reached the tag question: %s", got)
	}
}

func TestAReplayThatNeverComesStopsHoldingAnswers(t *testing.T) {
	m := newMockAgent(t, loadFixture(t, "claude"))
	s, _ := startMock(t, m, Config{LogPath: pendingLog(t), Adopt: &Adoption{
		Initialize: m.fix.Initialize, Session: m.fix.SessionNew, SessionID: m.sessionID(), PromptTurn: 1, ReplayWait: 50 * time.Millisecond,
	}})
	select {
	case <-s.Replayed():
	case <-time.After(5 * time.Second):
		t.Fatal("the replay wait never ended")
	}
	if err := s.Answer("req-1", "allow", nil); !errors.Is(err, ErrUnknownRequest) {
		t.Fatalf("answer to a request the keeper never sent again = %v", err)
	}
}

func TestASessionThatWasNotAdoptedIsReplayed(t *testing.T) {
	s, _ := startMock(t, newMockAgent(t, loadFixture(t, "claude")), Config{})
	select {
	case <-s.Replayed():
	default:
		t.Fatal("a new session waits for a replay")
	}
}

func TestAdoptionKeepsTheLastRecordedSelections(t *testing.T) {
	m := newMockAgent(t, loadFixture(t, "claude"))
	var res struct {
		ConfigOptions []map[string]any `json:"configOptions"`
	}
	if err := json.Unmarshal(m.fix.SessionNew, &res); err != nil {
		t.Fatal(err)
	}
	for _, option := range res.ConfigOptions {
		switch option["id"] {
		case "model":
			option["currentValue"] = "opus"
		case "mode":
			option["currentValue"] = "plan"
		}
	}
	selected, err := json.Marshal(res.ConfigOptions)
	if err != nil {
		t.Fatal(err)
	}
	path := t.TempDir() + "/run.items.jsonl"
	log, err := OpenLog(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range []Item{{Kind: KindConfigOptions, ConfigOptions: selected}, {Kind: KindModeChange, Mode: "plan"}, {Kind: KindTurnStart, Turn: 1}} {
		if err := log.Append(&it); err != nil {
			t.Fatal(err)
		}
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	s, _ := startMock(t, m, Config{LogPath: path, Adopt: &Adoption{
		Initialize: m.fix.Initialize, Session: m.fix.SessionNew, SessionID: m.sessionID(), PromptTurn: 1,
	}})
	st := s.State()
	if st.Mode != "plan" || !bytes.Equal(st.ConfigOptions, selected) {
		t.Fatalf("adopted mode %q, options %s; want plan and %s", st.Mode, st.ConfigOptions, selected)
	}
	its := items(t, s)
	if n := len(ofKind(its, KindConfigOptions)) + len(ofKind(its, KindModeChange)); n != 2 {
		t.Fatalf("the adoption recorded the selections again: %+v", its)
	}
}

func TestAdoptionKeepsTheSubagentsTheKeeperReported(t *testing.T) {
	shortenAnnounceWait(t)
	m := newMockAgent(t, loadFixture(t, "claude"))
	spawned, err := json.Marshal(map[string]any{"sessionId": m.sessionID(), "update": map[string]any{
		"sessionUpdate": "subagent_spawned", "subagentSessionId": "child", "name": "Cleaner", "task": "Clean the build", "capabilities": map[string]any{},
	}})
	if err != nil {
		t.Fatal(err)
	}
	s, rec := startMock(t, m, Config{LogPath: adoptLog(t), Adopt: &Adoption{
		Initialize: m.fix.Initialize, Session: m.fix.SessionNew, SessionID: m.sessionID(), PromptTurn: 2, Subagents: []json.RawMessage{spawned},
	}})
	got := make(chan error, 1)
	go func() {
		req := m.permissionRequest()
		req["sessionId"] = "child"
		_, err := m.request(context.Background(), acp.ClientMethodSessionRequestPermission, req)
		got <- err
	}()
	pending := rec.waitInputs(t, 1)
	if err := s.Answer(pending[0].ID, "allow", nil); err != nil {
		t.Fatal(err)
	}
	if err := <-got; err != nil {
		t.Fatalf("the adopted subagent's request: %v", err)
	}
	if tc := lastCalls(items(t, s))["t1"]; tc == nil || tc.ParentID != "subagent:child" {
		t.Fatalf("the permission's tool call %+v", tc)
	}
}
