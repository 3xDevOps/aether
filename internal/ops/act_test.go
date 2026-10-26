package ops

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/protocol"
)

func TestConnectorRunLifecycle(t *testing.T) {
	f := newFake(t)
	relaunched := testRun
	relaunched.Status = "starting"
	f.answers[protocol.MethodRunRelaunch] = returns(protocol.RunResult{Run: relaunched})
	cs := connectorSession(t, f, ScopeWrite)
	text, out := run(t, cs, "aether_run_lifecycle", map[string]any{"run_id": "run-1", "action": "relaunch"})
	if text != "Relaunched run run-1 (starting)." || out["run_id"] != "run-1" || out["url"] != "https://aether.example/?run=run-1" || out["write"] != writeDone {
		t.Fatalf("relaunch = %q %v", text, out)
	}
	if got := f.acted(); got.method != protocol.MethodRunRelaunch || got.scope != ScopeWrite {
		t.Fatalf("relaunch called %+v", got)
	}

	// testRun is Enhanced: switching it to Enhanced again changes nothing.
	f.calls = nil
	if text, _ = run(t, cs, "aether_run_lifecycle", map[string]any{"run_id": "run-1", "action": "enhanced"}); !strings.HasPrefix(text, "Already in enhanced mode") ||
		len(called(f, protocol.MethodRunModeSwitch)) != 0 {
		t.Fatalf("repeated switch = %q", text)
	}
	for action, want := range map[string]string{
		"standard":  `{"run_id":"run-1","mode":"tui","control_session_id":"","control_generation":0}`,
		"protect":   `{"run_id":"run-1","protected":true}`,
		"unprotect": `{"run_id":"run-1","protected":false}`,
		"archive":   `{"run_id":"run-1","archived":true}`,
		"restore":   `{"run_id":"run-1","archived":false}`,
	} {
		g := newFake(t)
		for _, m := range []string{protocol.MethodRunModeSwitch, protocol.MethodRunProtect, protocol.MethodRunArchive} {
			g.answers[m] = returns(protocol.RunResult{Run: testRun})
		}
		run(t, connectorSession(t, g, ScopeWrite), "aether_run_lifecycle", map[string]any{"run_id": "run-1", "action": action})
		if got := g.acted(); got.params != want {
			t.Errorf("%s sent %s %s, want %s", action, got.method, got.params, want)
		}
	}

	// The server's refusal reaches the model unchanged, with the way
	// forward when another session holds control.
	f = newFake(t)
	f.errs[protocol.MethodRunModeSwitch] = &protocol.Error{Code: protocol.CodeConflict, Message: "member mem-2 holds the run's control; switch from that session, or take control first"}
	f.errs[protocol.MethodRunArchive] = &protocol.Error{Code: protocol.CodeDenied, Message: "run.archive: denied: not the run's owner"}
	cs = connectorSession(t, f, ScopeWrite)
	if text, _, failed := try(t, cs, "aether_run_lifecycle", map[string]any{"run_id": "run-1", "action": "standard"}); !failed ||
		!strings.Contains(text, "member mem-2 holds the run's control") || !strings.Contains(text, "ask that member to release it") {
		t.Fatalf("control held = %q", text)
	}
	if text, _, failed := try(t, cs, "aether_run_lifecycle", map[string]any{"run_id": "run-1", "action": "archive"}); !failed || text != "run.archive: denied: not the run's owner" {
		t.Fatalf("denied archive = %q", text)
	}
	if _, _, failed := try(t, cs, "aether_run_lifecycle", map[string]any{"run_id": "run-1", "action": "delete"}); !failed {
		t.Fatal("an action outside the enum was accepted")
	}
}

func TestConnectorHandoff(t *testing.T) {
	members := protocol.MemberListResult{Members: []protocol.Member{
		{ID: "mem-1", DisplayName: "Ada", Role: "admin", GitEmail: "ada@example.com"},
		{ID: "mem-2", DisplayName: "Grace", Role: "viewer"},
		{ID: "mem-3", DisplayName: "Pending", Role: "collaborator", Pending: true},
	}}
	owned := testRun
	owned.MemberID = "mem-1"
	f := newFake(t)
	f.answers[protocol.MethodRunGet] = returns(protocol.RunResult{Run: owned})
	f.answers[protocol.MethodMemberList] = returns(members)
	f.errs[protocol.MethodRunHandoff] = &protocol.Error{Code: protocol.CodeInvalidParams, Message: "cannot hand off to Grace: viewers cannot own runs"}
	cs := connectorSession(t, f, ScopeControl)

	text, _, failed := try(t, cs, "aether_handoff_run", map[string]any{"run_id": "run-1", "to_member_id": "mem-3"})
	_, teammates, _ := strings.Cut(text, "teammates: ")
	if !failed || !strings.Contains(teammates, `{"id":"mem-1","role":"admin"}`) || strings.Contains(teammates, "mem-3") || strings.Contains(text, "Ada") || strings.Contains(text, "ada@example.com") {
		t.Fatalf("handoff to a pending member = %q, want teammates' ids and roles only", text)
	}
	if len(called(f, protocol.MethodRunHandoff)) != 0 {
		t.Fatal("an unknown teammate reached run.handoff")
	}
	if text, _, failed = try(t, cs, "aether_handoff_run", map[string]any{"run_id": "run-1", "to_member_id": "grace"}); !failed || !strings.Contains(text, "viewers cannot own runs") {
		t.Fatalf("handoff to a viewer = %q, want the server's refusal", text)
	}
	if got := called(f, protocol.MethodRunHandoff); len(got) != 1 || got[0].scope != ScopeControl || got[0].params != `{"run_id":"run-1","to_member_id":"mem-2"}` {
		t.Fatalf("run.handoff calls = %+v", got)
	}
	// Handing a run to its owner again changes nothing, so a retry is safe.
	text, out := run(t, cs, "aether_handoff_run", map[string]any{"run_id": "run-1", "to_member_id": "mem-1"})
	if text != "Run run-1 is already owned by mem-1." || out["owner_id"] != "mem-1" || len(called(f, protocol.MethodRunHandoff)) != 1 {
		t.Fatalf("handoff to the owner = %q %v", text, out)
	}
}

func TestConnectorSwarmManage(t *testing.T) {
	f := newFake(t)
	shown := protocol.MissionShowResult{Mission: protocol.Mission{ID: "mis-1"}, Attempts: []protocol.Attempt{
		{RunID: "run-7", TakeoverActive: true, TakeoverGeneration: 4, TakeoverMemberID: "mem-1"},
		{RunID: "run-8"},
	}}
	f.answers[protocol.MethodMissionShow] = returns(shown)
	f.answers[protocol.MethodMissionWorkerRelease] = returns(protocol.MissionWorkerReleaseResult{RunID: "run-7", TakeoverGeneration: 5})
	archived := "2026-10-26T10:00:00Z"
	f.answers[protocol.MethodMissionArchive] = returns(protocol.MissionArchiveResult{Mission: protocol.Mission{ID: "mis-1", Phase: "completed", ArchivedAt: &archived}})
	cs := connectorSession(t, f, ScopeControl)

	text, out := run(t, cs, "aether_swarm_manage", map[string]any{"swarm_id": "swarm:mis-1", "action": "release_worker", "run_id": "run-7"})
	if text != "Released control of worker run run-7; the integrator steers it again." || out["held"] != false {
		t.Fatalf("release = %q %v", text, out)
	}
	if got := f.acted(); got.params != `{"run_id":"run-7","expected_takeover_generation":4}` {
		t.Fatalf("release sent %s", got.params)
	}
	f.calls = nil
	if text, _ = run(t, cs, "aether_swarm_manage", map[string]any{"swarm_id": "mis-1", "action": "release_worker", "run_id": "run-8"}); !strings.HasPrefix(text, "Nobody holds control") ||
		len(called(f, protocol.MethodMissionWorkerRelease)) != 0 {
		t.Fatalf("releasing a worker nobody holds = %q, calls %+v", text, f.calls)
	}
	if msg, _, failed := try(t, cs, "aether_swarm_manage", map[string]any{"swarm_id": "mis-1", "action": "release_worker"}); !failed || !strings.Contains(msg, "takes run_id") {
		t.Fatalf("release without run_id = %q", msg)
	}
	text, out = run(t, cs, "aether_swarm_manage", map[string]any{"swarm_id": "mis-1", "action": "archive"})
	if text != "Swarm mis-1 is archived." || out["archived"] != true {
		t.Fatalf("archive = %q %v", text, out)
	}
}

func TestConnectorReplaceIntegrator(t *testing.T) {
	mission := protocol.Mission{ID: "mis-1", IntegratorGeneration: 3, CurrentIntegratorRunID: "run-old",
		Integrator: protocol.MissionIntegrator{AccountMemberID: "mem-2", Harness: "claude", Mode: "acp"},
		ExecutionChoices: []protocol.MissionExecutionChoice{
			{AccountMemberID: "mem-2", Harness: "claude", Mode: "acp"},
			{AccountMemberID: "mem-2", Harness: "codex", Mode: "tui"},
			{AccountMemberID: "mem-1", Harness: "codex", Mode: "tui"},
		}}
	f := newFake(t)
	f.answers[protocol.MethodServerInfo] = returns(protocol.ServerInfoResult{DashboardURL: "https://aether.example", Member: protocol.Member{ID: "mem-1"}})
	f.answers[protocol.MethodMissionShow] = returns(protocol.MissionShowResult{Mission: mission})
	replaced := mission
	replaced.IntegratorGeneration = 4
	f.answers[protocol.MethodMissionReplaceIntegrator] = returns(protocol.MissionReplaceIntegratorResult{Mission: replaced, RunID: "run-new"})
	cs := connectorSession(t, f, ScopeControl)

	if _, _, failed := try(t, cs, "aether_replace_integrator", map[string]any{"swarm_id": "mis-1"}); !failed {
		t.Fatal("a replacement without expected_generation was accepted")
	}
	text, out := run(t, cs, "aether_replace_integrator", map[string]any{"swarm_id": "mis-1", "expected_generation": 3})
	if text != "Replaced swarm mis-1's integrator with claude (acp); its run is run-new." || out["integrator_run_id"] != "run-new" || out["account_member_id"] != "mem-2" {
		t.Fatalf("replace = %q %v", text, out)
	}
	first := f.acted()
	var p protocol.MissionReplaceIntegratorParams
	if err := json.Unmarshal([]byte(first.params), &p); err != nil || p.ExpectedGeneration != 3 || p.IdempotencyKey == "" || p.Integrator.Harness != "claude" {
		t.Fatalf("replace sent %s", first.params)
	}
	// A keyless retry at the same expected generation reuses the key, so the
	// server returns the first replacement, even once the swarm moved past it.
	f.answers[protocol.MethodMissionShow] = returns(protocol.MissionShowResult{Mission: replaced})
	f.calls = nil
	run(t, cs, "aether_replace_integrator", map[string]any{"swarm_id": "mis-1", "expected_generation": 3})
	if again := f.acted(); again.params != first.params {
		t.Fatalf("retry sent %s, want %s", again.params, first.params)
	}
	// Another agent stays on the integrator's account where it offers it.
	f.calls = nil
	run(t, cs, "aether_replace_integrator", map[string]any{"swarm_id": "mis-1", "expected_generation": 3, "harness": "codex", "mode": "tui"})
	if got := f.acted(); !strings.Contains(got.params, `"account_member_id":"mem-2","harness":"codex","mode":"tui"`) {
		t.Fatalf("codex replacement sent %s", got.params)
	}
	if msg, _, failed := try(t, cs, "aether_replace_integrator", map[string]any{"swarm_id": "mis-1", "expected_generation": 3, "harness": "gemini"}); !failed || !strings.Contains(msg, "its agents are claude, codex") {
		t.Fatalf("an agent the swarm lacks = %q", msg)
	}

	// A replacement made in between is the server's conflict, which says
	// to read the swarm again rather than retry.
	f.errs[protocol.MethodMissionReplaceIntegrator] = &protocol.Error{Code: protocol.CodeConflict, Message: "store: mission stale: expected generation 3, current 4"}
	if text, _, failed := try(t, cs, "aether_replace_integrator", map[string]any{"swarm_id": "mis-1", "expected_generation": 3}); !failed ||
		text != "store: mission stale: expected generation 3, current 4; call aether_get_swarm: the integrator was replaced since expected_generation" {
		t.Fatalf("server conflict = %q", text)
	}
	f.errs[protocol.MethodMissionReplaceIntegrator] = &protocol.Error{Code: protocol.CodeUnavailable, Message: "server restarting"}
	if text, _, failed := try(t, cs, "aether_replace_integrator", map[string]any{"swarm_id": "mis-1", "expected_generation": 3, "idempotency_key": "k-1"}); !failed || !strings.Contains(text, "retry with idempotency_key k-1") {
		t.Fatalf("uncertain failure = %q", text)
	}
}

func TestConnectorIntegrationDecide(t *testing.T) {
	request := &protocol.DeliveryRequest{RequestID: "req-1", RequestVersion: 7, Action: "land", State: protocol.DeliveryPending}
	candidate := protocol.Candidate{CandidateID: "cand-1", WorkspaceID: "ws-1", TargetRef: "refs/heads/main", State: protocol.CandidateFrozen,
		DeliveryRequest: request, Verifications: []protocol.Verification{{VerificationID: "v-1", Status: protocol.VerificationPassed, Output: injection}},
		ExpiresAt: time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC)}
	f := newFake(t)
	f.answers[protocol.MethodIntegrationShow] = returns(protocol.IntegrationShowResult{Candidate: candidate})
	f.answers[protocol.MethodIntegrationList] = returns(protocol.IntegrationListResult{Candidates: []protocol.CandidateSummary{
		{CandidateID: "cand-1", State: protocol.CandidateFrozen, TargetRef: "refs/heads/main", DeliveryRequest: request}}})
	decided, approved := candidate, *request
	approved.State = protocol.DeliveryApproved
	decided.DeliveryRequest = &approved
	f.answers[protocol.MethodIntegrationDecide] = returns(protocol.IntegrationDecideResult{Candidate: decided})
	cs := connectorSession(t, f, ScopeControl)

	text, _ := run(t, cs, "aether_integration", map[string]any{"workspace_id": "ws-1"})
	if text != "1 candidates, 1 delivery requests awaiting a decision." {
		t.Fatalf("list = %q", text)
	}
	_, out := run(t, cs, "aether_integration", map[string]any{"workspace_id": "ws-1", "candidate_id": "cand-1"})
	if strings.Contains(trusted(t, out), injection) {
		t.Fatalf("verification output outside untrusted: %v", out)
	}

	text, out = run(t, cs, "aether_integration_decide", map[string]any{"workspace_id": "ws-1", "candidate_id": "cand-1", "request_id": "req-1", "action": "approve"})
	if text != "Candidate cand-1's delivery request is approved." || out["request_state"] != "approved" {
		t.Fatalf("approve = %q %v", text, out)
	}
	if got := f.acted(); got.method != protocol.MethodIntegrationDecide || got.params != `{"workspace_id":"ws-1","candidate_id":"cand-1","request_id":"req-1","request_version":7,"approve":true}` {
		t.Fatalf("approve sent %+v", got)
	}
	f.calls = nil
	if msg, _, failed := try(t, cs, "aether_integration_decide", map[string]any{"workspace_id": "ws-1", "candidate_id": "cand-1", "request_id": "req-0", "action": "deliver"}); !failed ||
		!strings.Contains(msg, "no longer req-0") || len(called(f, protocol.MethodIntegrationDeliver)) != 0 {
		t.Fatalf("deciding a request that changed = %q", msg)
	}
	// A request already in the state asked for is done, so a retry is safe.
	request.State = protocol.DeliveryApproved
	if text, _ = run(t, cs, "aether_integration_decide", map[string]any{"workspace_id": "ws-1", "candidate_id": "cand-1", "request_id": "req-1", "action": "approve"}); text != "Candidate cand-1's delivery request is already approved." ||
		len(called(f, protocol.MethodIntegrationDecide)) != 0 {
		t.Fatalf("approving twice = %q", text)
	}
	f.errs[protocol.MethodIntegrationDeliver] = &protocol.Error{Code: protocol.CodeDenied, Message: "integration.deliver: denied: push permission"}
	if text, _, failed := try(t, cs, "aether_integration_decide", map[string]any{"workspace_id": "ws-1", "candidate_id": "cand-1", "request_id": "req-1", "action": "deliver"}); !failed ||
		text != "integration.deliver: denied: push permission" {
		t.Fatalf("denied delivery = %q", text)
	}
}

func prFeedback(head string) protocol.RunPRFeedbackResult {
	return protocol.RunPRFeedbackResult{Identity: "octo",
		PullRequest: &protocol.RunPullRequest{Number: 7, URL: "https://github.com/acme/shop/pull/7", State: "open", Repository: "acme/shop", BaseBranch: "main", HeadOID: head},
		Checks:      []protocol.RunPRCheck{{Name: "test", Conclusion: "FAILURE"}, {Name: "lint", Conclusion: "SUCCESS"}, {Name: "e2e"}},
		Reviews: []protocol.RunPRReview{{Author: "grace", State: "CHANGES_REQUESTED"}, {Author: "ada", State: "CHANGES_REQUESTED"},
			{Author: "ada", State: "APPROVED"}},
		MergeStateStatus: "BLOCKED"}
}

func TestConnectorPRMerge(t *testing.T) {
	f := newFake(t)
	f.answers[protocol.MethodRunPRFeedback] = returns(prFeedback("abcdef0123456789"))
	f.answers[protocol.MethodRunPRMerge] = returns(protocol.RunPRMergeResult{Merged: true,
		PullRequest: &protocol.RunPullRequest{Number: 7, State: "merged", HeadOID: "abcdef0123456789"}})
	cs := connectorSession(t, f, ScopeControl)

	// A head that moved is refused before any merge, with what GitHub
	// reports blocking it.
	text, _, failed := try(t, cs, "aether_pr_merge", map[string]any{"run_id": "run-1", "expected_head_oid": "1234567"})
	if !failed || !strings.Contains(text, "head moved from 1234567 to abcdef0123456789") ||
		!strings.Contains(text, "GitHub reports 1 failing checks, 1 pending checks, 1 reviewers requesting changes, merge state BLOCKED") || strings.Contains(text, "grace") {
		t.Fatalf("moved head = %q", text)
	}
	if len(called(f, protocol.MethodRunPRMerge)) != 0 {
		t.Fatal("a moved head was merged")
	}
	if _, _, failed = try(t, cs, "aether_pr_merge", map[string]any{"run_id": "run-1", "expected_head_oid": "abc"}); !failed {
		t.Fatal("a 3-character head was accepted")
	}

	text, out := run(t, cs, "aether_pr_merge", map[string]any{"run_id": "run-1", "expected_head_oid": "abcdef0"})
	if text != "Merged pull request #7 into main." || out["merged"] != true {
		t.Fatalf("merge = %q %v", text, out)
	}
	if got := f.acted(); got.scope != ScopeControl || got.params != `{"run_id":"run-1","method":"squash","expected_head":"abcdef0123456789","repository":"acme/shop","number":7,"expected_login":"octo","expected_base":"main"}` {
		t.Fatalf("merge sent %+v", got)
	}

	// GitHub's refusal carries the blockers too.
	f.answers[protocol.MethodRunPRMerge] = returns(protocol.RunPRMergeResult{Error: "Required status check \"test\" is failing"})
	if text, _, failed = try(t, cs, "aether_pr_merge", map[string]any{"run_id": "run-1", "expected_head_oid": "abcdef0"}); !failed ||
		!strings.Contains(text, `GitHub did not merge pull request #7: Required status check "test" is failing; GitHub reports 1 failing checks`) {
		t.Fatalf("refused merge = %q", text)
	}
	// A merged pull request is reported merged, so a retry is safe.
	merged := prFeedback("abcdef0123456789")
	merged.PullRequest.State = "merged"
	f.answers[protocol.MethodRunPRFeedback] = returns(merged)
	f.calls = nil
	if text, _ = run(t, cs, "aether_pr_merge", map[string]any{"run_id": "run-1", "expected_head_oid": "abcdef0"}); text != "Pull request #7 is already merged." || len(called(f, protocol.MethodRunPRMerge)) != 0 {
		t.Fatalf("merging twice = %q", text)
	}
	f.errs[protocol.MethodRunPRFeedback] = &protocol.Error{Code: protocol.CodeDenied, Message: "run.pr.feedback: denied"}
	if text, _, failed = try(t, cs, "aether_pr_merge", map[string]any{"run_id": "run-1", "expected_head_oid": "abcdef0"}); !failed || text != "run.pr.feedback: denied" {
		t.Fatalf("denied feedback = %q", text)
	}
}

func TestConnectorPRCreate(t *testing.T) {
	expected := protocol.RunGitExpected{Branch: "fix-login", Head: "abcdef0123456789"}
	target := protocol.RunPRTarget{Repository: "acme/shop", BaseBranch: "main", HeadRepository: "acme/shop", HeadBranch: "fix-login"}
	f := newFake(t)
	f.answers[protocol.MethodRunPRStatus] = returns(protocol.RunPRStatusResult{Identity: "octo", Expected: &expected, Target: &target})
	f.answers[protocol.MethodRunPRCreate] = returns(protocol.RunPRCreateResult{Created: true,
		PullRequest: &protocol.RunPullRequest{Number: 8, URL: "https://github.com/acme/shop/pull/8", State: "open"}})
	cs := connectorSession(t, f, ScopeControl)

	if text, _, failed := try(t, cs, "aether_pr_create", map[string]any{"run_id": "run-1", "title": "Fix login", "body": "b", "expected_head": "1234567"}); !failed ||
		!strings.Contains(text, "moved to abcdef0123456789 since 1234567") || len(called(f, protocol.MethodRunPRCreate)) != 0 {
		t.Fatalf("moved branch = %q", text)
	}
	text, out := run(t, cs, "aether_pr_create", map[string]any{"run_id": "run-1", "title": "Fix login", "body": "b", "draft": true, "expected_head": "abcdef0"})
	if text != "Opened pull request #8 for run run-1: https://github.com/acme/shop/pull/8." || out["created"] != true {
		t.Fatalf("create = %q %v", text, out)
	}
	// The pull request opened is the one the status lookup resolved, so the
	// target cannot change after the member approves it.
	if got := f.acted(); got.params != `{"run_id":"run-1","expected":{"branch":"fix-login","head":"abcdef0123456789"},"target":{"repository":"acme/shop","base_branch":"main","head_repository":"acme/shop","head_branch":"fix-login"},"title":"Fix login","body":"b","draft":true,"expected_login":"octo"}` {
		t.Fatalf("create sent %s", got.params)
	}
	// An open pull request is returned, so a retry opens no second one.
	f.answers[protocol.MethodRunPRStatus] = returns(protocol.RunPRStatusResult{Identity: "octo", Expected: &expected, Target: &target,
		PullRequest: &protocol.RunPullRequest{Number: 8, State: "open", URL: "u"}})
	f.calls = nil
	if text, _ = run(t, cs, "aether_pr_create", map[string]any{"run_id": "run-1", "title": "Fix login", "body": "b"}); !strings.HasPrefix(text, "Run run-1 already has pull request #8 open") ||
		len(called(f, protocol.MethodRunPRCreate)) != 0 {
		t.Fatalf("second create = %q", text)
	}
	f.answers[protocol.MethodRunPRStatus] = returns(protocol.RunPRStatusResult{Identity: "octo", Expected: &expected, Target: &target})
	f.answers[protocol.MethodRunPRCreate] = returns(protocol.RunPRCreateResult{CreationUncertain: true, Error: "gh timed out"})
	if _, out = run(t, cs, "aether_pr_create", map[string]any{"run_id": "run-1", "title": "Fix login", "body": "b"}); out["write"] != writePending {
		t.Fatalf("uncertain creation = %v", out)
	}
	f.answers[protocol.MethodRunPRStatus] = returns(protocol.RunPRStatusResult{Error: "the checkout is on a detached HEAD at abc, so expected and target cannot default; pass both"})
	if text, _, failed := try(t, cs, "aether_pr_create", map[string]any{"run_id": "run-1", "title": "Fix login", "body": "b"}); !failed || !strings.Contains(text, "detached HEAD") {
		t.Fatalf("detached checkout = %q", text)
	}
}

func TestConnectorMemoryWrites(t *testing.T) {
	f := newFake(t)
	f.answers[protocol.MethodMemoryAdd] = returns(protocol.MemoryAddResult{Item: protocol.MemoryItem{ID: "mem-9", Kind: "gotcha", WorkspaceID: "ws-1"}, Duplicate: true})
	f.answers[protocol.MethodMemoryPin] = returns(protocol.MemoryPinResult{Item: protocol.MemoryItem{ID: "mem-9", PinnedAt: new("2026-10-26T10:00:00Z")}})
	f.answers[protocol.MethodMemoryShow] = returns(protocol.MemoryShowResult{Item: protocol.MemoryItem{ID: "mem-9", Kind: "gotcha", Body: injection}})
	f.errs[protocol.MethodMemoryUnpin] = &protocol.Error{Code: protocol.CodeDenied, Message: "memory.unpin: denied"}
	cs := connectorSession(t, f, ScopeWrite)

	text, out := run(t, cs, "aether_memory_add", map[string]any{"workspace_id": "ws-1", "kind": "gotcha", "text": "CI needs Docker."})
	if !strings.Contains(text, "already remembers this as note mem-9") || out["url"] != "https://aether.example/?page=memory" {
		t.Fatalf("duplicate add = %q %v", text, out)
	}
	if text, _ = run(t, cs, "aether_memory_pin", map[string]any{"workspace_id": "ws-1", "id": "mem-9", "pin": true}); text != "Pinned note mem-9." {
		t.Fatalf("pin = %q", text)
	}
	if msg, _, failed := try(t, cs, "aether_memory_pin", map[string]any{"workspace_id": "ws-1", "id": "mem-9", "pin": false}); !failed || msg != "memory.unpin: denied" {
		t.Fatalf("denied unpin = %q", msg)
	}
	if _, out = run(t, cs, "aether_memory_show", map[string]any{"workspace_id": "ws-1", "id": "mem-9"}); strings.Contains(trusted(t, out), injection) {
		t.Fatalf("note body outside untrusted: %v", out)
	}

	// Repeating a replacement that went through returns the note that
	// replaced the old one, though the server refuses to replace it again.
	f.errs[protocol.MethodMemoryAdd] = &protocol.Error{Code: protocol.CodeConflict, Message: "memory.add: note mem-1 was already replaced"}
	f.answers[protocol.MethodMemoryShow] = func(raw json.RawMessage) (any, error) {
		var p protocol.MemoryShowParams
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		if p.ID == "mem-1" {
			return protocol.MemoryShowResult{Item: protocol.MemoryItem{ID: "mem-1", SupersededBy: "mem-9"}}, nil
		}
		return protocol.MemoryShowResult{Item: protocol.MemoryItem{ID: "mem-9", Kind: "gotcha", Body: "CI needs Docker."}}, nil
	}
	if text, out = run(t, cs, "aether_memory_add", map[string]any{"workspace_id": "ws-1", "kind": "gotcha", "text": "CI needs Docker.", "supersedes": "mem-1"}); out["id"] != "mem-9" || out["duplicate"] != true {
		t.Fatalf("repeated replacement = %q %v", text, out)
	}
	if text, _, failed := try(t, cs, "aether_memory_add", map[string]any{"workspace_id": "ws-1", "kind": "gotcha", "text": "Something else.", "supersedes": "mem-1"}); !failed || !strings.Contains(text, "already replaced") {
		t.Fatalf("a different replacement of a replaced note = %q", text)
	}
}

func TestConnectorRunChangesPages(t *testing.T) {
	f := newFake(t)
	multi := testRun
	multi.Repos = []protocol.RunRepo{{WorkspaceID: "ws-1", Name: "api", Primary: true}, {WorkspaceID: "ws-2", Name: "web"}}
	f.answers[protocol.MethodRunGet] = returns(protocol.RunResult{Run: multi})
	f.answers[protocol.MethodRunGitLog] = func(raw json.RawMessage) (any, error) {
		var p protocol.RunGitLogParams
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		commits := make([]protocol.RunGitCommit, p.Limit)
		for i := range commits {
			commits[i] = protocol.RunGitCommit{Short: string(rune('a' + i%26))}
		}
		return protocol.RunGitLogResult{Ahead: 30, Commits: commits}, nil
	}
	var diff strings.Builder
	for i := range 12 {
		diff.WriteString("diff --git a/f" + string(rune('a'+i)) + " b/f\n+line\n")
	}
	f.answers[protocol.MethodRunGitDiff] = returns(protocol.RunGitDiffResult{Output: protocol.RunRepoCommandOutput{Stdout: strings.TrimSuffix(diff.String(), "\n"), Truncated: true}})
	cs := connectorSession(t, f)

	_, out := run(t, cs, "aether_run_changes", map[string]any{"run_id": "run-1", "repo": "ws-2", "include_diff": true})
	if out["next_commits_offset"] != float64(20) || out["next_diff_file"] != float64(10) || out["diff_files"] != float64(12) || out["diff_truncated"] != true || len(out["repos"].([]any)) != 2 {
		t.Fatalf("first page = %v", out)
	}
	if got := called(f, protocol.MethodRunGitDiff); got[0].params != `{"repo":"ws-2","run_id":"run-1"}` {
		t.Fatalf("diff params = %s", got[0].params)
	}
	if len(called(f, protocol.MethodRunEvidenceList)) != 0 {
		t.Fatal("an extra repo read the primary's evidence")
	}
	_, out = run(t, cs, "aether_run_changes", map[string]any{"run_id": "run-1", "commits_offset": 20, "include_diff": true, "diff_file": 10})
	untrusted := out["untrusted"].(map[string]any)
	if _, more := out["next_commits_offset"]; more || len(untrusted["commits"].([]any)) != 20 || len(untrusted["diff"].([]any)) != 2 || out["next_diff_file"] != nil {
		t.Fatalf("last page = %v", out)
	}
	if !strings.HasPrefix(untrusted["diff"].([]any)[0].(string), "diff --git a/fk") {
		t.Fatalf("second diff page starts %q", untrusted["diff"].([]any)[0])
	}
}
