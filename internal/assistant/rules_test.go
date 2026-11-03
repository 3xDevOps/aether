package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/coordtransport"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

// ruleServer is actionServer with a teammate's account shared with m1 and
// workspaces that may be gone.
type ruleServer struct {
	actionServer
	gone map[string]bool
}

func (f *ruleServer) Call(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, *protocol.Error) {
	switch method {
	case protocol.MethodAccountList:
		return json.RawMessage(`{"accounts":[{"id":"m1","display_name":"Ari"},{"id":"m2","display_name":"Dana"}]}`), nil
	case protocol.MethodWorkspaceGet:
		var p protocol.WorkspaceGetParams
		_ = json.Unmarshal(params, &p)
		if f.gone[p.WorkspaceID] {
			return nil, &protocol.Error{Code: protocol.CodeNotFound, Message: "workspace " + p.WorkspaceID + " not found"}
		}
	}
	return f.actionServer.Call(ctx, method, params)
}

// card makes a tool call and returns the card it shows, leaving it open;
// done receives the call's result.
func card(t *testing.T, sock string, m *member, method string, params any) (protocol.AssistantConfirmation, <-chan error) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- coordtransport.Call(t.Context(), sock, method, params, nil) }()
	deadline := time.Now().Add(5 * time.Second)
	for len(m.confirms.snapshot()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("no card was shown")
		}
		time.Sleep(time.Millisecond)
	}
	return m.confirms.snapshot()[0], done
}

func newRuleService(t *testing.T) (*Service, *member, *memStore, string, *ruleServer) {
	t.Helper()
	svc, m, db := newActionService(t)
	fake := &ruleServer{gone: map[string]bool{}}
	return svc, m, db, serveCaller(t, svc, m, fake), fake
}

func TestAlwaysApprovePassesTheNextLaunchOfItsKindOnly(t *testing.T) {
	svc, m, db, sock, _ := newRuleService(t)
	launch := protocol.RunLaunchParams{WorkspaceID: "ws-1", Harness: "claude", Mode: "acp", NoFallback: true, Task: "fix the login redirect"}
	c, done := card(t, sock, m, protocol.MethodRunLaunch, launch)
	want := "Always approve launches of Claude Code (acp) in api on your account, permission default, no fallback"
	if c.Rule == nil || c.Rule.Wording != want || c.Rule.Suggested {
		t.Fatalf("card rule = %+v, want %q", c.Rule, want)
	}
	rule, err := svc.ApproveAlways(t.Context(), "m1", c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := wait(t, done); err != nil {
		t.Fatalf("the card approved with always = %v", err)
	}
	if rule.Wording != want || rule.Summary != c.Summary || rule.Method != protocol.MethodRunLaunch {
		t.Fatalf("saved rule = %+v, want the card's wording and summary", rule)
	}

	launch.Task = "a different task, still the same kind"
	if err := callTool(t, sock, m, protocol.MethodRunLaunch, launch, false, false); err != nil {
		t.Fatalf("launch the rule covers = %v", err)
	}
	b, _ := os.ReadFile(svc.logPath("m1"))
	if !strings.Contains(string(b), `"title":"Auto-approved by rule `+rule.ID+`: Launch claude (acp) in api, no fallback: \"a different task`) {
		t.Fatalf("transcript = %s, want the pass by rule", b)
	}
	got := actions(t, svc, m)
	if a := got[len(got)-1][0]; a.Approval != "rule" || a.RuleID != rule.ID || a.Outcome != protocol.AssistantActionAutoApproved {
		t.Fatalf("pass record = %+v, want approval by rule %s", a, rule.ID)
	}
	if db.rules[0].Uses != 1 || db.rules[0].LastUsedAt.IsZero() {
		t.Fatalf("rule after one pass = %+v, want one use", db.rules[0])
	}

	for name, other := range map[string]protocol.RunLaunchParams{
		"another mode":       {WorkspaceID: "ws-1", Harness: "claude", Mode: "tui", NoFallback: true, Task: "x"},
		"another agent":      {WorkspaceID: "ws-1", Harness: "codex", Mode: "acp", NoFallback: true, Task: "x"},
		"a teammate account": {WorkspaceID: "ws-1", Harness: "claude", Mode: "acp", AccountMemberID: "m2", NoFallback: true, Task: "x"},
		"a model":            {WorkspaceID: "ws-1", Harness: "claude", Mode: "acp", Model: "opus", NoFallback: true, Task: "x"},
		"root":               {WorkspaceID: "ws-1", Harness: "claude", Mode: "acp", Permission: "root", NoFallback: true, Task: "x"},
		"another workspace":  {WorkspaceID: "ws-2", Harness: "claude", Mode: "acp", NoFallback: true, Task: "x"},
	} {
		c, done := card(t, sock, m, protocol.MethodRunLaunch, other)
		if c.Rule == nil {
			t.Errorf("%s: card offers no rule", name)
		}
		if err := svc.Confirm("m1", c.ID, false); err != nil {
			t.Fatal(err)
		}
		_ = wait(t, done)
	}

	if err := svc.RevokeRule(t.Context(), "m1", rule.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.RevokeRule(t.Context(), "m1", rule.ID); !errors.Is(err, ErrNoSuchRule) {
		t.Fatalf("revoking twice = %v, want ErrNoSuchRule", err)
	}
	if err := callTool(t, sock, m, protocol.MethodRunLaunch, launch, true, true); err != nil {
		t.Fatalf("launch after the revoke = %v, want its card back", err)
	}
}

func TestRuleWordingNamesEveryKeyField(t *testing.T) {
	svc, m, _, sock, _ := newRuleService(t)
	for params, want := range map[*protocol.RunLaunchParams]string{
		{WorkspaceID: "ws-1", Harness: "codex", Model: "gpt-5", Effort: "high", Permission: "isolated", AccountMemberID: "m2", NoFallback: true, Task: "x"}: "Always approve launches of Codex (default mode, model gpt-5, effort high) in api on Dana's account, permission isolated, no fallback",
		{WorkspaceID: "ws-1", Harness: "claude", AccountMemberID: "m1", NoFallback: true, Task: "x"}:                                                        "Always approve launches of Claude Code (default mode) in api on your account, permission default, no fallback",
	} {
		c, done := card(t, sock, m, protocol.MethodRunLaunch, params)
		if c.Rule == nil || c.Rule.Wording != want {
			t.Errorf("rule = %+v, want %q", c.Rule, want)
		}
		if params.Model != "" && !strings.Contains(c.Summary, "model gpt-5, effort high") {
			t.Errorf("card %q does not show the model and effort the rule names", c.Summary)
		}
		_ = svc.Confirm("m1", c.ID, false)
		_ = wait(t, done)
	}
	for method, want := range map[string]string{
		protocol.MethodRunInject: "Always approve messages to your own sessions in api",
		protocol.MethodRunKill:   "Always approve stopping your own sessions in api",
		protocol.MethodEnvSave:   "Always approve saving your environment as a new image",
	} {
		params := any(protocol.RunInjectParams{RunID: "run-1", Message: "hi"})
		if method == protocol.MethodEnvSave {
			params = struct{}{}
		}
		c, done := card(t, sock, m, method, params)
		if c.Rule == nil || c.Rule.Wording != want {
			t.Errorf("%s rule = %+v, want %q", method, c.Rule, want)
		}
		_ = svc.Confirm("m1", c.ID, false)
		_ = wait(t, done)
	}
}

// Cards that act on another member's session or may spend accounts nobody
// chose, and launches wider than the card's key, offer no rule.
func TestNeverRuleableCardsOfferNoRule(t *testing.T) {
	svc, m, _, sock, _ := newRuleService(t)
	for name, call := range map[string]struct {
		method string
		params any
	}{
		"message another's session":  {protocol.MethodRunInject, protocol.RunInjectParams{RunID: "run-2", Message: "hi"}},
		"stop another's session":     {protocol.MethodRunKill, protocol.RunIDParams{RunID: "run-2"}},
		"automatic fallback, shared": {protocol.MethodRunLaunch, protocol.RunLaunchParams{WorkspaceID: "ws-1", Harness: "claude", Task: "x"}},
		"a fallback order":           {protocol.MethodRunLaunch, protocol.RunLaunchParams{WorkspaceID: "ws-1", Harness: "claude", FallbackAccountMemberIDs: []string{"m2"}, Task: "x"}},
		"extra repositories":         {protocol.MethodRunLaunch, protocol.RunLaunchParams{WorkspaceID: "ws-1", Harness: "claude", NoFallback: true, ExtraWorkspaceIDs: []string{"ws-2"}, Task: "x"}},
		"subsessions":                {protocol.MethodRunLaunch, protocol.RunLaunchParams{WorkspaceID: "ws-1", Harness: "claude", NoFallback: true, Children: "on", Task: "x"}},
		"limits":                     {protocol.MethodRunLaunch, protocol.RunLaunchParams{WorkspaceID: "ws-1", Harness: "claude", NoFallback: true, Limits: &protocol.RunLimits{Pids: 64}, Task: "x"}},
		"close":                      {protocol.MethodRunClose, protocol.RunCloseParams{RunID: "run-1", Outcome: "merged"}},
		"handoff":                    {protocol.MethodRunHandoff, protocol.RunHandoffParams{RunID: "run-1", ToMemberID: "m2"}},
		"memory pin":                 {protocol.MethodMemoryPin, protocol.MemoryPinParams{WorkspaceID: "ws-1", ID: "note-1"}},
	} {
		c, done := card(t, sock, m, call.method, call.params)
		if c.Rule != nil {
			t.Errorf("%s: card offers %+v, want no rule", name, c.Rule)
		}
		if _, err := svc.ApproveAlways(t.Context(), "m1", c.ID); !errors.Is(err, ErrNoRule) {
			t.Errorf("%s: always = %v, want ErrNoRule", name, err)
		}
		_ = svc.Confirm("m1", c.ID, false)
		_ = wait(t, done)
	}
	if _, err := svc.ApproveAlways(t.Context(), "m1", "no-such-card"); !errors.Is(err, ErrNoConfirmation) {
		t.Fatalf("always on a missing card = %v, want ErrNoConfirmation", err)
	}
}

// Every method the tool socket confirms is rule-able exactly when it is one
// of run.launch, run.inject, run.kill and env.save: today's auto-approval
// list. Nothing else, template.launch included, may become a rule.
func TestOnlyTodaysAutoApprovedMethodsAreRuleable(t *testing.T) {
	svc, m, _, sock, _ := newRuleService(t)
	err := coordtransport.Call(t.Context(), sock, "no.such.method", nil, nil)
	_, list, ok := strings.Cut(err.Error(), "allowed methods: ")
	if !ok {
		t.Fatalf("unknown method = %v, want the allowed list", err)
	}
	ruleable := []string{protocol.MethodRunLaunch, protocol.MethodRunInject, protocol.MethodRunKill, protocol.MethodEnvSave}
	params := json.RawMessage(`{"workspace_id":"ws-1","harness":"claude","no_fallback":true,"run_id":"run-1","name":"t","id":"x","watch_id":"w"}`)
	launch := json.RawMessage(`{"workspace_id":"ws-1","harness":"claude","no_fallback":true}`)
	methods := strings.Split(strings.TrimSpace(list), ", ")
	for _, method := range ruleable {
		if !slices.Contains(methods, method) {
			t.Errorf("%s is not a tool socket method", method)
		}
	}
	for _, method := range methods {
		p := params
		if method == protocol.MethodRunLaunch {
			p = launch
		}
		_, got := svc.ruleKey(t.Context(), m, protocol.AssistantConfirmation{Method: method, Params: p})
		if want := slices.Contains(ruleable, method); got != want {
			t.Errorf("%s rule-able = %v, want %v", method, got, want)
		}
	}
	for _, method := range []string{protocol.MethodAssistantRules, protocol.MethodAssistantRuleRevoke, protocol.MethodAssistantConfirm, protocol.MethodAssistantSettings} {
		if slices.Contains(methods, method) {
			t.Errorf("the tool socket serves %s", method)
		}
	}
}

func TestRuleWhoseWorkspaceIsGoneIsInactive(t *testing.T) {
	svc, m, db, sock, fake := newRuleService(t)
	launch := protocol.RunLaunchParams{WorkspaceID: "ws-1", Harness: "claude", NoFallback: true, Task: "x"}
	c, done := card(t, sock, m, protocol.MethodRunLaunch, launch)
	if _, err := svc.ApproveAlways(t.Context(), "m1", c.ID); err != nil {
		t.Fatal(err)
	}
	_ = wait(t, done)
	shared, err := db.AddAssistantRule(t.Context(), "m1", store.AssistantRuleKey{Method: protocol.MethodRunLaunch, WorkspaceID: "ws-1", Agent: "claude", AccountMemberID: "m9", Permission: "default", NoFallback: true}, "w", "s")
	if err != nil {
		t.Fatal(err)
	}
	fake.gone["ws-1"] = true
	rules, err := svc.Rules(t.Context(), "m1")
	if err != nil || len(rules) != 2 {
		t.Fatalf("rules = %+v, %v", rules, err)
	}
	if !strings.Contains(rules[0].Inactive, "workspace ws-1 no longer exists") {
		t.Fatalf("rule of a removed workspace = %+v, want inactive", rules[0])
	}
	delete(fake.gone, "ws-1")
	rules, _ = svc.Rules(t.Context(), "m1")
	if rules[0].Inactive != "" || rules[1].ID != shared.ID || rules[1].Inactive != "account m9 is no longer shared with you" {
		t.Fatalf("rules = %+v, want the first active and the unshared account's inactive", rules)
	}
	fake.gone["ws-1"] = true
	params, _ := json.Marshal(launch)
	c = protocol.AssistantConfirmation{Method: protocol.MethodRunLaunch, Params: params}
	if passed, err := svc.approveByRule(t.Context(), m.conversation(), m, &c); passed || err != nil {
		t.Fatalf("card an inactive rule covers = passed %v, %v; want a card", passed, err)
	}
}

// Three cards of one kind approved in a row highlight that kind's rule
// rather than the suggestion to turn auto-approval on; three of different
// kinds bring the suggestion.
func TestNudgeHighlightsTheRuleOfARepeatedKind(t *testing.T) {
	svc, m, _, sock, _ := newRuleService(t)
	kill := protocol.RunIDParams{RunID: "run-1"}
	for range suggestAfter {
		if err := callTool(t, sock, m, protocol.MethodRunKill, kill, true, true); err == nil {
			t.Fatal("run.kill succeeded; the fake refuses it")
		}
	}
	if st, _ := svc.Settings(t.Context(), "m1"); st.SuggestAutoApprove {
		t.Fatal("suggested auto-approval after three cards of one kind")
	}
	c, done := card(t, sock, m, protocol.MethodRunKill, kill)
	if c.Rule == nil || !c.Rule.Suggested {
		t.Fatalf("fourth card of the kind = %+v, want its rule highlighted", c.Rule)
	}
	_ = svc.Confirm("m1", c.ID, false)
	_ = wait(t, done)

	for _, call := range []struct {
		method string
		params any
	}{
		{protocol.MethodRunKill, kill},
		{protocol.MethodRunInject, protocol.RunInjectParams{RunID: "run-1", Message: "hi"}},
		{protocol.MethodEnvSave, struct{}{}},
	} {
		_ = callTool(t, sock, m, call.method, call.params, true, true)
	}
	if st, _ := svc.Settings(t.Context(), "m1"); !st.SuggestAutoApprove {
		t.Fatal("no suggestion after three cards of different kinds")
	}
	c, done = card(t, sock, m, protocol.MethodEnvSave, struct{}{})
	if c.Rule == nil || c.Rule.Suggested {
		t.Fatalf("card after different kinds = %+v, want its rule offered, not highlighted", c.Rule)
	}
	_ = svc.Confirm("m1", c.ID, false)
	_ = wait(t, done)
}

// A launch carrying a saved preference shows its card and offers no rule,
// even when a rule covers its kind; one without passes by the rule.
func TestRuleNeverPassesAPreference(t *testing.T) {
	f := newPrefFixture(t)
	f.say(1, "remember that I prefer table output")
	if _, err := f.remember(protocol.AssistantRememberParams{Text: "I prefer table output"}); err != nil {
		t.Fatal(err)
	}
	key := store.AssistantRuleKey{Method: protocol.MethodRunLaunch, WorkspaceID: "ws-1", Agent: "claude", Permission: "default", NoFallback: true}
	if _, err := f.db.AddAssistantRule(t.Context(), f.m.id, key, "w", "s"); err != nil {
		t.Fatal(err)
	}
	carrying := protocol.RunLaunchParams{WorkspaceID: "ws-1", Harness: "claude", NoFallback: true, Task: "Fix the report. I PREFER TABLE\n  OUTPUT."}
	c, done := card(t, f.sock, f.m, protocol.MethodRunLaunch, carrying)
	if c.Rule != nil {
		t.Fatalf("card carrying a preference offers %+v", c.Rule)
	}
	_ = f.svc.Confirm(f.m.id, c.ID, true)
	if err := wait(t, done); err != nil {
		t.Fatal(err)
	}
	plain := protocol.RunLaunchParams{WorkspaceID: "ws-1", Harness: "claude", NoFallback: true, Task: "Fix the report."}
	if err := callTool(t, f.sock, f.m, protocol.MethodRunLaunch, plain, false, false); err != nil {
		t.Fatal(err)
	}
	got := actions(t, f.svc, f.m)
	if len(got) < 2 || got[len(got)-2][0].Approval != "card" || got[len(got)-1][0].Approval != "rule" {
		t.Fatalf("launch records = %+v; want the one carrying a preference carded and the other passed by the rule", got)
	}
}
