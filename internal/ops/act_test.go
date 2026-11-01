package ops

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/protocol"
)

func TestConnectorRunLifecycle(t *testing.T) {
	f := newFake(t)
	relaunched := testRun
	relaunched.Status = "starting"
	f.answers[protocol.MethodRunRelaunch] = returns(protocol.RunResult{Run: relaunched})
	cs := connectorSession(t, f, ScopeWrite)
	text, out := run(t, cs, "aether_session_lifecycle", map[string]any{"run_id": "run-1", "action": "relaunch"})
	if text != "Relaunched session run-1 (starting)." || out["run_id"] != "run-1" || out["url"] != "https://aether.example/?run=run-1" || out["write"] != writeDone {
		t.Fatalf("relaunch = %q %v", text, out)
	}
	if got := f.acted(); got.method != protocol.MethodRunRelaunch || got.scope != ScopeWrite {
		t.Fatalf("relaunch called %+v", got)
	}

	for action, want := range map[string]string{
		"protect":   `{"run_id":"run-1","protected":true}`,
		"unprotect": `{"run_id":"run-1","protected":false}`,
		"archive":   `{"run_id":"run-1","archived":true}`,
		"restore":   `{"run_id":"run-1","archived":false}`,
	} {
		g := newFake(t)
		protectedRun := testRun
		protectedRun.Protected = action == "unprotect"
		g.answers[protocol.MethodRunGet] = returns(protocol.RunResult{Run: protectedRun})
		for _, m := range []string{protocol.MethodRunProtect, protocol.MethodRunArchive} {
			g.answers[m] = returns(protocol.RunResult{Run: testRun})
		}
		run(t, connectorSession(t, g, ScopeWrite), "aether_session_lifecycle", map[string]any{"run_id": "run-1", "action": action})
		if got := g.acted(); got.params != want {
			t.Errorf("%s sent %s %s, want %s", action, got.method, got.params, want)
		}
	}

	// run.protect revokes the run's control, so protecting a protected run
	// does not call it again.
	f = newFake(t)
	protectedRun := testRun
	protectedRun.Protected = true
	f.answers[protocol.MethodRunGet] = returns(protocol.RunResult{Run: protectedRun})
	if text, _ := run(t, connectorSession(t, f, ScopeWrite), "aether_session_lifecycle", map[string]any{"run_id": "run-1", "action": "protect"}); !strings.HasPrefix(text, "Already protected") ||
		len(called(f, protocol.MethodRunProtect)) != 0 {
		t.Fatalf("repeated protect = %q", text)
	}

	// The server's refusal reaches the model unchanged.
	f = newFake(t)
	f.errs[protocol.MethodRunArchive] = &protocol.Error{Code: protocol.CodeDenied, Message: "run.archive: denied: not the session's owner"}
	cs = connectorSession(t, f, ScopeWrite)
	if text, _, failed := try(t, cs, "aether_session_lifecycle", map[string]any{"run_id": "run-1", "action": "archive"}); !failed || text != "run.archive: denied: not the session's owner" {
		t.Fatalf("denied archive = %q", text)
	}
	for _, action := range []string{"delete", "standard"} {
		if _, _, failed := try(t, cs, "aether_session_lifecycle", map[string]any{"run_id": "run-1", "action": action}); !failed {
			t.Fatalf("action %s outside the enum was accepted", action)
		}
	}
}

func TestConnectorSwitchRunMode(t *testing.T) {
	f := newFake(t)
	f.answers[protocol.MethodRunModeSwitch] = returns(protocol.RunResult{Run: testRun})
	cs := connectorSession(t, f, ScopeControl)
	// testRun is Enhanced: switching it to Enhanced again changes nothing.
	if text, _ := run(t, cs, "aether_switch_session_mode", map[string]any{"run_id": "run-1", "mode": "enhanced"}); !strings.HasPrefix(text, "Already in enhanced mode") ||
		len(called(f, protocol.MethodRunModeSwitch)) != 0 {
		t.Fatalf("repeated switch = %q", text)
	}
	run(t, cs, "aether_switch_session_mode", map[string]any{"run_id": "run-1", "mode": "standard"})
	if got := f.acted(); got.scope != ScopeControl || got.params != `{"run_id":"run-1","mode":"tui","control_session_id":"","control_generation":0}` {
		t.Fatalf("switch called %+v", got)
	}

	// The way forward when another session holds control.
	f.errs[protocol.MethodRunModeSwitch] = &protocol.Error{Code: protocol.CodeConflict, Message: "member mem-2 holds the session's control; switch from that session, or take control first"}
	if text, _, failed := try(t, cs, "aether_switch_session_mode", map[string]any{"run_id": "run-1", "mode": "standard"}); !failed ||
		!strings.Contains(text, "member mem-2 holds the session's control") || !strings.Contains(text, "ask that member to release it") {
		t.Fatalf("control held = %q", text)
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
	f.errs[protocol.MethodRunHandoff] = &protocol.Error{Code: protocol.CodeInvalidParams, Message: "cannot hand off to Grace: viewers cannot own sessions"}
	cs := connectorSession(t, f, ScopeControl)

	text, _, failed := try(t, cs, "aether_handoff_session", map[string]any{"run_id": "run-1", "to_member_id": "mem-3"})
	_, teammates, _ := strings.Cut(text, "teammates: ")
	if !failed || !strings.Contains(teammates, `{"id":"mem-1","role":"admin"}`) || strings.Contains(teammates, "mem-3") || strings.Contains(text, "Ada") || strings.Contains(text, "ada@example.com") {
		t.Fatalf("handoff to a pending member = %q, want teammates' ids and roles only", text)
	}
	if len(called(f, protocol.MethodRunHandoff)) != 0 {
		t.Fatal("an unknown teammate reached run.handoff")
	}
	if text, _, failed = try(t, cs, "aether_handoff_session", map[string]any{"run_id": "run-1", "to_member_id": "grace"}); !failed || !strings.Contains(text, "viewers cannot own sessions") {
		t.Fatalf("handoff to a viewer = %q, want the server's refusal", text)
	}
	if got := called(f, protocol.MethodRunHandoff); len(got) != 1 || got[0].scope != ScopeControl || got[0].params != `{"run_id":"run-1","to_member_id":"mem-2"}` {
		t.Fatalf("run.handoff calls = %+v", got)
	}
	// Handing a run to its owner again changes nothing, so a retry is safe.
	text, out := run(t, cs, "aether_handoff_session", map[string]any{"run_id": "run-1", "to_member_id": "mem-1"})
	if text != "Session run-1 is already owned by mem-1." || out["owner_id"] != "mem-1" || len(called(f, protocol.MethodRunHandoff)) != 1 {
		t.Fatalf("handoff to the owner = %q %v", text, out)
	}
}

func prFeedback() protocol.RunPRFeedbackResult {
	return protocol.RunPRFeedbackResult{Identity: "octo",
		PullRequest: &protocol.RunPullRequest{Number: 7, URL: "https://github.com/acme/shop/pull/7", State: "open", Repository: "acme/shop", BaseBranch: "main", HeadOID: "abcdef0123456789"},
		Checks:      []protocol.RunPRCheck{{Name: "test", Conclusion: "FAILURE"}, {Name: "lint", Conclusion: "SUCCESS"}, {Name: "e2e"}},
		Reviews: []protocol.RunPRReview{{Author: "grace", State: "CHANGES_REQUESTED"}, {Author: "ada", State: "CHANGES_REQUESTED"},
			{Author: "ada", State: "APPROVED"}, {Author: "grace", State: "COMMENTED"}},
		MergeStateStatus: "BLOCKED"}
}

func TestConnectorPRMerge(t *testing.T) {
	f := newFake(t)
	f.answers[protocol.MethodRunPRFeedback] = returns(prFeedback())
	f.answers[protocol.MethodRunPRMerge] = returns(protocol.RunPRMergeResult{Merged: true,
		PullRequest: &protocol.RunPullRequest{Number: 7, State: "merged", HeadOID: "abcdef0123456789"}})
	cs := connectorSession(t, f, ScopeControl)

	// A head that moved is refused before any merge, with what GitHub
	// reports blocking it.
	text, _, failed := try(t, cs, "aether_pr_merge", map[string]any{"run_id": "run-1", "repository": "acme/shop", "number": 7, "expected_head_oid": "1234567"})
	if !failed || !strings.Contains(text, "head moved from 1234567 to abcdef0123456789") ||
		!strings.Contains(text, "GitHub reports 1 failing checks, 1 pending checks, 1 reviewers requesting changes, merge state BLOCKED") || strings.Contains(text, "grace") {
		t.Fatalf("moved head = %q", text)
	}
	if len(called(f, protocol.MethodRunPRMerge)) != 0 {
		t.Fatal("a moved head was merged")
	}
	if _, _, failed = try(t, cs, "aether_pr_merge", map[string]any{"run_id": "run-1", "repository": "acme/shop", "number": 7, "expected_head_oid": "abc"}); !failed {
		t.Fatal("a 3-character head was accepted")
	}

	text, out := run(t, cs, "aether_pr_merge", map[string]any{"run_id": "run-1", "repository": "acme/shop", "number": 7, "expected_head_oid": "abcdef0"})
	if text != "Merged pull request #7 into main." || out["merged"] != true || out["merge_state"] != "BLOCKED" ||
		fmt.Sprint(out["blockers"]) != "map[changes_requested:1 failing_checks:1 pending_checks:1]" {
		t.Fatalf("merge = %q %v", text, out)
	}
	if got := f.acted(); got.scope != ScopeControl || got.params != `{"run_id":"run-1","method":"squash","expected_head":"abcdef0123456789","repository":"acme/shop","number":7,"expected_login":"octo","expected_base":"main"}` {
		t.Fatalf("merge sent %+v", got)
	}

	if text, _, failed = try(t, cs, "aether_pr_merge", map[string]any{"run_id": "run-1", "repository": "acme/shop", "number": 6, "expected_head_oid": "abcdef0"}); !failed || !strings.Contains(text, "now acme/shop#7, not acme/shop#6") {
		t.Fatalf("another pull request = %q", text)
	}
	// The same number on another fork is not the pull request reviewed.
	if text, _, failed = try(t, cs, "aether_pr_merge", map[string]any{"run_id": "run-1", "repository": "fork/shop", "number": 7, "expected_head_oid": "abcdef0"}); !failed ||
		!strings.Contains(text, "now acme/shop#7, not fork/shop#7") {
		t.Fatalf("another repository = %q", text)
	}
	// A recorded pull request with no head says why instead of a moved head.
	headless := prFeedback()
	headless.PullRequest.HeadOID, headless.Error = "", "checks and review feedback need the session's environment, which is gone"
	f.answers[protocol.MethodRunPRFeedback] = returns(headless)
	if text, _, failed = try(t, cs, "aether_pr_merge", map[string]any{"run_id": "run-1", "repository": "acme/shop", "number": 7, "expected_head_oid": "abcdef0"}); !failed ||
		!strings.Contains(text, "head commit could not be read: checks and review feedback need the session's environment, which is gone") {
		t.Fatalf("headless pull request = %q", text)
	}
	f.answers[protocol.MethodRunPRFeedback] = returns(prFeedback())
	f.answers[protocol.MethodRunPRMerge] = returns(protocol.RunPRMergeResult{PullRequest: &protocol.RunPullRequest{Number: 7, State: "open"}})
	if _, queued := run(t, cs, "aether_pr_merge", map[string]any{"run_id": "run-1", "repository": "acme/shop", "number": 7, "expected_head_oid": "abcdef0"}); queued["write"] != writePending || queued["merged"] != false {
		t.Fatalf("queued merge = %v", queued)
	}
	// GitHub's refusal carries the blockers too.
	f.answers[protocol.MethodRunPRMerge] = returns(protocol.RunPRMergeResult{Error: "Required status check \"test\" is failing"})
	if text, _, failed = try(t, cs, "aether_pr_merge", map[string]any{"run_id": "run-1", "repository": "acme/shop", "number": 7, "expected_head_oid": "abcdef0"}); !failed ||
		!strings.Contains(text, `GitHub did not merge pull request #7: Required status check "test" is failing; GitHub reports 1 failing checks`) {
		t.Fatalf("refused merge = %q", text)
	}
	// A merge GitHub's lost reply hid but a read-back confirmed is merged.
	f.answers[protocol.MethodRunPRMerge] = returns(protocol.RunPRMergeResult{Merged: true, Error: "gh: connection reset"})
	if text, out = run(t, cs, "aether_pr_merge", map[string]any{"run_id": "run-1", "repository": "acme/shop", "number": 7, "expected_head_oid": "abcdef0"}); text != "Merged pull request #7 into main." ||
		out["merge_error"] != "gh: connection reset" {
		t.Fatalf("confirmed merge = %q %v", text, out)
	}
	// Cut-off feedback gives lower bounds.
	partial := prFeedback()
	partial.Truncated = true
	f.answers[protocol.MethodRunPRFeedback] = returns(partial)
	if text, _, failed = try(t, cs, "aether_pr_merge", map[string]any{"run_id": "run-1", "repository": "acme/shop", "number": 7, "expected_head_oid": "1234567"}); !failed ||
		!strings.Contains(text, "GitHub reports at least 1 failing checks") || !strings.Contains(text, "cut off") {
		t.Fatalf("partial feedback = %q", text)
	}
	f.answers[protocol.MethodRunPRFeedback] = returns(prFeedback())
	// A merged pull request is reported merged, so a retry is safe.
	merged := prFeedback()
	merged.PullRequest.State = "merged"
	f.answers[protocol.MethodRunPRFeedback] = returns(merged)
	f.calls = nil
	if text, _ = run(t, cs, "aether_pr_merge", map[string]any{"run_id": "run-1", "repository": "acme/shop", "number": 7, "expected_head_oid": "abcdef0"}); text != "Pull request #7 is already merged." || len(called(f, protocol.MethodRunPRMerge)) != 0 {
		t.Fatalf("merging twice = %q", text)
	}
	f.errs[protocol.MethodRunPRFeedback] = &protocol.Error{Code: protocol.CodeDenied, Message: "run.pr.feedback: denied"}
	if text, _, failed = try(t, cs, "aether_pr_merge", map[string]any{"run_id": "run-1", "repository": "acme/shop", "number": 7, "expected_head_oid": "abcdef0"}); !failed || text != "run.pr.feedback: denied" {
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
	if text != "Opened pull request #8 for session run-1: https://github.com/acme/shop/pull/8." || out["created"] != true {
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
	if text, _ = run(t, cs, "aether_pr_create", map[string]any{"run_id": "run-1", "title": "Fix login", "body": "b"}); !strings.HasPrefix(text, "Session run-1 already has pull request #8 open") ||
		len(called(f, protocol.MethodRunPRCreate)) != 0 {
		t.Fatalf("second create = %q", text)
	}
	f.answers[protocol.MethodRunPRStatus] = returns(protocol.RunPRStatusResult{Identity: "octo", Expected: &expected, Target: &target})
	f.answers[protocol.MethodRunPRCreate] = returns(protocol.RunPRCreateResult{Reconciled: true, CreationUncertain: true, Error: "gh timed out",
		PullRequest: &protocol.RunPullRequest{Number: 9, State: "open", URL: "u9"}})
	if text, out = run(t, cs, "aether_pr_create", map[string]any{"run_id": "run-1", "title": "Fix login", "body": "b"}); !strings.Contains(text, "pull request #9 for session run-1 is open") || out["write"] != writeDone {
		t.Fatalf("reconciled creation = %q %v", text, out)
	}
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
	if text, _ = run(t, cs, "aether_memory_pin", map[string]any{"workspace_id": "ws-1", "id": "mem-9", "pin": true}); text != "Pinned note mem-9." ||
		called(f, protocol.MethodMemoryPin)[0].params != `{"workspace_id":"ws-1","id":"mem-9","scope":"workspace"}` {
		t.Fatalf("pin = %q, %+v", text, called(f, protocol.MethodMemoryPin))
	}
	if msg, _, failed := try(t, cs, "aether_memory_pin", map[string]any{"workspace_id": "ws-1", "id": "mem-9", "pin": false}); !failed || msg != "memory.unpin: denied" {
		t.Fatalf("denied unpin = %q", msg)
	}
	if _, out = run(t, cs, "aether_memory_search", map[string]any{"workspace_id": "ws-1", "id": "mem-9"}); strings.Contains(trusted(t, out), injection) {
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
	if text, out = run(t, cs, "aether_memory_add", map[string]any{"workspace_id": "ws-1", "kind": "gotcha", "text": "ci  needs\nDocker.", "supersedes": "mem-1"}); out["id"] != "mem-9" || out["duplicate"] != true {
		t.Fatalf("repeated replacement = %q %v", text, out)
	}
	if text, _, failed := try(t, cs, "aether_memory_add", map[string]any{"workspace_id": "ws-1", "kind": "gotcha", "text": "Something else.", "supersedes": "mem-1"}); !failed || !strings.Contains(text, "already replaced") {
		t.Fatalf("a different replacement of a replaced note = %q", text)
	}
	// A declined card stays declined, even for a replacement already made.
	f.errs[protocol.MethodMemoryAdd] = &protocol.Error{Code: protocol.CodeDenied, Message: "memory.add was not approved: the member declined this action"}
	if text, _, failed := try(t, cs, "aether_memory_add", map[string]any{"workspace_id": "ws-1", "kind": "gotcha", "text": "CI needs Docker.", "supersedes": "mem-1"}); !failed ||
		text != "memory.add was not approved: the member declined this action" {
		t.Fatalf("declined replacement = %q", text)
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

	_, out := run(t, cs, "aether_session_changes", map[string]any{"run_id": "run-1", "repo": "ws-2", "include_diff": true})
	if out["next_commits_offset"] != float64(20) || out["next_diff_file"] != float64(10) || out["diff_files"] != float64(12) || out["diff_truncated"] != true || len(out["repos"].([]any)) != 2 {
		t.Fatalf("first page = %v", out)
	}
	if got := called(f, protocol.MethodRunGitDiff); got[0].params != `{"repo":"ws-2","run_id":"run-1"}` {
		t.Fatalf("diff params = %s", got[0].params)
	}
	if len(called(f, protocol.MethodRunEvidenceList)) != 0 {
		t.Fatal("an extra repo read the primary's evidence")
	}
	_, out = run(t, cs, "aether_session_changes", map[string]any{"run_id": "run-1", "commits_offset": 20, "include_diff": true, "diff_file": 10})
	untrusted := out["untrusted"].(map[string]any)
	if _, more := out["next_commits_offset"]; more || len(untrusted["commits"].([]any)) != 20 || len(untrusted["diff"].([]any)) != 2 || out["next_diff_file"] != nil {
		t.Fatalf("last page = %v", out)
	}
	if !strings.HasPrefix(untrusted["diff"].([]any)[0].(string), "diff --git a/fk") {
		t.Fatalf("second diff page starts %q", untrusted["diff"].([]any)[0])
	}

	// A page holds its byte budget whatever the files' runes, and names the
	// files it cut.
	wide := "diff --git a/w b/w\n+" + strings.Repeat("é", 5000)
	f.answers[protocol.MethodRunGitDiff] = returns(protocol.RunGitDiffResult{Output: protocol.RunRepoCommandOutput{Stdout: wide + "\n" + wide + "\n" + wide}})
	_, out = run(t, cs, "aether_session_changes", map[string]any{"run_id": "run-1", "include_diff": true})
	page, size := out["untrusted"].(map[string]any)["diff"].([]any), 0
	for _, d := range page {
		if s := d.(string); len(s) > diffFileText || !strings.HasSuffix(s, "…") {
			t.Fatalf("wide file is %d bytes, ends %q", len(s), s[len(s)-8:])
		}
		size += len(d.(string))
	}
	if size > diffPageText || len(page) != 2 || fmt.Sprint(out["diff_clipped_files"]) != "[0 1]" || out["next_diff_file"] != float64(2) {
		t.Fatalf("wide page = %d bytes, %d files, clipped %v, next %v", size, len(page), out["diff_clipped_files"], out["next_diff_file"])
	}

	// Unresolved conflicts' combined diffs page as files of their own.
	f.answers[protocol.MethodRunGitDiff] = returns(protocol.RunGitDiffResult{Output: protocol.RunRepoCommandOutput{
		Stdout: "diff --git a/x b/x\n+x\ndiff --cc a\n++<<<<<<< ours\ndiff --combined b\n++b"}})
	_, out = run(t, cs, "aether_session_changes", map[string]any{"run_id": "run-1", "include_diff": true})
	if got := fmt.Sprint(out["untrusted"].(map[string]any)["diff"]); out["diff_files"] != float64(3) ||
		got != "[diff --git a/x b/x\n+x diff --cc a\n++<<<<<<< ours diff --combined b\n++b]" {
		t.Fatalf("conflict diff = %v files %s", out["diff_files"], got)
	}
}
