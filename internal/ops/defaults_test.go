package ops

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/protocol"
)

var savedChoices = protocol.LaunchDefaultsResult{Revision: 4, Defaults: protocol.LaunchDefaults{
	Agent:           "claude",
	Agents:          map[string]protocol.AgentLaunchDefault{"claude": {Mode: "acp", Model: "opus"}, "codex": {Mode: "tui", Model: "gpt-5"}},
	AccountMemberID: "mem-ada",
	Fallback:        &protocol.LaunchFallbackDefault{Policy: protocol.FallbackOrdered, Order: []string{"mem-me"}},
}}

func savedFake(t *testing.T, saved protocol.LaunchDefaultsResult) *fakeServer {
	f := newFake(t)
	f.answers[protocol.MethodLaunchDefaultsGet] = returns(saved)
	f.answers[protocol.MethodAgentList] = returns(protocol.AgentListResult{Agents: []protocol.AgentInfo{
		{Name: "claude", Installed: true, Enhanced: "adapter", EnhancedInstalled: true},
		{Name: "codex", Installed: true, Enhanced: "adapter"}}})
	f.answers[protocol.MethodRunLaunch] = answerTo(func(p protocol.RunLaunchParams) any {
		r := testRun
		r.Harness, r.Mode, r.Model, r.AccountMemberID = p.Harness, p.Mode, p.Model, p.AccountMemberID
		return protocol.RunLaunchResult{Run: r}
	})
	f.answers[protocol.MethodMissionCreate] = returns(protocol.MissionCreateResult{Mission: protocol.Mission{ID: "mis-1"}})
	return f
}

func launchParams(t *testing.T, f *fakeServer) protocol.RunLaunchParams {
	t.Helper()
	var p protocol.RunLaunchParams
	if err := json.Unmarshal([]byte(f.acted().params), &p); err != nil {
		t.Fatal(err)
	}
	return p
}

// A launch that names only its task takes the saved agent, its mode and
// model, the saved account and fallback, and its receipt names each field
// they filled.
func TestLaunchAppliesSavedDefaults(t *testing.T) {
	f := savedFake(t, savedChoices)
	cs := connectorSession(t, f, ScopeWrite)
	_, out := run(t, cs, "aether_launch_run", map[string]any{"workspace_id": "ws-1", "task": "fix the cart", "idempotency_key": "k1"})
	p := launchParams(t, f)
	if p.Harness != "claude" || p.Mode != "acp" || p.Model != "opus" || p.AccountMemberID != "mem-ada" || !slices.Equal(p.FallbackAccountMemberIDs, []string{"mem-me"}) {
		t.Fatalf("run.launch params = %+v, want the saved choices", p)
	}
	applied := out["launch"].(map[string]any)["defaults_applied"]
	if jsonOf(t, applied) != `["account_member_id","fallback","harness","mode","model"]` || strings.Contains(f.acted().params, "saved_defaults") {
		t.Fatalf("defaults_applied = %v, run.launch params %s", applied, f.acted().params)
	}

	// The assistant's card is told what saved defaults filled; its socket
	// takes that off before the server sees it.
	f = savedFake(t, savedChoices)
	run(t, assistantSession(t, f.call), "aether_launch_run", map[string]any{"workspace_id": "ws-1", "task": "fix the cart", "idempotency_key": "k1"})
	if !strings.Contains(f.acted().params, `"saved_defaults":{"applied":["account_member_id","fallback","harness","mode","model"]}`) {
		t.Fatalf("assistant run.launch params = %s", f.acted().params)
	}
}

// Explicit params win over saved ones field by field, and saved_defaults
// ignore sends exactly what was given.
func TestLaunchExplicitParamsBeatSavedDefaults(t *testing.T) {
	f := savedFake(t, savedChoices)
	cs := connectorSession(t, f, ScopeWrite)
	_, out := run(t, cs, "aether_launch_run", map[string]any{"workspace_id": "ws-1", "task": "t", "harness": "codex", "account_member_id": "mem-me",
		"no_fallback": true, "idempotency_key": "k1"})
	p := launchParams(t, f)
	if p.Harness != "codex" || p.Mode != "tui" || p.Model != "gpt-5" || p.AccountMemberID != "mem-me" || !p.NoFallback || p.FallbackAccountMemberIDs != nil {
		t.Fatalf("run.launch params = %+v, want codex with its saved mode and model on the account given, fallback off", p)
	}
	receipt := out["launch"].(map[string]any)
	if jsonOf(t, receipt["defaults_applied"]) != `["mode","model"]` ||
		jsonOf(t, receipt["defaults_unchecked"]) != `[{"field":"model","reason":"it was saved and checked for account mem-ada, and this launch runs on mem-me"}]` {
		t.Fatalf("receipt = %v, want the saved model applied and reported unchecked on another account", receipt)
	}

	f = savedFake(t, savedChoices)
	cs = connectorSession(t, f, ScopeWrite)
	run(t, cs, "aether_launch_run", map[string]any{"workspace_id": "ws-1", "task": "t", "harness": "claude", "saved_defaults": "ignore", "idempotency_key": "k2"})
	if got := f.acted().params; got != `{"workspace_id":"ws-1","task":"t","harness":"claude","idempotency_key":"k2"}` {
		t.Fatalf("run.launch params with saved_defaults ignore = %s", got)
	}
	if len(called(f, protocol.MethodLaunchDefaultsGet)) != 0 {
		t.Fatal("saved_defaults ignore read the saved defaults")
	}
	text, _, failed := try(t, cs, "aether_launch_run", map[string]any{"workspace_id": "ws-1", "task": "t", "saved_defaults": "ignore"})
	if !failed || !strings.Contains(text, "harness is required: you have no saved agent") {
		t.Fatalf("a launch with no harness and saved defaults ignored = %q", text)
	}
}

// A saved mode the agent cannot start in on the launch's account or a saved
// fallback member get reports unusable is skipped with its reason; a saved
// model get could not check is used and reported unchecked.
func TestLaunchSkipsUnusableSavedChoices(t *testing.T) {
	saved := savedChoices
	saved.Defaults.AccountMemberID = ""
	saved.Defaults.Fallback = &protocol.LaunchFallbackDefault{Policy: protocol.FallbackOrdered, Order: []string{"mem-bo"}}
	saved.Invalid = []protocol.LaunchDefaultIssue{
		{Field: "fallback.order", Value: "mem-bo", Reason: "permission denied: member mem-bo has not shared their account with you"}}
	saved.Unknown = []protocol.LaunchDefaultIssue{{Field: "agents.codex.model", Value: "gpt-5", Reason: "Aether has no current list of the models codex offers"}}
	f := savedFake(t, saved)
	cs := connectorSession(t, f, ScopeWrite)
	_, out := run(t, cs, "aether_launch_run", map[string]any{"workspace_id": "ws-1", "task": "t", "harness": "codex", "mode": "", "idempotency_key": "k1"})
	p := launchParams(t, f)
	if p.Mode != "tui" || p.Model != "gpt-5" || !p.NoFallback {
		t.Fatalf("run.launch params = %+v, want the saved mode and model, fallback off", p)
	}
	receipt := out["launch"].(map[string]any)
	if skipped := jsonOf(t, receipt["defaults_skipped"]); strings.Contains(skipped, `"model"`) || !strings.Contains(skipped, `mem-bo: permission denied`) {
		t.Fatalf("defaults_skipped = %s", skipped)
	}
	if unchecked := jsonOf(t, receipt["defaults_unchecked"]); unchecked != `[{"field":"model","reason":"Aether has no current list of the models codex offers"}]` {
		t.Fatalf("defaults_unchecked = %s", unchecked)
	}

	saved = savedChoices
	saved.Defaults.Agents = map[string]protocol.AgentLaunchDefault{"codex": {Mode: "acp"}}
	saved.Defaults.Agent = "codex"
	f = savedFake(t, saved)
	cs = connectorSession(t, f, ScopeWrite)
	_, out = run(t, cs, "aether_launch_run", map[string]any{"workspace_id": "ws-1", "task": "t", "idempotency_key": "k2"})
	if p := launchParams(t, f); p.Harness != "codex" || p.Mode != "" {
		t.Fatalf("run.launch params = %+v, want codex in its default mode", p)
	}
	if skipped := jsonOf(t, out["launch"].(map[string]any)["defaults_skipped"]); !strings.Contains(skipped, `codex cannot start in Enhanced mode on this account`) {
		t.Fatalf("defaults_skipped = %s", skipped)
	}
}

// A saved account the member may no longer use fails the launch with the
// server's reason instead of starting it on their own account, and a saved
// agent that cannot launch leaves the harness to the caller.
func TestLaunchRefusesUnusableSavedAccount(t *testing.T) {
	saved := savedChoices
	saved.Invalid = []protocol.LaunchDefaultIssue{{Field: "account_member_id", Value: "mem-ada", Reason: "permission denied: member mem-ada has not shared their account with you"}}
	f := savedFake(t, saved)
	cs := connectorSession(t, f, ScopeWrite)
	text, _, failed := try(t, cs, "aether_launch_run", map[string]any{"workspace_id": "ws-1", "task": "t", "idempotency_key": "k1"})
	if !failed || !strings.Contains(text, "saved account mem-ada: permission denied: member mem-ada has not shared their account with you; pass account_member_id or clear it") {
		t.Fatalf("launch on a revoked saved account = %q", text)
	}
	if len(called(f, protocol.MethodRunLaunch)) != 0 {
		t.Fatal("a refused saved account still launched")
	}

	// The saved agent is checked on the account the launch uses: not signed
	// in on the saved one, it still launches on an account given here.
	f = savedFake(t, savedChoices)
	f.answers[protocol.MethodAgentList] = answerTo(func(p protocol.AgentListParams) any {
		return protocol.AgentListResult{Agents: []protocol.AgentInfo{{Name: "claude", Installed: true, LoginMissing: p.AccountMemberID == "mem-ada"}}}
	})
	cs = connectorSession(t, f, ScopeWrite)
	text, _, failed = try(t, cs, "aether_launch_run", map[string]any{"workspace_id": "ws-1", "task": "t", "idempotency_key": "k1"})
	if !failed || !strings.Contains(text, "harness is required: your saved agent claude cannot launch: the account's owner is not signed in to it") {
		t.Fatalf("launch with an unusable saved agent = %q", text)
	}
	run(t, cs, "aether_launch_run", map[string]any{"workspace_id": "ws-1", "task": "t", "account_member_id": "mem-me", "idempotency_key": "k2"})
	if p := launchParams(t, f); p.Harness != "claude" || p.AccountMemberID != "mem-me" {
		t.Fatalf("run.launch params = %+v, want the saved agent on your own account", p)
	}
}

// A keyed retry whose saved defaults changed since its first attempt gets
// the server's refusal and says why.
func TestKeyedRetryAfterSavedDefaultsChanged(t *testing.T) {
	f := savedFake(t, savedChoices)
	f.errs[protocol.MethodRunLaunch] = &protocol.Error{Code: protocol.CodeConflict,
		Message: `run.launch: idempotency_key "k1" was used for a launch with other params; use a new key for a new launch`}
	cs := connectorSession(t, f, ScopeWrite)
	text, _, failed := try(t, cs, "aether_launch_run", map[string]any{"workspace_id": "ws-1", "task": "t", "idempotency_key": "k1"})
	if !failed || !strings.Contains(text, "use a new key for a new launch; your saved defaults may have changed since the first attempt; pass the fields explicitly") {
		t.Fatalf("keyed retry = %q", text)
	}
}

// A template launch never reads saved defaults, and its tool's schema
// refuses a saved_defaults that is not apply or ignore.
func TestTemplateLaunchIgnoresSavedDefaults(t *testing.T) {
	f := savedFake(t, savedChoices)
	f.answers[protocol.MethodTemplateLaunch] = returns(protocol.TemplateLaunchResult{Run: testRun})
	cs := connectorSession(t, f, ScopeWrite)
	run(t, cs, "aether_launch_run", map[string]any{"workspace_id": "ws-1", "template": "triage", "idempotency_key": "k1"})
	if len(called(f, protocol.MethodLaunchDefaultsGet)) != 0 {
		t.Fatal("a template launch read the saved defaults")
	}
	text, _, failed := try(t, cs, "aether_launch_run", map[string]any{"workspace_id": "ws-1", "template": "triage", "saved_defaults": "skip", "idempotency_key": "k2"})
	if !failed || !strings.Contains(text, `saved_defaults: enum: skip does not equal any of: [ apply ignore]`) {
		t.Fatalf("template launch with saved_defaults skip = %q", text)
	}
}

// A saved model checked on your own account is reported unchecked when
// usage fallback starts the run on another.
func TestFallbackMovedRunReportsSavedModelUnchecked(t *testing.T) {
	saved := savedChoices
	saved.Defaults.AccountMemberID, saved.Defaults.Fallback = "", nil
	f := savedFake(t, saved)
	f.answers[protocol.MethodRunLaunch] = answerTo(func(p protocol.RunLaunchParams) any {
		r := testRun
		r.Model, r.AccountMemberID, r.RequestedAccountMemberID = p.Model, "mem-bo", "mem-me"
		return protocol.RunLaunchResult{Run: r}
	})
	cs := connectorSession(t, f, ScopeWrite)
	_, out := run(t, cs, "aether_launch_run", map[string]any{"workspace_id": "ws-1", "task": "t", "idempotency_key": "k1"})
	unchecked := jsonOf(t, out["launch"].(map[string]any)["defaults_unchecked"])
	if unchecked != `[{"field":"model","reason":"it was saved and checked for account mem-me, and usage fallback started the run on mem-bo"}]` {
		t.Fatalf("defaults_unchecked = %s", unchecked)
	}
}

// A receipt for a launch that already happened, replayed by its key or
// returned as a keyless duplicate, does not claim what saved defaults filled
// this time.
func TestRepeatedLaunchReceiptOmitsSavedDefaults(t *testing.T) {
	f := savedFake(t, savedChoices)
	f.answers[protocol.MethodRunLaunch] = returns(protocol.RunLaunchResult{Run: testRun, Replayed: true})
	cs := connectorSession(t, f, ScopeWrite)
	_, out := run(t, cs, "aether_launch_run", map[string]any{"workspace_id": "ws-1", "task": "fix the cart", "idempotency_key": "k1"})
	receipt := jsonOf(t, out["launch"])
	if out["replayed"] != true || strings.Contains(receipt, "defaults_") {
		t.Fatalf("replayed launch = %v, receipt %s", out, receipt)
	}

	f = savedFake(t, savedChoices)
	prior := testRun
	prior.Harness, prior.Mode, prior.Model, prior.Task, prior.AccountMemberID = "claude", "acp", "opus", "fix the cart", "mem-ada"
	prior.FallbackAccountMemberIDs, prior.CreatedAt = []string{"mem-me"}, time.Now().UTC().Format(time.RFC3339)
	f.answers[protocol.MethodRunList] = returns(protocol.RunListResult{Runs: []protocol.Run{prior}})
	cs = connectorSession(t, f, ScopeWrite)
	_, out = run(t, cs, "aether_launch_run", map[string]any{"workspace_id": "ws-1", "task": "fix the cart"})
	if receipt = jsonOf(t, out["launch"]); out["duplicate"] != true || strings.Contains(receipt, "defaults_") {
		t.Fatalf("duplicate launch = %v, receipt %s", out, receipt)
	}
}

// A swarm's saved agent, mode and model fill only its integrator; the
// saved account fills the integrator and each worker that names none.
func TestSwarmAppliesSavedDefaultsToIntegrator(t *testing.T) {
	f := savedFake(t, savedChoices)
	cs := connectorSession(t, f, ScopeWrite)
	_, out := run(t, cs, "aether_launch_swarm", map[string]any{"workspace_id": "ws-1", "objective": "o",
		"workers": []any{map[string]any{"harness": "codex"}, map[string]any{"harness": "codex", "account_member_id": "mem-me"}}, "idempotency_key": "s1"})
	var p protocol.MissionCreateParams
	if err := json.Unmarshal([]byte(f.acted().params), &p); err != nil {
		t.Fatal(err)
	}
	if p.Integrator != (protocol.MissionIntegrator{AccountMemberID: "mem-ada", Harness: "claude", Mode: "acp", Model: "opus"}) {
		t.Fatalf("integrator = %+v, want the saved choices", p.Integrator)
	}
	workers := map[string]protocol.MissionExecutionChoice{}
	for _, c := range p.ExecutionChoices[1:] {
		workers[c.AccountMemberID] = c
	}
	if workers["mem-ada"] != (protocol.MissionExecutionChoice{AccountMemberID: "mem-ada", Harness: "codex", Mode: "tui"}) || workers["mem-me"].Harness != "codex" {
		t.Fatalf("workers = %+v, want codex on the saved account and on yours, in its own mode and no saved model", p.ExecutionChoices[1:])
	}
	if !slices.Equal(p.FallbackAccountMemberIDs, []string{"mem-me"}) {
		t.Fatalf("fallback = %v, want the saved order swarm-wide", p.FallbackAccountMemberIDs)
	}
	if jsonOf(t, out["launch"].(map[string]any)["defaults_applied"]) != `["account_member_id","fallback","harness","mode","model","workers.account_member_id"]` {
		t.Fatalf("receipt = %v", out["launch"])
	}

	// An integrator account given explicitly leaves the saved one for the
	// workers that name none.
	f = savedFake(t, savedChoices)
	cs = connectorSession(t, f, ScopeWrite)
	run(t, cs, "aether_launch_swarm", map[string]any{"workspace_id": "ws-1", "objective": "o", "account_member_id": "mem-me",
		"workers": []any{map[string]any{"harness": "codex"}}, "idempotency_key": "s2"})
	if err := json.Unmarshal([]byte(f.acted().params), &p); err != nil {
		t.Fatal(err)
	}
	if p.Integrator.AccountMemberID != "mem-me" || p.ExecutionChoices[len(p.ExecutionChoices)-1].AccountMemberID != "mem-ada" {
		t.Fatalf("choices = %+v, want the integrator on yours and the worker on the saved account", p.ExecutionChoices)
	}
}

// aether_launch_options lists the saved defaults with each unusable field.
func TestLaunchOptionsListSavedDefaults(t *testing.T) {
	saved := savedChoices
	saved.Invalid = []protocol.LaunchDefaultIssue{{Field: "account_member_id", Value: "mem-ada", Reason: "permission denied"}}
	f := savedFake(t, saved)
	_, out := run(t, connectorSession(t, f), "aether_launch_options", map[string]any{})
	got := out["saved"].(map[string]any)
	if got["revision"] != float64(4) || got["defaults"].(map[string]any)["agent"] != "claude" || got["invalid"].([]any)[0].(map[string]any)["reason"] != "permission denied" {
		t.Fatalf("saved = %v", got)
	}
	if out["server"].(map[string]any)["ops_contract"] != float64(ContractVersion) {
		t.Fatalf("server = %v", out["server"])
	}
}

// The assistant's save changes only the fields given, and applies them
// once more to the choices another device saved meanwhile.
func TestAssistantSavesLaunchDefaults(t *testing.T) {
	f := savedFake(t, savedChoices)
	newer := savedChoices
	newer.Revision, newer.Defaults.AccountMemberID = 5, "mem-me"
	data, _ := json.Marshal(newer)
	sets := 0
	f.answers[protocol.MethodLaunchDefaultsSet] = answerTo(func(p protocol.LaunchDefaultsSetParams) any {
		sets++
		return protocol.LaunchDefaultsResult{Revision: p.Revision + 1, Defaults: *p.Defaults}
	})
	f.errs[protocol.MethodLaunchDefaultsSet] = &protocol.Error{Code: protocol.CodeConflict, Message: "launch.defaults.set: revision 4 is not current", Data: data}
	cs := assistantSession(t, f.call)
	text, _, failed := try(t, cs, "aether_save_launch_defaults", map[string]any{"agent": "codex", "model": "gpt-5.1"})
	if !failed || !strings.Contains(text, "revision 4 is not current") {
		t.Fatalf("a save refused twice = %q", text)
	}
	calls := called(f, protocol.MethodLaunchDefaultsSet)
	var second protocol.LaunchDefaultsSetParams
	if err := json.Unmarshal([]byte(calls[len(calls)-1].params), &second); err != nil {
		t.Fatal(err)
	}
	d := second.Defaults
	if len(calls) != 2 || second.Revision != 5 || d.Agent != "codex" || d.Agents["codex"] != (protocol.AgentLaunchDefault{Mode: "tui", Model: "gpt-5.1"}) ||
		d.Agents["claude"].Model != "opus" || d.AccountMemberID != "mem-me" {
		t.Fatalf("launch.defaults.set calls = %+v, want the change applied to revision 5", calls)
	}

	delete(f.errs, protocol.MethodLaunchDefaultsSet)
	if text, _ = run(t, cs, "aether_save_launch_defaults", map[string]any{"fallback": "off"}); text != "Saved your launch defaults (revision 5)." || sets != 1 {
		t.Fatalf("save = %q after %d sets", text, sets)
	}
	f.answers[protocol.MethodLaunchDefaultsSet] = answerTo(func(p protocol.LaunchDefaultsSetParams) any {
		return protocol.LaunchDefaultsResult{Revision: p.Revision + 1, Defaults: *p.Defaults}
	})
	run(t, cs, "aether_save_launch_defaults", map[string]any{"agent": "claude", "model": ""})
	calls = called(f, protocol.MethodLaunchDefaultsSet)
	if !strings.Contains(calls[len(calls)-1].params, `"claude":{"mode":"acp"}`) {
		t.Fatalf("clearing the saved model sent %s", calls[len(calls)-1].params)
	}
	if text, _, failed = try(t, cs, "aether_save_launch_defaults", map[string]any{"model": "opus"}); !failed || !strings.Contains(text, "pass agent too") {
		t.Fatalf("a model with no agent = %q", text)
	}
}

// A share revoked after get checked it fails at launch with the server's
// error, and the launch is never retried on another account.
func TestRevokedSavedAccountFailsAtLaunch(t *testing.T) {
	f := savedFake(t, savedChoices)
	f.errs[protocol.MethodRunLaunch] = &protocol.Error{Code: protocol.CodeDenied, Message: "run.launch: permission denied: member mem-ada has not shared their account with you"}
	cs := connectorSession(t, f, ScopeWrite)
	text, _, failed := try(t, cs, "aether_launch_run", map[string]any{"workspace_id": "ws-1", "task": "t", "idempotency_key": "k1"})
	if !failed || !strings.HasPrefix(text, "run.launch: permission denied: member mem-ada has not shared their account with you") || len(called(f, protocol.MethodRunLaunch)) != 1 {
		t.Fatalf("launch on a share revoked meanwhile = %q after %d launches", text, len(called(f, protocol.MethodRunLaunch)))
	}
}

// When the agents cannot be listed, as when another agent's installation
// cannot be read, the saved agent and mode still launch and the receipt
// says they went unchecked; the server checks the agent asked for.
func TestSavedAgentLaunchesWhenAgentsCannotBeListed(t *testing.T) {
	f := savedFake(t, savedChoices)
	f.errs[protocol.MethodAgentList] = &protocol.Error{Code: protocol.CodeInternal, Message: "read codex installation: permission denied"}
	_, out := run(t, connectorSession(t, f, ScopeWrite), "aether_launch_run", map[string]any{"workspace_id": "ws-1", "task": "fix the cart", "idempotency_key": "k1"})
	if p := launchParams(t, f); p.Harness != "claude" || p.Mode != "acp" {
		t.Fatalf("run.launch params = %+v, want the saved agent and mode", p)
	}
	unchecked := jsonOf(t, out["launch"].(map[string]any)["defaults_unchecked"])
	if !strings.Contains(unchecked, `"field":"harness"`) || !strings.Contains(unchecked, `"field":"mode"`) || !strings.Contains(unchecked, "permission denied") {
		t.Fatalf("defaults_unchecked = %s", unchecked)
	}
}

// A swarm whose integrator usage fallback started on another account says
// its saved model went unchecked there, as a run's receipt does.
func TestFallbackMovedIntegratorReportsSavedModelUnchecked(t *testing.T) {
	f := savedFake(t, savedChoices)
	f.answers[protocol.MethodMissionCreate] = returns(protocol.MissionCreateResult{Mission: protocol.Mission{ID: "mis-1", CurrentIntegratorRunID: "run-int"}})
	f.answers[protocol.MethodRunGet] = returns(protocol.RunResult{Run: protocol.Run{ID: "run-int", AccountMemberID: "mem-me"}})
	_, out := run(t, connectorSession(t, f, ScopeWrite), "aether_launch_swarm", map[string]any{"workspace_id": "ws-1", "objective": "o", "idempotency_key": "s1"})
	if unchecked := jsonOf(t, out["launch"].(map[string]any)["defaults_unchecked"]); !strings.Contains(unchecked, `"field":"model"`) || !strings.Contains(unchecked, "mem-me") {
		t.Fatalf("defaults_unchecked = %s, want the saved model noted unchecked on mem-me", unchecked)
	}
}

// A save refused because another device cleared choices applies its change
// to the newer choices alone, so the cleared account and agent stay
// cleared; a keyed launch retried after the saved choices were cleared
// still says how to recover the first launch.
func TestClearedSavedChoicesStayCleared(t *testing.T) {
	f := savedFake(t, savedChoices)
	cleared, _ := json.Marshal(protocol.LaunchDefaultsResult{Revision: 5, Defaults: protocol.LaunchDefaults{Agent: "codex", Agents: map[string]protocol.AgentLaunchDefault{"codex": {Mode: "tui"}}}})
	f.errs[protocol.MethodLaunchDefaultsSet] = &protocol.Error{Code: protocol.CodeConflict, Message: "launch.defaults.set: revision 4 is not current", Data: cleared}
	try(t, assistantSession(t, f.call), "aether_save_launch_defaults", map[string]any{"fallback": "off"})
	calls := called(f, protocol.MethodLaunchDefaultsSet)
	var second protocol.LaunchDefaultsSetParams
	if err := json.Unmarshal([]byte(calls[len(calls)-1].params), &second); err != nil {
		t.Fatal(err)
	}
	if d := second.Defaults; second.Revision != 5 || d.AccountMemberID != "" || d.Agents["claude"] != (protocol.AgentLaunchDefault{}) {
		t.Fatalf("retried save = %+v, want the cleared account and claude choice kept cleared", second)
	}

	f = savedFake(t, protocol.LaunchDefaultsResult{})
	f.errs[protocol.MethodRunLaunch] = &protocol.Error{Code: protocol.CodeConflict,
		Message: `run.launch: idempotency_key "k1" was used for a launch with other params; use a new key for a new launch`}
	text, _, failed := try(t, connectorSession(t, f, ScopeWrite), "aether_launch_run", map[string]any{"workspace_id": "ws-1", "task": "t", "harness": "claude", "idempotency_key": "k1"})
	if !failed || !strings.Contains(text, "your saved defaults may have changed since the first attempt") {
		t.Fatalf("keyed retry after the saved choices were cleared = %q", text)
	}
}
