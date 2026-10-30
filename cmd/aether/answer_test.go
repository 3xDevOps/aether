package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

func answerControl(t *testing.T, items []protocol.AttentionItem, respond *fakeReply) (*protocol.Client, *fakeReply) {
	t.Helper()
	post := &fakeReply{result: protocol.RunRoomPostResult{Message: protocol.RoomMessage{ID: "msg-1"}}}
	return fakeControlMethods(t, map[string]*fakeReply{
		protocol.MethodRunGet:          {result: protocol.RunResult{Run: protocol.Run{ID: "r1", WorkspaceID: "ws1", Status: "needs-attention"}}},
		protocol.MethodAttentionList:   {result: protocol.AttentionListResult{Items: items}},
		protocol.MethodRunInputRespond: respond,
		protocol.MethodRunRoomPost:     post,
	}), post
}

// aether answer allows a permission request once or rejects it once; it
// never sends an always option, which would change the agent's policy.
func TestAnswerAllowsAPermissionOnlyOnce(t *testing.T) {
	respond := &fakeReply{result: protocol.RunInputRespondResult{Status: protocol.RunInputRespondAnswered}}
	c, _ := answerControl(t, []protocol.AttentionItem{permissionItem()}, respond)
	var out bytes.Buffer
	if err := answer(c, &out, nil, "r1", "req-1", "always-1", nil); err == nil || err.Error() != "option always-1 is not allow once or reject once, and aether answer never answers for always: pick one of allow-1, reject-1, or decide it in the Aether dashboard" {
		t.Fatalf("answer with an always option = %v, want a refusal naming the once options", err)
	}
	if respond.params() != nil {
		t.Fatalf("an always option reached run.input.respond: %s", respond.params())
	}
	if err := answer(c, &out, nil, "r1", "req-1", "allow-1", nil); err != nil {
		t.Fatal(err)
	}
	if got, want := string(respond.params()), `{"run_id":"r1","request_id":"req-1","option_id":"allow-1"}`; got != want {
		t.Fatalf("run.input.respond params = %s, want %s", got, want)
	}
	if got, want := out.String(), "run r1 permission req-1: answered allow-1 (Allow once)\n"; got != want {
		t.Fatalf("answer printed %q, want %q", got, want)
	}
	if err := answer(c, &out, nil, "r1", "req-9", "allow-1", nil); err == nil || !strings.Contains(err.Error(), "aether runs r1 lists what is pending") {
		t.Fatalf("answer to nothing pending = %v", err)
	}
}

func TestAnswerShowsTheServersSudoRefusal(t *testing.T) {
	refusal := "run.input.respond: permission denied: allowing a command to run as root requires the run's owner or an admin"
	respond := &fakeReply{err: &protocol.Error{Code: protocol.CodeDenied, Message: refusal}}
	c, _ := answerControl(t, nil, respond)
	if err := answer(c, &bytes.Buffer{}, nil, "r1", "sudo-abc", domain.SudoAllow, nil); err == nil || !strings.Contains(err.Error(), refusal) {
		t.Fatalf("answer to a sudo request listed for someone else = %v", err)
	}
	if got, want := string(respond.params()), `{"run_id":"r1","request_id":"sudo-abc","option_id":"allow-once"}`; got != want {
		t.Fatalf("run.input.respond params = %s, want %s", got, want)
	}
}

func TestAnswerNamesAQueuedRequest(t *testing.T) {
	c := fakeControlMethods(t, map[string]*fakeReply{
		protocol.MethodRunGet: {result: protocol.RunResult{Run: protocol.Run{ID: "r1", WorkspaceID: "ws1", Status: "needs-attention",
			PendingInputs: []domain.RunInputRequest{{ID: "req-1", Kind: "permission"}, {ID: "req-2", Kind: "permission"}}}}},
		protocol.MethodAttentionList: {result: protocol.AttentionListResult{Items: []protocol.AttentionItem{permissionItem()}}},
	})
	err := answer(c, &bytes.Buffer{}, nil, "r1", "req-2", "allow-1", nil)
	if err == nil || !strings.Contains(err.Error(), "request req-2 on run r1 is queued behind") || !strings.Contains(err.Error(), "aether runs r1") {
		t.Fatalf("answer to a queued request = %v", err)
	}
}

func TestAnswerNamesTheWayPastAControlHolder(t *testing.T) {
	data, err := json.Marshal(map[string]string{"reason": protocol.ErrorReasonControlHeld})
	if err != nil {
		t.Fatal(err)
	}
	respond := &fakeReply{err: &protocol.Error{Code: protocol.CodeConflict, Message: "run control is held by another member", Data: data}}
	item := protocol.AttentionItem{Kind: protocol.AttentionKindQuestion, RunID: "r1", WorkspaceID: "ws1",
		Request: &protocol.AttentionRequest{ID: "q-1", Kind: "question", Title: "Which database?", Options: []protocol.AttentionOption{{ID: "pg", Label: "Postgres"}}}}
	c, _ := answerControl(t, []protocol.AttentionItem{item}, respond)
	err = answer(c, &bytes.Buffer{}, nil, "r1", "q-1", "pg", map[string]any{"name": "app"})
	if err == nil || !strings.HasSuffix(err.Error(), "send your answer to the agent with aether message r1 -") {
		t.Fatalf("answer while control is held = %v", err)
	}
	if got, want := string(respond.params()), `{"run_id":"r1","request_id":"q-1","option_id":"pg","values":{"name":"app"}}`; got != want {
		t.Fatalf("run.input.respond params = %s, want %s", got, want)
	}
}

// A teammate's question takes text, here from stdin, and is answered once
// however often the command runs.
func TestAnswerRepliesToATeammatesQuestion(t *testing.T) {
	item := protocol.AttentionItem{Kind: protocol.AttentionKindRoomQuestion, RunID: "r1", WorkspaceID: "ws1",
		Message: &protocol.AttentionMessage{ID: "msg-q", Body: "ship it today?"}}
	c, post := answerControl(t, []protocol.AttentionItem{item}, nil)
	var out bytes.Buffer
	if err := answer(c, &out, strings.NewReader("yes, after CI\n"), "r1", "msg-q", "-", nil); err != nil {
		t.Fatal(err)
	}
	want := `{"workspace_id":"ws1","run_id":"r1","kind":"reply","body":"yes, after CI","correlation_id":"msg-q","idempotency_key":"cli-answer-msg-q"}`
	if got := string(post.params()); got != want {
		t.Fatalf("run.room.post params = %s, want %s", got, want)
	}
	if got := out.String(); got != "replied to question msg-q on run r1\n" {
		t.Fatalf("answer printed %q", got)
	}
}

func TestNeedLinesNameTheAnswer(t *testing.T) {
	standard := protocol.AttentionItem{Kind: protocol.AttentionKindQuestion, RunID: "r2", Reason: "Question: answer in the terminal",
		Request: &protocol.AttentionRequest{ID: "q-2", Kind: "question"}}
	swarm := protocol.AttentionItem{Kind: protocol.AttentionKindSwarmQuestion, RunID: "r3", MissionID: "m1",
		Message: &protocol.AttentionMessage{ID: "mq-1", Body: "which branch?"}}
	waiting := protocol.AttentionItem{Kind: protocol.AttentionKindNeedsInput, RunID: "r4", Reason: "which database?"}
	var lines []string
	for _, it := range []protocol.AttentionItem{permissionItem(), standard, swarm, waiting} {
		lines = append(lines, needLines(it)...)
	}
	want := "r1 permission req-1: Run rm -rf build\n" +
		"  aether answer r1 req-1 allow-1    # Allow once\n" +
		"  aether answer r1 req-1 reject-1    # Reject\n" +
		"r2 question q-2: Question: answer in the terminal\n" +
		"  answer it in the run's terminal: aether attach r2\n" +
		"r3 swarm-question mq-1: which branch?\n" +
		"  aether answer r3 mq-1 \"<answer>\"\n" +
		"r4 needs-input: which database?\n" +
		"  aether message r4 \"<reply>\""
	if got := strings.Join(lines, "\n"); got != want {
		t.Fatalf("need lines = %q\nwant %q", got, want)
	}
}
