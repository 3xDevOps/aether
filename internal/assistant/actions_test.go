package assistant

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/acphost"
	"github.com/3xDevOps/Aether/internal/assistant/tools"
	"github.com/3xDevOps/Aether/internal/coordtransport"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

type actionServer struct {
	mu    sync.Mutex
	calls []string
}

func (f *actionServer) Call(_ context.Context, method string, params json.RawMessage) (json.RawMessage, *protocol.Error) {
	f.mu.Lock()
	f.calls = append(f.calls, method)
	f.mu.Unlock()
	switch method {
	case protocol.MethodRunGet:
		if strings.Contains(string(params), "run-gone") {
			return nil, &protocol.Error{Code: protocol.CodeNotFound, Message: "run run-gone not found"}
		}
		if strings.Contains(string(params), "run-2") {
			return json.RawMessage(`{"run":{"id":"run-2","member_id":"m2","workspace_id":"ws-1","title":"Docs"}}`), nil
		}
		return json.RawMessage(`{"run":{"id":"run-1","member_id":"m1","workspace_id":"ws-1","title":"Fix login"}}`), nil
	case protocol.MethodMemberList:
		return json.RawMessage(`{"members":[{"id":"m1","display_name":"Ari"},{"id":"m2","display_name":"Dana"}]}`), nil
	case protocol.MethodWorkspaceGet:
		return json.RawMessage(`{"workspace":{"id":"ws-1","name":"api"}}`), nil
	case protocol.MethodRunLaunch:
		return json.RawMessage(`{"run":{"id":"run-new","workspace_id":"ws-1"}}`), nil
	case protocol.MethodRunKill:
		return nil, &protocol.Error{Code: protocol.CodeConflict, Message: "run run-1 is not live: it finished at 12:04"}
	}
	return json.RawMessage(`{}`), nil
}

func (f *actionServer) ran(method string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Contains(f.calls, method)
}

// serveTools serves m's tool socket as a live session does.
func serveTools(t *testing.T, svc *Service, m *member) (string, *actionServer) {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "a.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	fake := &actionServer{}
	srv := &tools.Server{Caller: actionCaller{Caller: fake, s: svc, m: m}, Confirm: svc.approver(m), Member: string(m.id)}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx, ln) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Serve: %v", err)
		}
	})
	return sock, fake
}

// actions reads the action records in m's transcript, grouped by action in
// the order each began.
func actions(t *testing.T, svc *Service, m *member) [][]protocol.AssistantAction {
	t.Helper()
	f, err := os.Open(svc.logPath(m.id))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	var out [][]protocol.AssistantAction
	index := map[string]int{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var it acphost.Item
		if err := json.Unmarshal(sc.Bytes(), &it); err != nil {
			t.Fatal(err)
		}
		if it.Notice == nil || it.Notice.Action == nil {
			continue
		}
		a := *it.Notice.Action
		i, ok := index[a.ID]
		if !ok {
			i = len(out)
			index[a.ID] = i
			out = append(out, nil)
		}
		out[i] = append(out[i], a)
	}
	return out
}

func outcomes(records []protocol.AssistantAction) []string {
	out := make([]string, len(records))
	for i, a := range records {
		out[i] = a.Outcome
	}
	return out
}

// callTool makes a tool call and, when answer is set, answers the card it
// shows with approve.
func callTool(t *testing.T, sock string, m *member, method string, params any, answer, approve bool) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- coordtransport.Call(t.Context(), sock, method, params, nil) }()
	if answer {
		deadline := time.Now().Add(5 * time.Second)
		for len(m.confirms.snapshot()) == 0 {
			if time.Now().After(deadline) {
				t.Fatal("no card was shown")
			}
			time.Sleep(time.Millisecond)
		}
		if err := m.confirms.decide(m.confirms.snapshot()[0].ID, approve); err != nil {
			t.Fatal(err)
		}
	}
	return wait(t, done)
}

func newActionService(t *testing.T) (*Service, *member, *memStore) {
	t.Helper()
	db := newMemStore()
	svc := New(Config{Dir: t.TempDir(), Store: db})
	t.Cleanup(func() { _ = svc.Close() })
	return svc, svc.member("m1"), db
}

func TestApprovedLaunchRecordsApprovalThenTheLaunchedRun(t *testing.T) {
	svc, m, _ := newActionService(t)
	sock, _ := serveTools(t, svc, m)
	launch := protocol.RunLaunchParams{WorkspaceID: "ws-1", Harness: "claude", Task: "fix the login redirect"}
	if err := callTool(t, sock, m, protocol.MethodRunLaunch, launch, true, true); err != nil {
		t.Fatal(err)
	}
	got := actions(t, svc, m)
	if len(got) != 1 || !slices.Equal(outcomes(got[0]), []string{protocol.AssistantActionApproved, protocol.AssistantActionSucceeded}) {
		t.Fatalf("records = %+v, want one action approved, then succeeded", got)
	}
	approved, done := got[0][0], got[0][1]
	if approved.Approval != "card" || approved.ConfirmationID == "" || approved.Method != protocol.MethodRunLaunch ||
		approved.Target.WorkspaceID != "ws-1" || approved.Summary == "" || approved.CreatedAt.IsZero() || approved.DecidedAt.IsZero() {
		t.Fatalf("approval = %+v, want the card, its target and times", approved)
	}
	if done.Result == nil || done.Result.RunID != "run-new" || done.Result.WorkspaceID != "ws-1" || done.FinishedAt.IsZero() ||
		done.ConfirmationID != approved.ConfirmationID || done.Approval != "card" {
		t.Fatalf("outcome = %+v, want the launched run and the approval kept", done)
	}
}

func TestAutoApprovedCallIsRecordedBeforeAndAfter(t *testing.T) {
	svc, m, db := newActionService(t)
	db.settings["m1"] = store.AssistantSettings{AutoApprove: true}
	sock, _ := serveTools(t, svc, m)
	if err := callTool(t, sock, m, protocol.MethodRunInject, protocol.RunInjectParams{RunID: "run-1", Message: "rerun the tests", IdempotencyKey: "key-1"}, false, false); err != nil {
		t.Fatal(err)
	}
	got := actions(t, svc, m)
	if len(got) != 1 || !slices.Equal(outcomes(got[0]), []string{protocol.AssistantActionAutoApproved, protocol.AssistantActionSucceeded}) {
		t.Fatalf("records = %+v, want auto-approved, then succeeded", got)
	}
	if a := got[0][1]; a.Approval != "auto" || a.ConfirmationID != "" || a.Target.RunID != "run-1" || a.Result != nil || a.IdempotencyKey != "key-1" {
		t.Fatalf("outcome = %+v, want auto approval on run-1 under key-1 and no result", a)
	}
}

func TestRefusedCardsAreRecordedAndNeverRun(t *testing.T) {
	svc, m, _ := newActionService(t)
	sock, fake := serveTools(t, svc, m)
	stop := protocol.RunIDParams{RunID: "run-1"}
	if err := callTool(t, sock, m, protocol.MethodRunKill, stop, true, false); err == nil {
		t.Fatal("a declined stop returned no error")
	}

	m.confirms.ttl = 20 * time.Millisecond
	if err := callTool(t, sock, m, protocol.MethodRunKill, stop, false, false); err == nil {
		t.Fatal("an expired stop returned no error")
	}
	m.confirms.ttl = confirmTTL

	done := make(chan error, 1)
	go func() { done <- coordtransport.Call(t.Context(), sock, protocol.MethodEnvSave, nil, nil) }()
	for len(m.confirms.snapshot()) == 0 {
		time.Sleep(time.Millisecond)
	}
	m.confirms.cancelAll()
	if err := wait(t, done); err == nil {
		t.Fatal("a withdrawn save returned no error")
	}

	if fake.ran(protocol.MethodRunKill) || fake.ran(protocol.MethodEnvSave) {
		t.Fatal("a refused call ran")
	}
	got := actions(t, svc, m)
	want := []string{protocol.AssistantActionDeclined, protocol.AssistantActionExpired, protocol.AssistantActionWithdrawn}
	if len(got) != len(want) {
		t.Fatalf("records = %+v, want %v", got, want)
	}
	for i, records := range got {
		a := records[0]
		if len(records) != 1 || a.Outcome != want[i] || a.ConfirmationID == "" || a.FinishedAt.IsZero() || a.Error != "" {
			t.Fatalf("action %d = %+v, want one %s record with its card", i, records, want[i])
		}
	}
	if got[0][0].Target.RunID != "run-1" {
		t.Fatalf("declined stop = %+v, want run-1 as its target", got[0][0])
	}
}

func TestFailedCallRecordsTheServersError(t *testing.T) {
	svc, m, _ := newActionService(t)
	sock, _ := serveTools(t, svc, m)
	err := callTool(t, sock, m, protocol.MethodRunKill, protocol.RunIDParams{RunID: "run-1"}, true, true)
	const want = "run run-1 is not live: it finished at 12:04"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("the failed stop = %v, want the server's error", err)
	}
	got := actions(t, svc, m)
	if len(got) != 1 || !slices.Equal(outcomes(got[0]), []string{protocol.AssistantActionApproved, protocol.AssistantActionFailed}) {
		t.Fatalf("records = %+v, want approved, then failed", got)
	}
	if a := got[0][1]; a.Error != want || a.Result != nil {
		t.Fatalf("failure = %+v, want the server's error verbatim", a)
	}
}

func TestReversibleCallIsRecordedWithoutApproval(t *testing.T) {
	svc, m, _ := newActionService(t)
	sock, _ := serveTools(t, svc, m)
	if err := callTool(t, sock, m, protocol.MethodRunPause, protocol.RunIDParams{RunID: "run-1"}, false, false); err != nil {
		t.Fatal(err)
	}
	if err := callTool(t, sock, m, protocol.MethodRunGet, protocol.RunIDParams{RunID: "run-1"}, false, false); err != nil {
		t.Fatal(err)
	}
	got := actions(t, svc, m)
	if len(got) != 1 || len(got[0]) != 1 {
		t.Fatalf("records = %+v, want the pause alone", got)
	}
	if a := got[0][0]; a.Outcome != protocol.AssistantActionSucceeded || a.Approval != "" || a.Summary != "Pause run run-1" || a.Target.RunID != "run-1" {
		t.Fatalf("pause = %+v, want it succeeded with no approval", a)
	}
}

func TestCreatedNamesOnlyNewIDs(t *testing.T) {
	target := protocol.AssistantActionTarget{RunID: "run-1"}
	for _, tc := range []struct {
		result string
		want   *protocol.AssistantActionTarget
	}{
		{`{"run":{"id":"run-2","workspace_id":"ws-1"}}`, &protocol.AssistantActionTarget{RunID: "run-2", WorkspaceID: "ws-1"}},
		{`{"run_id":"run-1","pr_url":"https://example.com/pr/4"}`, nil},
		{`{"run":{"id":"run-1"}}`, nil},
		{`null`, nil},
	} {
		got := created(json.RawMessage(tc.result), target)
		if (got == nil) != (tc.want == nil) || got != nil && *got != *tc.want {
			t.Errorf("created(%s) = %+v, want %+v", tc.result, got, tc.want)
		}
	}
}

func TestReversibleCallOnAnotherMembersRunNamesTheOwner(t *testing.T) {
	svc, m, _ := newActionService(t)
	sock, _ := serveTools(t, svc, m)
	if err := callTool(t, sock, m, protocol.MethodRunResume, protocol.RunIDParams{RunID: "run-2"}, false, false); err != nil {
		t.Fatal(err)
	}
	got := actions(t, svc, m)
	if len(got) != 1 || got[0][0].OtherOwner != "Dana" || got[0][0].Summary != "Resume run run-2, owned by Dana" {
		t.Fatalf("records = %+v, want the resume naming Dana as the owner", got)
	}
}

func TestReversibleCallWhoseOwnerCannotBeReadFailsWithoutRunning(t *testing.T) {
	svc, m, _ := newActionService(t)
	sock, fake := serveTools(t, svc, m)
	err := callTool(t, sock, m, protocol.MethodRunPause, protocol.RunIDParams{RunID: "run-gone"}, false, false)
	if err == nil || !strings.Contains(err.Error(), "look up the owner of run run-gone: run run-gone not found") {
		t.Fatalf("pause = %v, want the owner lookup's error", err)
	}
	if fake.ran(protocol.MethodRunPause) {
		t.Fatal("run.pause ran without its owner known")
	}
	got := actions(t, svc, m)
	if len(got) != 1 || got[0][0].Outcome != protocol.AssistantActionFailed || !strings.Contains(got[0][0].Error, "run run-gone not found") {
		t.Fatalf("records = %+v, want the pause recorded as failed with the lookup's error", got)
	}
}

// failingSettings fails reading or writing the assistant settings.
type failingSettings struct {
	*memStore
	read, write error
}

func (f *failingSettings) AssistantSettings(ctx context.Context, id domain.MemberID) (store.AssistantSettings, error) {
	if f.read != nil {
		return store.AssistantSettings{}, f.read
	}
	return f.memStore.AssistantSettings(ctx, id)
}

func (f *failingSettings) PutAssistantSettings(ctx context.Context, id domain.MemberID, st store.AssistantSettings) error {
	if f.write != nil {
		return f.write
	}
	return f.memStore.PutAssistantSettings(ctx, id, st)
}

func TestAnApprovalThatCannotBeReadOrCountedIsRecordedAsFailed(t *testing.T) {
	db := &failingSettings{memStore: newMemStore()}
	svc := New(Config{Dir: t.TempDir(), Store: db})
	t.Cleanup(func() { _ = svc.Close() })
	m := svc.member("m1")
	sock, fake := serveTools(t, svc, m)
	stop := protocol.RunIDParams{RunID: "run-1"}

	db.write = errors.New("database is locked")
	if err := callTool(t, sock, m, protocol.MethodRunKill, stop, true, true); err == nil || !strings.Contains(err.Error(), "database is locked") {
		t.Fatalf("a stop whose approval was not counted = %v, want the store's error", err)
	}
	db.write, db.read = nil, errors.New("disk I/O error")
	if err := callTool(t, sock, m, protocol.MethodRunKill, stop, false, false); err == nil || !strings.Contains(err.Error(), "disk I/O error") {
		t.Fatalf("a stop whose setting was not read = %v, want the store's error", err)
	}
	if fake.ran(protocol.MethodRunKill) {
		t.Fatal("a call whose approval failed ran")
	}
	got := actions(t, svc, m)
	if len(got) != 2 {
		t.Fatalf("records = %+v, want both failures", got)
	}
	for i, want := range []string{"count the approval: database is locked", "read the auto-approval setting: disk I/O error"} {
		a := got[i][0]
		if len(got[i]) != 1 || a.Outcome != protocol.AssistantActionFailed || !strings.Contains(a.Error, want) || a.Target.RunID != "run-1" || a.FinishedAt.IsZero() {
			t.Fatalf("action %d = %+v, want one failed record with %q", i, got[i], want)
		}
	}
	if got[0][0].ConfirmationID == "" || got[1][0].ConfirmationID != "" {
		t.Fatalf("records = %+v, want the card only on the first", got)
	}
}

func TestAResetBetweenApprovalAndCallKeepsItsRecordsOutOfTheNewConversation(t *testing.T) {
	svc, m, _ := newActionService(t)
	c := protocol.AssistantConfirmation{Method: protocol.MethodEnvSave, Summary: "Save your environment", Params: json.RawMessage(`{}`), CreatedAt: time.Now()}
	if err := answerCard(t, svc, m, c, true); err != nil {
		t.Fatal(err)
	}
	m.op.Lock()
	err := svc.resetLocked(t.Context(), m)
	m.op.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	fake := &actionServer{}
	call := actionCaller{Caller: fake, s: svc, m: m}
	if _, err := call.Call(t.Context(), c.Method, c.Params); err != nil {
		t.Fatal(err)
	}
	if got := actions(t, svc, m); len(got) != 0 {
		t.Fatalf("records after the reset = %+v, want none in the new conversation", got)
	}

	if err := answerCard(t, svc, m, c, true); err != nil {
		t.Fatal(err)
	}
	if _, err := call.Call(t.Context(), c.Method, c.Params); err != nil {
		t.Fatal(err)
	}
	got := actions(t, svc, m)
	if len(got) != 1 || !slices.Equal(outcomes(got[0]), []string{protocol.AssistantActionApproved, protocol.AssistantActionSucceeded}) {
		t.Fatalf("records = %+v, want the new conversation's approval, then success", got)
	}
	if _, ok := m.actions.take(c.Method, c.Params); ok {
		t.Fatal("an approval is still held after its call")
	}
}

// resetCaller discards the conversation while a call is in flight.
type resetCaller struct {
	svc *Service
	m   *member
}

func (r resetCaller) Call(ctx context.Context, _ string, _ json.RawMessage) (json.RawMessage, *protocol.Error) {
	r.m.op.Lock()
	defer r.m.op.Unlock()
	if err := r.svc.resetLocked(ctx, r.m); err != nil {
		return nil, &protocol.Error{Code: protocol.CodeInternal, Message: err.Error()}
	}
	return json.RawMessage(`{}`), nil
}

func TestRecordsOfADiscardedConversationAreDropped(t *testing.T) {
	svc, m, _ := newActionService(t)
	done := make(chan error, 1)
	go func() {
		done <- svc.approver(m)(t.Context(), protocol.AssistantConfirmation{Method: protocol.MethodRunKill, Summary: "Stop run run-1", CreatedAt: time.Now()})
	}()
	for len(m.confirms.snapshot()) == 0 {
		time.Sleep(time.Millisecond)
	}
	m.op.Lock()
	err := svc.resetLocked(t.Context(), m)
	m.op.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	m.confirms.cancelAll()
	if err := wait(t, done); !errors.Is(err, errSessionEnded) {
		t.Fatalf("withdrawn card = %v, want only the withdrawal", err)
	}
	if got := actions(t, svc, m); len(got) != 0 {
		t.Fatalf("records after the reset = %+v, want none in the new conversation", got)
	}

	call := actionCaller{Caller: resetCaller{svc: svc, m: m}, s: svc, m: m}
	if _, err := call.Call(t.Context(), protocol.MethodRunPause, json.RawMessage(`{"run_id":"run-1"}`)); err != nil {
		t.Fatal(err)
	}
	if got := actions(t, svc, m); len(got) != 0 {
		t.Fatalf("records after a reset during the call = %+v, want none", got)
	}
	after := actionCaller{Caller: &actionServer{}, s: svc, m: m}
	if _, err := after.Call(t.Context(), protocol.MethodRunPause, json.RawMessage(`{"run_id":"run-1"}`)); err != nil {
		t.Fatal(err)
	}
	if got := actions(t, svc, m); len(got) != 1 {
		t.Fatalf("records = %+v, want the pause made in the new conversation", got)
	}
}
