package ops

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/protocol"
)

var checkedLongAgo = time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)

func runWith(status string, prs ...protocol.RecordedPullRequest) protocol.RunResult {
	return protocol.RunResult{Run: protocol.Run{ID: "run-1", WorkspaceID: "ws-1", Status: status, PendingInputs: nil}, PullRequests: prs}
}

func openPR(n int) protocol.RecordedPullRequest {
	return protocol.RecordedPullRequest{Repository: "acme/shop", Number: n, URL: fmt.Sprintf("https://github.com/acme/shop/pull/%d", n), State: "open", CheckedAt: checkedLongAgo}
}

func dig(t *testing.T, v any, path ...any) any {
	t.Helper()
	for _, p := range path {
		switch k := p.(type) {
		case string:
			m, ok := v.(map[string]any)
			if !ok {
				t.Fatalf("%v: not an object at %q", path, k)
			}
			v = m[k]
		case int:
			s, ok := v.([]any)
			if !ok || k >= len(s) {
				t.Fatalf("%v: no item %d in %v", path, k, v)
			}
			v = s[k]
		}
	}
	return v
}

// shown is what a live lookup as the GitHub login ana returns.
func shown(n int, state string) protocol.RunPRStatusResult {
	return protocol.RunPRStatusResult{Identity: "ana", PullRequest: &protocol.RunPullRequest{Number: n, State: state, Repository: "acme/shop"}}
}

func feedback(conclusions ...string) protocol.RunPRFeedbackResult {
	res := protocol.RunPRFeedbackResult{Identity: "ana", PullRequest: &protocol.RunPullRequest{Number: 2, State: "open", Repository: "acme/shop"},
		Reviews: []protocol.RunPRReview{{Author: "bo", State: "APPROVED"}}}
	for i, c := range conclusions {
		res.Checks = append(res.Checks, protocol.RunPRCheck{Name: fmt.Sprintf("check-%d", i), Conclusion: c})
	}
	return res
}

// One run's evidence: its recorded pull requests, GitHub's checks of the
// open one, its children with their staleness and reports, and the
// conflict between a success report and failing checks.
func TestRunEvidence(t *testing.T) {
	f := newFake(t)
	f.answers[protocol.MethodRunGet] = returns(runWith("completed", openPR(2)))
	f.answers[protocol.MethodRunPRStatus] = returns(shown(2, "open"))
	f.answers[protocol.MethodRunPRFeedback] = returns(feedback("FAILURE"))
	f.answers[protocol.MethodWorkspaceTimeline] = timeline(t, reportEvent("run-1", "success", "all green"))
	f.answers[protocol.MethodRunChildren] = returns(protocol.RunChildrenResult{Children: []protocol.Child{
		{RunID: "run-c1", State: "succeeded", Report: &protocol.ChildReport{Outcome: "success", Summary: injection, At: "2026-10-26T09:00:00Z"}},
		{RunID: "run-c2", State: "running"},
	}})
	cs := connectorSession(t, f)

	text, out := run(t, cs, "aether_get_run", map[string]any{"run_id": "run-1", "evidence": true})
	ev := out["evidence"].(map[string]any)
	if jsonOf(t, ev["conflicts"]) != `["success_with_failing_checks"]` || !strings.Contains(text, "success_with_failing_checks") {
		t.Errorf("conflicts = %v, summary %q", ev["conflicts"], text)
	}
	if n := len(called(f, protocol.MethodRunPRStatus)); n != 1 {
		t.Errorf("%d pull request status lookups, want getRun's one reused", n)
	}
	if dig(t, ev, "reviewed", "checks", "failing") != float64(1) || dig(t, ev, "pull_requests", 0, "source") != sourceGitHub || dig(t, ev, "report", "outcome") != "success" {
		t.Errorf("evidence = %v", ev)
	}
	c1 := dig(t, out, "children", 0).(map[string]any)
	if dig(t, c1, "report", "outcome") != "success" || dig(t, out, "untrusted", "child_reports", "run-c1") != injection || strings.Contains(trusted(t, out), injection) {
		t.Errorf("children = %v, untrusted %v", out["children"], out["untrusted"])
	}
	if out["released"].(map[string]any)["state"] != "unknown" {
		t.Errorf("released = %v", out["released"])
	}

	f.answers[protocol.MethodRunGet] = returns(runWith("running"))
	f.answers[protocol.MethodRunPRStatus] = returns(shown(4, "MERGED"))
	_, out = run(t, cs, "aether_get_run", map[string]any{"run_id": "run-1", "evidence": true})
	if pr := dig(t, out, "evidence", "pull_requests", 0).(map[string]any); pr["number"] != float64(4) || pr["source"] != sourceGitHub || pr["checked_at"] == nil {
		t.Errorf("a pull request GitHub shows but Aether has not recorded = %v", pr)
	}

	merged := openPR(2)
	merged.State = "merged"
	f.answers[protocol.MethodRunGet] = returns(runWith("merged", merged))
	f.answers[protocol.MethodWorkspaceTimeline] = timeline(t)
	f.calls = nil
	_, out = run(t, cs, "aether_get_run", map[string]any{"run_id": "run-1", "evidence": true})
	ev = out["evidence"].(map[string]any)
	if len(called(f, protocol.MethodRunPRFeedback)) != 0 || ev["reviewed"] != nil || ev["report"] != notInReports ||
		ev["closed_as_merged"] == nil || jsonOf(t, ev["conflicts"]) != `[]` {
		t.Errorf("a merged run's evidence = %v; want no live checks and no conflict", ev)
	}
}

// A finished run's lookup can answer from Aether's record without asking
// GitHub; the pull request then keeps its source and checked_at, the
// failure is carried, and the summary says what was not confirmed. A
// pull request in another repository is marked not checked live, and one
// GitHub shows in a different repository does not replace a recorded one
// with the same number.
func TestRunEvidenceFromRecordWithoutGitHub(t *testing.T) {
	f := newFake(t)
	docs := openPR(5)
	docs.Repo, docs.Repository = "ws-docs", "acme/docs"
	f.answers[protocol.MethodRunGet] = returns(runWith("completed", openPR(2), docs))
	recordOnly := shown(2, "open")
	recordOnly.Identity = ""
	f.answers[protocol.MethodRunPRStatus] = returns(recordOnly)
	f.answers[protocol.MethodRunPRFeedback] = returns(protocol.RunPRFeedbackResult{PullRequest: recordOnly.PullRequest,
		Error: "checks and review feedback need the run's environment, which is gone: no live environment"})
	f.answers[protocol.MethodWorkspaceTimeline] = timeline(t, reportEvent("run-1", "success", "shipped"))
	cs := connectorSession(t, f)

	text, out := run(t, cs, "aether_get_run", map[string]any{"run_id": "run-1", "evidence": true})
	ev := out["evidence"].(map[string]any)
	pr := dig(t, ev, "pull_requests", 0).(map[string]any)
	if pr["source"] != sourceRecord || pr["checked_at"] != "2026-10-20T09:00:00Z" || pr["not_checked_live"] != nil {
		t.Errorf("recorded pull request = %v, want Aether's record and its checked_at", pr)
	}
	if e, _ := dig(t, ev, "reviewed", "error").(string); !strings.Contains(e, "environment, which is gone") || dig(t, ev, "reviewed", "checks") != nil {
		t.Errorf("reviewed = %v", ev["reviewed"])
	}
	if dig(t, ev, "pull_requests", 1, "not_checked_live") != true {
		t.Errorf("the other repository's pull request = %v", dig(t, ev, "pull_requests", 1))
	}
	if !strings.Contains(text, "Not confirmed: 1 GitHub lookups returned an error, 1 runs have pull requests not checked live.") {
		t.Errorf("summary = %q", text)
	}

	f.answers[protocol.MethodRunGet] = returns(runWith("running", openPR(2)))
	other := shown(2, "open")
	other.PullRequest.Repository = "acme/other"
	f.answers[protocol.MethodRunPRStatus] = returns(other)
	f.answers[protocol.MethodRunPRFeedback] = returns(protocol.RunPRFeedbackResult{Identity: "ana", PullRequest: other.PullRequest})
	_, out = run(t, cs, "aether_get_run", map[string]any{"run_id": "run-1", "evidence": true})
	prs := dig(t, out, "evidence", "pull_requests").([]any)
	if len(prs) != 2 || dig(t, prs, 1, "repository") != "acme/shop" || dig(t, prs, 1, "source") != sourceRecord {
		t.Errorf("pull requests = %v, want acme/other#2 beside the recorded acme/shop#2", prs)
	}
}

// A review list GitHub cut off leaves approvals unknown, since it may omit
// a later verdict; and a pull request found in an extra repository keeps
// that repository for follow-up lookups.
func TestEvidencePartialAndExtraRepo(t *testing.T) {
	f := newFake(t)
	cs := connectorSession(t, f)
	extra := openPR(2)
	extra.Repo = "ws-extra"
	f.answers[protocol.MethodRunGet] = returns(runWith("running", extra))
	f.answers[protocol.MethodRunPRStatus] = answerTo(func(p protocol.RunPRStatusParams) any {
		if p.Repo != "ws-extra" {
			return protocol.RunPRStatusResult{Identity: "ana", Error: "no pull request"}
		}
		return shown(5, "OPEN")
	})
	cut := feedback("SUCCESS")
	cut.PullRequest.Number, cut.Truncated = 5, true
	f.answers[protocol.MethodRunPRFeedback] = returns(cut)
	_, out := run(t, cs, "aether_get_run", map[string]any{"run_id": "run-1", "evidence": true})
	ev := out["evidence"].(map[string]any)
	if r := dig(t, ev, "reviewed").(map[string]any); r["approvals"] != nil || r["partial"] != true {
		t.Errorf("reviewed from a cut-off review list = %v, want approvals unknown and partial", r)
	}
	if pr := dig(t, ev, "pull_requests", 0).(map[string]any); pr["number"] != float64(5) || pr["repo"] != "ws-extra" {
		t.Errorf("pull request found in the extra repository = %v, want its repo kept", pr)
	}
}
