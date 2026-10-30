package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/coordtransport"
	"github.com/3xDevOps/Aether/internal/protocol"
)

type fakeCaller struct {
	mu     sync.Mutex
	calls  []string
	params map[string]json.RawMessage
}

func (f *fakeCaller) Call(_ context.Context, method string, params json.RawMessage) (json.RawMessage, *protocol.Error) {
	f.mu.Lock()
	f.calls = append(f.calls, method)
	if f.params == nil {
		f.params = map[string]json.RawMessage{}
	}
	f.params[method] = params
	f.mu.Unlock()
	switch method {
	case protocol.MethodRunGet:
		return json.RawMessage(`{"run":{"id":"run-1","title":"Fix login","task":"t","member_id":"m2"}}`), nil
	case protocol.MethodRunList:
		return json.RawMessage(`{"runs":[]}`), nil
	case protocol.MethodWorkspaceGet:
		return json.RawMessage(`{"workspace":{"id":"ws-1","name":"shop"}}`), nil
	case protocol.MethodMemberList:
		return json.RawMessage(`{"members":[{"id":"m1","display_name":"Ada"},{"id":"m2","display_name":"Grace"}]}`), nil
	case protocol.MethodAccountList:
		return json.RawMessage(`{"accounts":[{"id":"m1","display_name":"Ada"},{"id":"m2","display_name":"Grace"}]}`), nil
	case protocol.MethodWorkspaceList:
		return json.RawMessage(`{"workspaces":[{"id":"ws-1","name":"shop"},{"id":"ws-2","name":"web"}]}`), nil
	case protocol.MethodTemplateList:
		return json.RawMessage(`{"templates":[{"name":"triage","harness":"claude","mode":"acp","model":"opus","task":"triage issue {{issue}}\nthen label it","version":"v1"}]}`), nil
	}
	return json.RawMessage(`{}`), nil
}

func (f *fakeCaller) called() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func start(t *testing.T, confirm Confirmer) (string, *fakeCaller) {
	t.Helper()
	return startAs(t, "m1", confirm)
}

func startAs(t *testing.T, member string, confirm Confirmer) (string, *fakeCaller) {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "a.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	fc := &fakeCaller{}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- (&Server{Caller: fc, Confirm: confirm, Member: member}).Serve(ctx, ln) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Serve: %v", err)
		}
	})
	return sock, fc
}

func rpcCode(t *testing.T, err error) int {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error")
	}
	return coordtransport.ErrorCode(err)
}

func TestAllowlistAndMemberID(t *testing.T) {
	sock, fc := start(t, nil)
	err := coordtransport.Call(t.Context(), sock, protocol.MethodMemberInvitationCreate, nil, nil)
	if rpcCode(t, err) != protocol.CodeMethodNotFound || !strings.Contains(err.Error(), "run.launch") {
		t.Fatalf("member.invitation.create: %v", err)
	}
	for _, method := range []string{protocol.MethodAssistantSettings, protocol.MethodAssistantConfirm, protocol.MethodMemoryRm} {
		err = coordtransport.Call(t.Context(), sock, method, map[string]any{"auto_approve": true, "confirmation_id": "c", "approve": true}, nil)
		if rpcCode(t, err) != protocol.CodeMethodNotFound {
			t.Fatalf("%s on the tool socket: %v", method, err)
		}
	}
	for _, params := range []map[string]any{{"run_id": "run-1", "member_id": "m2"}, {"run_id": "run-1", "account_member_id": "m2"}} {
		if rpcCode(t, coordtransport.Call(t.Context(), sock, protocol.MethodRunGet, params, nil)) != protocol.CodeInvalidParams {
			t.Fatalf("%v accepted", params)
		}
	}
	err = coordtransport.Call(t.Context(), sock, protocol.MethodRunList, map[string]any{"account_member_id": "m2"}, nil)
	if rpcCode(t, err) != protocol.CodeInvalidParams {
		t.Fatalf("account_member_id on run.list: %v", err)
	}
	if got := fc.called(); len(got) != 0 {
		t.Fatalf("caller reached: %v", got)
	}
}

func TestReadPassesThrough(t *testing.T) {
	sock, fc := start(t, nil)
	var out protocol.RunListResult
	if err := coordtransport.Call(t.Context(), sock, protocol.MethodRunList, protocol.RunListParams{ActiveOnly: true}, &out); err != nil {
		t.Fatal(err)
	}
	if got := fc.called(); len(got) != 1 || got[0] != protocol.MethodRunList {
		t.Fatalf("calls: %v", got)
	}
}

func TestConfirmedApproveAndDecline(t *testing.T) {
	var got protocol.AssistantConfirmation
	var verdict error
	sock, fc := startAs(t, "m2", func(_ context.Context, c protocol.AssistantConfirmation) error {
		got = c
		return verdict
	})
	params := protocol.RunIDParams{RunID: "run-1"}

	verdict = errors.New("declined by the member")
	err := coordtransport.Call(t.Context(), sock, protocol.MethodRunKill, params, nil)
	if rpcCode(t, err) != protocol.CodeDenied || !strings.Contains(err.Error(), "declined by the member") {
		t.Fatalf("decline: %v", err)
	}
	if got.Summary != `Stop run run-1 ("Fix login")` || got.Method != protocol.MethodRunKill {
		t.Fatalf("confirmation: %+v", got)
	}
	for _, m := range fc.called() {
		if m == protocol.MethodRunKill {
			t.Fatal("run.kill ran after a decline")
		}
	}

	verdict = nil
	if err := coordtransport.Call(t.Context(), sock, protocol.MethodRunKill, params, nil); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if calls := fc.called(); calls[len(calls)-1] != protocol.MethodRunKill {
		t.Fatalf("calls: %v", calls)
	}

	if err := coordtransport.Call(t.Context(), sock, protocol.MethodEnvSave, nil, nil); err != nil {
		t.Fatalf("env.save: %v", err)
	}
	if !strings.HasPrefix(got.Summary, "Save your environment") {
		t.Fatalf("env.save summary: %q", got.Summary)
	}
}

func TestDocs(t *testing.T) {
	sock, _ := start(t, nil)
	var list protocol.AssistantDocsResult
	if err := coordtransport.Call(t.Context(), sock, protocol.MethodAssistantDocs, nil, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Topics) == 0 || strings.HasSuffix(list.Topics[0], ".md") {
		t.Fatalf("topics: %v", list.Topics)
	}
	var topic protocol.AssistantDocsResult
	if err := coordtransport.Call(t.Context(), sock, protocol.MethodAssistantDocs, protocol.AssistantDocsParams{Topic: "quickstart"}, &topic); err != nil {
		t.Fatal(err)
	}
	if topic.Text == "" {
		t.Fatal("empty quickstart")
	}
	err := coordtransport.Call(t.Context(), sock, protocol.MethodAssistantDocs, protocol.AssistantDocsParams{Topic: "nope"}, nil)
	if rpcCode(t, err) != protocol.CodeNotFound || !strings.Contains(err.Error(), "quickstart") {
		t.Fatalf("unknown topic: %v", err)
	}
}

func TestRunListFilters(t *testing.T) {
	sock, fc := start(t, nil)
	if err := coordtransport.Call(t.Context(), sock, protocol.MethodRunList, map[string]any{"member_id": "m2", "active_only": true}, nil); err != nil {
		t.Fatal(err)
	}
	if got := string(fc.params[protocol.MethodRunList]); !strings.Contains(got, `"member_id":"m2"`) {
		t.Fatalf("explicit filter: %s", got)
	}
	if err := coordtransport.Call(t.Context(), sock, protocol.MethodRunList, map[string]any{"mine": true}, nil); err != nil {
		t.Fatal(err)
	}
	if got := string(fc.params[protocol.MethodRunList]); !strings.Contains(got, `"member_id":"m1"`) || strings.Contains(got, "mine") {
		t.Fatalf("mine: %s", got)
	}
}

func TestConfirmedCallsNameAnotherMembersRun(t *testing.T) {
	var got protocol.AssistantConfirmation
	sock, fc := start(t, func(_ context.Context, c protocol.AssistantConfirmation) error {
		got = c
		return errors.New("no")
	})
	task := strings.Repeat("x", 200)
	err := coordtransport.Call(t.Context(), sock, protocol.MethodRunLaunch, protocol.RunLaunchParams{WorkspaceID: "ws-1", Harness: "claude", Mode: "acp", Task: task}, nil)
	if rpcCode(t, err) != protocol.CodeDenied {
		t.Fatalf("launch: %v", err)
	}
	if want := `Launch claude (acp) in shop, may fall back to accounts shared with you: "` + strings.Repeat("x", 120) + `…"`; got.Summary != want {
		t.Fatalf("launch summary %q, want %q", got.Summary, want)
	}
	err = coordtransport.Call(t.Context(), sock, protocol.MethodRunInject, protocol.RunInjectParams{RunID: "run-1", Message: "hi"}, nil)
	if rpcCode(t, err) != protocol.CodeDenied {
		t.Fatalf("inject: %v", err)
	}
	if want := `Send to run run-1 ("Fix login"), owned by Grace: "hi"`; got.Summary != want || !got.OtherMember || got.OtherOwner != "Grace" {
		t.Fatalf("inject summary %q, owner %q, want %q by Grace", got.Summary, got.OtherOwner, want)
	}
	err = coordtransport.Call(t.Context(), sock, protocol.MethodRunKill, protocol.RunIDParams{RunID: "run-1"}, nil)
	if rpcCode(t, err) != protocol.CodeDenied {
		t.Fatalf("kill: %v", err)
	}
	if want := `Stop run run-1 ("Fix login"), owned by Grace`; got.Summary != want || !got.OtherMember || got.OtherOwner != "Grace" {
		t.Fatalf("kill summary %q, owner %q, want %q by Grace", got.Summary, got.OtherOwner, want)
	}
	for _, m := range fc.called() {
		if m == protocol.MethodRunLaunch || m == protocol.MethodRunInject {
			t.Fatalf("%s ran after a decline", m)
		}
	}
	sock, _ = startAs(t, "m2", func(_ context.Context, c protocol.AssistantConfirmation) error {
		got = c
		return errors.New("no")
	})
	_ = coordtransport.Call(t.Context(), sock, protocol.MethodRunInject, protocol.RunInjectParams{RunID: "run-1", Message: "hi"}, nil)
	if want := `Send to run run-1 ("Fix login"): "hi"`; got.Summary != want || got.OtherMember || got.OtherOwner != "" {
		t.Fatalf("own run summary %q, owner %q, want %q and no other owner", got.Summary, got.OtherOwner, want)
	}
}

func TestDisconnectWithdrawsConfirmation(t *testing.T) {
	asked := make(chan struct{})
	withdrawn := make(chan error, 1)
	sock, fc := start(t, func(ctx context.Context, _ protocol.AssistantConfirmation) error {
		close(asked)
		<-ctx.Done()
		withdrawn <- ctx.Err()
		return ctx.Err()
	})
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	req, _ := json.Marshal(protocol.Request{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: protocol.MethodEnvSave})
	if _, err := conn.Write(append(req, '\n')); err != nil {
		t.Fatal(err)
	}
	<-asked
	_ = conn.Close()
	select {
	case err := <-withdrawn:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("withdrawn with %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the confirmation outlived its caller")
	}
	for _, m := range fc.called() {
		if m == protocol.MethodEnvSave {
			t.Fatal("env.save ran for a caller that left")
		}
	}
}

type launchCaller struct {
	fakeCaller
	fail map[string]bool
}

func (c *launchCaller) Call(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, *protocol.Error) {
	if c.fail[method] {
		return nil, &protocol.Error{Code: protocol.CodeInternal, Message: "boom"}
	}
	switch method {
	case protocol.MethodRunLaunch:
		return json.RawMessage(`{"run":{"id":"run-9"}}`), nil
	case protocol.MethodRunRelaunch:
		return json.RawMessage(`{"run":{"id":"run-1"}}`), nil
	}
	return c.fakeCaller.Call(ctx, method, params)
}

func TestFollowIsToldOnlyOfSuccessfulLaunchesAndMessages(t *testing.T) {
	var followed []string
	var verdict error
	caller := &launchCaller{fail: map[string]bool{}}
	srv := &Server{
		Caller: caller, Member: "m1",
		Confirm: func(context.Context, protocol.AssistantConfirmation) error { return verdict },
		Follow:  func(_ context.Context, run string) { followed = append(followed, run) },
	}
	call := func(method string, params any) *protocol.Error {
		raw, _ := json.Marshal(params)
		_, perr := srv.dispatch(t.Context(), protocol.Request{Method: method, Params: raw})
		return perr
	}
	launch := protocol.RunLaunchParams{WorkspaceID: "ws-1", Harness: "claude", Task: "t"}
	inject := protocol.RunInjectParams{RunID: "run-1", Message: "hi"}

	verdict = errors.New("no")
	_ = call(protocol.MethodRunLaunch, launch)
	_ = call(protocol.MethodRunInject, inject)
	caller.fail[protocol.MethodRunLaunch], caller.fail[protocol.MethodRunInject] = true, true
	verdict = nil
	_ = call(protocol.MethodRunLaunch, launch)
	_ = call(protocol.MethodRunInject, inject)
	if len(followed) != 0 {
		t.Fatalf("followed %v after declines and errors, want none", followed)
	}

	caller.fail = map[string]bool{}
	if perr := call(protocol.MethodRunLaunch, launch); perr != nil {
		t.Fatal(perr)
	}
	if perr := call(protocol.MethodRunInject, inject); perr != nil {
		t.Fatal(perr)
	}
	if perr := call(protocol.MethodRunRelaunch, protocol.RunIDParams{RunID: "run-1"}); perr != nil {
		t.Fatal(perr)
	}
	_ = call(protocol.MethodRunPause, protocol.RunIDParams{RunID: "run-1"})
	if want := []string{"run-9", "run-1", "run-1"}; !slices.Equal(followed, want) {
		t.Fatalf("followed %v, want %v", followed, want)
	}
}

func TestViewContextIsServedFromTheMembersOwnSnapshot(t *testing.T) {
	fc := &fakeCaller{}
	view := &protocol.AssistantViewContext{Version: 1, Route: protocol.AssistantViewEntry{Name: "run", RunID: "run-1"}}
	s := &Server{Caller: fc, Member: "m1", View: func() protocol.AssistantViewContextResult {
		return protocol.AssistantViewContextResult{ViewContext: view, Connected: true}
	}}
	raw, perr := s.dispatch(t.Context(), protocol.Request{Method: protocol.MethodAssistantViewContext})
	var got protocol.AssistantViewContextResult
	if perr != nil || json.Unmarshal(raw, &got) != nil || got.ViewContext.Route.RunID != "run-1" || !got.Connected {
		t.Fatalf("assistant.view_context = %s, %v", raw, perr)
	}
	if calls := fc.called(); len(calls) != 0 {
		t.Fatalf("assistant.view_context reached the control surface: %v", calls)
	}
}

// Prepare passes exactly the account fields through and refuses every other
// member id, which would make the call act for or on another member.
func TestPrepareAccountFields(t *testing.T) {
	for _, c := range []struct {
		method, params string
		ok             bool
	}{
		{protocol.MethodRunLaunch, `{"account_member_id":"m2","fallback_account_member_ids":["m3"]}`, true},
		{protocol.MethodAgentList, `{"account_member_id":"m2"}`, true},
		{protocol.MethodAgentModels, `{"name":"claude","account_member_id":"m2"}`, true},
		{protocol.MethodAccountUsage, `{"account_member_id":"m2"}`, true},
		{protocol.MethodRunList, `{"member_id":"m2"}`, true},
		{protocol.MethodRunLaunch, `{"member_id":"m2"}`, false},
		{protocol.MethodRunGet, `{"account_member_id":"m2"}`, false},
		{protocol.MethodRunList, `{"account_member_id":"m2"}`, false},
		{protocol.MethodTemplateLaunch, `{"account_member_id":"m2"}`, false},
		{protocol.MethodRunLaunch, `{"to_member_id":"m2"}`, false},
	} {
		_, err := Prepare("m1", c.method, json.RawMessage(c.params))
		if (err == nil) != c.ok {
			t.Errorf("%s %s = %v, want ok %v", c.method, c.params, err, c.ok)
		}
	}
}

// A launch card quotes the task's first line, unescaped.
func TestLaunchCardQuotesTheTaskFirstLine(t *testing.T) {
	var card string
	s := &Server{Caller: &fakeCaller{}, Member: "m1", Confirm: func(_ context.Context, c protocol.AssistantConfirmation) error {
		card = c.Summary
		return nil
	}}
	raw, _ := json.Marshal(protocol.RunLaunchParams{WorkspaceID: "ws-1", Harness: "claude", Mode: "acp", NoFallback: true,
		Task: "Fix the \"login\" bug\n\nSteps:\n1. reproduce"})
	if _, perr := s.dispatch(t.Context(), protocol.Request{Method: protocol.MethodRunLaunch, Params: raw}); perr != nil {
		t.Fatal(perr)
	}
	if want := `Launch claude (acp) in shop, no fallback: "Fix the "login" bug…"`; card != want {
		t.Fatalf("run.launch card = %q, want %q", card, want)
	}
}

// A template launch's card shows the whole task its params fill in and the
// assistant's reason as their own fields, neither sent to the server, and
// the approved launch is pinned to the version the card showed; a launch
// pinned to an older version gets no card.
func TestTemplateLaunchIsPinnedToItsCard(t *testing.T) {
	fc := &fakeCaller{}
	var cards []string
	var card protocol.AssistantConfirmation
	s := &Server{Caller: fc, Member: "m1", Confirm: func(_ context.Context, c protocol.AssistantConfirmation) error {
		cards, card = append(cards, c.Summary), c
		return nil
	}}
	launch := func(method string, params any) *protocol.Error {
		raw, _ := json.Marshal(params)
		_, perr := s.dispatch(t.Context(), protocol.Request{Method: method, Params: raw})
		return perr
	}
	if perr := launch(protocol.MethodTemplateLaunch, map[string]any{"workspace_id": "ws-1", "name": "triage", "params": map[string]string{"issue": "42"},
		"reason": "the member asked to triage 42"}); perr != nil {
		t.Fatal(perr)
	}
	sent, err := protocol.DecodeParams[protocol.TemplateLaunchParams](fc.params[protocol.MethodTemplateLaunch])
	if err != nil || sent.ExpectedVersion != "v1" || sent.Params["issue"] != "42" || !strings.Contains(cards[0], `: "triage issue 42…"`) {
		t.Fatalf("template.launch sent %+v (%v) after card %q, want pinned to v1", sent, err, cards[0])
	}
	if card.Task != "triage issue 42\nthen label it" || card.Reason != "the member asked to triage 42" {
		t.Fatalf("template.launch card task %q, reason %q; want the whole rendered task and the reason", card.Task, card.Reason)
	}
	if raw := string(fc.params[protocol.MethodTemplateLaunch]); strings.Contains(raw, "then label it") || strings.Contains(raw, "reason") {
		t.Fatalf("template.launch reached the server as %s, want neither the card's task nor its reason", raw)
	}
	delete(fc.params, protocol.MethodTemplateLaunch)
	perr := launch(protocol.MethodTemplateLaunch, protocol.TemplateLaunchParams{WorkspaceID: "ws-1", Name: "triage", Params: map[string]string{"issue": "42"},
		ExpectedVersion: "v0"})
	if perr == nil || perr.Code != protocol.CodeConflict || len(cards) != 1 || fc.params[protocol.MethodTemplateLaunch] != nil {
		t.Fatalf("a stale template launch = %v with %d cards, want a conflict and no card", perr, len(cards))
	}
}

// replayCaller answers a replay_only template.launch with the run key
// "k-done" started and refuses any other key.
type replayCaller struct {
	fakeCaller
	launches []string
}

func (c *replayCaller) Call(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, *protocol.Error) {
	if method != protocol.MethodTemplateLaunch {
		return c.fakeCaller.Call(ctx, method, params)
	}
	p, _ := protocol.DecodeParams[protocol.TemplateLaunchParams](params)
	c.launches = append(c.launches, fmt.Sprintf("%s replay=%t", p.IdempotencyKey, p.ReplayOnly))
	switch {
	case p.ReplayOnly && p.IdempotencyKey == "k-done":
		return json.RawMessage(`{"run":{"id":"run-first"},"replayed":true}`), nil
	case p.ReplayOnly:
		return nil, &protocol.Error{Code: protocol.CodeNotFound, Message: "template.launch: no run was launched under idempotency_key"}
	}
	return json.RawMessage(`{"run":{"id":"run-new"}}`), nil
}

// A keyed template launch whose run already started returns it without a
// card, even once the template changed; a new key still gets the card and
// its version check.
func TestTemplateLaunchRetryReplaysBeforeTheCard(t *testing.T) {
	caller := &replayCaller{}
	var cards []string
	var followed []string
	s := &Server{Caller: caller, Member: "m1", Confirm: func(_ context.Context, c protocol.AssistantConfirmation) error {
		cards = append(cards, c.Summary)
		return nil
	}, Follow: func(_ context.Context, run string) { followed = append(followed, run) }}
	launch := func(p protocol.TemplateLaunchParams) (json.RawMessage, *protocol.Error) {
		raw, _ := json.Marshal(p)
		return s.dispatch(t.Context(), protocol.Request{Method: protocol.MethodTemplateLaunch, Params: raw})
	}
	got, perr := launch(protocol.TemplateLaunchParams{WorkspaceID: "ws-1", Name: "triage", Params: map[string]string{"issue": "42"}, ExpectedVersion: "v0",
		IdempotencyKey: "k-done"})
	if perr != nil || !strings.Contains(string(got), "run-first") || len(cards) != 0 || !slices.Equal(followed, []string{"run-first"}) {
		t.Fatalf("retry of a stale launch = %s, %v with %d cards, followed %v; want the first run, no card", got, perr, len(cards), followed)
	}
	if _, perr = launch(protocol.TemplateLaunchParams{WorkspaceID: "ws-1", Name: "triage", Params: map[string]string{"issue": "42"}, ExpectedVersion: "v0",
		IdempotencyKey: "k-new"}); perr == nil || perr.Code != protocol.CodeConflict || len(cards) != 0 {
		t.Fatalf("a new launch of a changed template = %v with %d cards, want a conflict and no card", perr, len(cards))
	}
	got, perr = launch(protocol.TemplateLaunchParams{WorkspaceID: "ws-1", Name: "triage", Params: map[string]string{"issue": "42"}, IdempotencyKey: "k-new"})
	if perr != nil || !strings.Contains(string(got), "run-new") || len(cards) != 1 ||
		!slices.Equal(caller.launches, []string{"k-done replay=true", "k-new replay=true", "k-new replay=true", "k-new replay=false"}) {
		t.Fatalf("a new launch = %s, %v with %d cards, launches %v; want one card, then the launch", got, perr, len(cards), caller.launches)
	}
}
