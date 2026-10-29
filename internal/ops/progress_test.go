package ops

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/protocol"
)

var checkedLongAgo = time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)

// shopSwarm is a completed swarm in the shop workspace: task-1 is done by
// run-a, whose submission the integrator accepted; task-2 is still ready,
// worked by run-b, whose submission nobody accepted.
func shopSwarm() protocol.MissionShowResult {
	accepted := "2026-10-26T08:00:00Z"
	return protocol.MissionShowResult{
		Mission: protocol.Mission{ID: "mis-1", WorkspaceID: "ws-1", Objective: "ship the cart", Phase: "completed", CurrentIntegratorRunID: "run-int"},
		Tasks: []protocol.Task{
			{ID: "task-1", Status: "done", CurrentRevision: 1, Revision: &protocol.TaskRevision{Title: "cart api", Objective: injection, AcceptedByMemberID: "mem-ana", AcceptedAt: &accepted}},
			{ID: "task-2", Status: "ready", CurrentRevision: 1, Revision: &protocol.TaskRevision{Title: "cart ui", Objective: "draw the cart", AcceptedByRunID: "run-int"},
				Dependencies: []protocol.TaskDependency{{TaskID: "task-2", DependsOnTaskID: "task-1"}}},
		},
		Attempts: []protocol.Attempt{
			{ID: "att-a", TaskID: "task-1", TaskRevision: 1, RunID: "run-a", State: "completed"},
			{ID: "att-b", TaskID: "task-2", TaskRevision: 1, RunID: "run-b", State: "submitted"},
		},
		Submissions: []protocol.Submission{
			{ID: "sub-a", AttemptID: "att-a", TaskRevision: 1, State: "accepted", ScopeViolations: []string{"web/x.ts"}, CreatedAt: "2026-10-26T07:00:00Z",
				Acceptance: &protocol.Acceptance{TaskRevision: 1, AcceptedByRunID: "run-int", AcceptedAt: accepted, ScopeDisposition: injection}},
			{ID: "sub-b", AttemptID: "att-b", TaskRevision: 1, State: "proposed", CreatedAt: "2026-10-26T07:30:00Z"},
		},
	}
}

// runsAt answers run.get from runs, with each run's recorded pull requests.
func runsAt(runs map[string]protocol.RunResult) func(json.RawMessage) (any, error) {
	return answerTo(func(p protocol.RunIDParams) any {
		res, ok := runs[p.RunID]
		if !ok {
			return protocol.RunResult{Run: protocol.Run{ID: p.RunID, WorkspaceID: "ws-1", Status: "running"}}
		}
		return res
	})
}

func runWith(id, status string, prs ...protocol.RecordedPullRequest) protocol.RunResult {
	return protocol.RunResult{Run: protocol.Run{ID: id, WorkspaceID: "ws-1", Status: status, PendingInputs: nil}, PullRequests: prs}
}

func openPR(n int) protocol.RecordedPullRequest {
	return protocol.RecordedPullRequest{Repository: "acme/shop", Number: n, URL: fmt.Sprintf("https://github.com/acme/shop/pull/%d", n), State: "open", CheckedAt: checkedLongAgo}
}

func ranFor(f *fakeServer, method string) []string {
	var ids []string
	for _, c := range called(f, method) {
		var p struct {
			RunID string `json:"run_id"`
		}
		_ = json.Unmarshal([]byte(c.params), &p)
		ids = append(ids, p.RunID)
	}
	slices.Sort(ids)
	return ids
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

func byRun(t *testing.T, rows any, id string) map[string]any {
	t.Helper()
	for _, r := range rows.([]any) {
		if m := r.(map[string]any); m["run_id"] == id {
			return m
		}
	}
	t.Fatalf("no row for %s in %v", id, rows)
	return nil
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

// A completed swarm's evidence lists what is still open, what each run
// opened and whether GitHub merged it, and where a run's success report
// disagrees with its checks or its unaccepted submission. A run a person
// closed as merged is not a GitHub merge.
func TestSwarmProgress(t *testing.T) {
	f := newFake(t)
	f.answers[protocol.MethodMissionShow] = returns(shopSwarm())
	merged := openPR(1)
	merged.State, merged.MergedAt = "merged", &checkedLongAgo
	f.answers[protocol.MethodRunGet] = runsAt(map[string]protocol.RunResult{
		"run-int": runWith("run-int", "merged", openPR(3)),
		"run-a":   runWith("run-a", "completed", merged),
		"run-b":   runWith("run-b", "completed", openPR(2)),
	})
	f.answers[protocol.MethodRunPRStatus] = answerTo(func(p protocol.RunPRStatusParams) any {
		return shown(map[string]int{"run-int": 3, "run-b": 2}[p.RunID], "open")
	})
	f.answers[protocol.MethodRunPRFeedback] = answerTo(func(p protocol.RunPRFeedbackParams) any {
		fb := feedback("SUCCESS", "FAILURE", "")
		fb.PullRequest.Number = map[string]int{"run-int": 3, "run-b": 2}[p.RunID]
		return fb
	})
	f.answers[protocol.MethodWorkspaceTimeline] = timeline(t, reportEvent("run-a", "success", "done"), reportEvent("run-b", "success", injection))
	f.answers[protocol.MethodIntegrationList] = returns(protocol.IntegrationListResult{Candidates: []protocol.CandidateSummary{
		{CandidateID: "cand-1", TargetRef: injection, DeliveryReceipt: &protocol.DeliveryReceipt{ReceiptID: "rcpt-1", Result: protocol.DeliveryResultLanded, CreatedAt: checkedLongAgo}}}})
	cs := connectorSession(t, f)

	_, out := run(t, cs, "aether_get_swarm", map[string]any{"swarm_id": "mis-1"})
	if out["progress"] != nil || len(called(f, protocol.MethodRunPRStatus)) != 0 {
		t.Fatalf("aether_get_swarm without evidence = %v, made %d pull request lookups", out, len(called(f, protocol.MethodRunPRStatus)))
	}

	text, out := run(t, cs, "aether_get_swarm", map[string]any{"swarm_id": "mis-1", "evidence": true})
	p := out["progress"].(map[string]any)
	if !strings.Contains(text, "1 open tasks in a completed swarm, 2 runs whose reports conflict") || strings.Contains(text, "Not confirmed") {
		t.Errorf("summary = %q", text)
	}
	if got := dig(t, p, "flags", "open_tasks_in_completed_swarm"); jsonOf(t, got) != `["task-2"]` {
		t.Errorf("open tasks = %v, want task-2", got)
	}
	task2 := dig(t, p, "requested", "tasks", 1).(map[string]any)
	if jsonOf(t, task2["depends_on"]) != `["task-1"]` || task2["plan_accepted_by"] != "run run-int" || dig(t, p, "requested", "tasks", 0, "plan_accepted_by") != "member mem-ana" {
		t.Errorf("requested = %v", dig(t, p, "requested"))
	}
	sub := dig(t, p, "implemented", "attempts", 0, "submission").(map[string]any)
	if sub["accepted"] != true || sub["accepted_for_revision"] != float64(1) || sub["accepted_by"] != "integrator run run-int" || sub["scope_violations"] != float64(1) || dig(t, p, "implemented", "attempts", 1, "submission", "accepted_by") != nil {
		t.Errorf("implemented = %v", dig(t, p, "implemented"))
	}
	if got := ranFor(f, protocol.MethodRunPRStatus); !slices.Equal(got, []string{"run-b", "run-int"}) {
		t.Errorf("live pull request lookups for %v, want only the runs with one not merged", got)
	}
	ma := byRun(t, p["merged"], "run-a")
	if pr := dig(t, ma, "pull_requests", 0).(map[string]any); pr["state"] != "merged" || pr["source"] != sourceRecord || pr["checked_at"] != "2026-10-20T09:00:00Z" {
		t.Errorf("run-a's merged pull request = %v, want Aether's record with when it was checked", pr)
	}
	mi := byRun(t, p["merged"], "run-int")
	if mi["closed_as_merged"] == nil || mi["role"] != "integrator" || dig(t, mi, "pull_requests", 0, "source") != sourceGitHub || dig(t, mi, "pull_requests", 0, "state") != "open" {
		t.Errorf("run-int closed as merged = %v", mi)
	}
	rb := byRun(t, p["reviewed"], "run-b")
	if jsonOf(t, rb["checks"]) != `{"failing":1,"pending":1,"total":3}` || rb["approvals"] != float64(1) || rb["checked_at"] == nil {
		t.Errorf("run-b reviewed = %v", rb)
	}
	flags := dig(t, p, "flags", "report_conflicts")
	if got := jsonOf(t, byRun(t, flags, "run-b")["conflicts"]); got != `["success_with_failing_checks","success_with_pending_checks","success_without_accepted_submission"]` {
		t.Errorf("run-b conflicts = %s", got)
	}
	if got := jsonOf(t, byRun(t, flags, "run-int")["conflicts"]); got != `["closed_as_merged_but_pr_not_merged"]` {
		t.Errorf("run-int conflicts = %s", got)
	}
	if byRun(t, p["reports"], "run-int")["missing"] != notInReports || byRun(t, p["reports"], "run-a")["outcome"] != "success" {
		t.Errorf("reports = %v", p["reports"])
	}
	if dig(t, p, "aether_delivery", "source") != sourceAether || dig(t, p, "aether_delivery", "receipts", 0, "receipt_id") != "rcpt-1" {
		t.Errorf("delivery = %v", p["aether_delivery"])
	}
	if jsonOf(t, out["released"]) != `{"reason":"`+notTracked+`","state":"unknown"}` || jsonOf(t, out["deployed"]) != jsonOf(t, out["released"]) {
		t.Errorf("released %v, deployed %v", out["released"], out["deployed"])
	}
	if strings.Contains(trusted(t, out), injection) {
		t.Errorf("agent text outside untrusted: %s", trusted(t, out))
	}
	if dig(t, out, "untrusted", "progress_tasks", 0, "objective") != injection || dig(t, out, "untrusted", "scope_dispositions", "sub-a") != injection {
		t.Errorf("task objectives = %v", dig(t, out, "untrusted", "progress_tasks"))
	}
	if len(called(f, protocol.MethodWorkspaceTimeline)) != 1 {
		t.Errorf("workspace.timeline called %d times, want once", len(called(f, protocol.MethodWorkspaceTimeline)))
	}
}

// At most five runs are checked on GitHub per read; the rest keep Aether's
// record and when it was made. An unreachable GitHub is the observation's
// error, not a guess.
func TestSwarmProgressBoundsLiveLookups(t *testing.T) {
	f := newFake(t)
	show := shopSwarm()
	show.Mission.Phase, show.Mission.CurrentIntegratorRunID = "active", ""
	show.Attempts, show.Submissions = nil, nil
	runs := map[string]protocol.RunResult{}
	for i := range 7 {
		id := fmt.Sprintf("run-%d", i)
		show.Attempts = append(show.Attempts, protocol.Attempt{ID: "att-" + id, TaskID: "task-1", RunID: id, State: "running"})
		runs[id] = runWith(id, "running", openPR(i+1))
	}
	f.answers[protocol.MethodMissionShow] = returns(show)
	f.answers[protocol.MethodRunGet] = runsAt(runs)
	var cands protocol.IntegrationListResult
	for i := range defaultRows + 2 {
		cands.Candidates = append(cands.Candidates, protocol.CandidateSummary{CandidateID: fmt.Sprintf("cand-%d", i),
			DeliveryReceipt: &protocol.DeliveryReceipt{ReceiptID: fmt.Sprintf("rcpt-%d", i), Result: protocol.DeliveryResultLanded, CreatedAt: checkedLongAgo}})
	}
	f.answers[protocol.MethodIntegrationList] = returns(cands)
	f.errs[protocol.MethodRunPRStatus] = errors.New("github.com: dial tcp: i/o timeout")
	f.answers[protocol.MethodRunPRFeedback] = returns(protocol.RunPRFeedbackResult{Error: "gh: could not reach api.github.com"})
	f.errs[protocol.MethodWorkspaceTimeline] = errors.New("timeline: database is locked")
	text, out := run(t, connectorSession(t, f), "aether_get_swarm", map[string]any{"swarm_id": "mis-1", "evidence": true})
	p := out["progress"].(map[string]any)
	if !strings.Contains(text, "Not confirmed: 5 GitHub lookups returned an error, 2 runs have pull requests not checked live, reports could not be read.") {
		t.Errorf("summary = %q, want what it could not confirm", text)
	}
	if !strings.Contains(p["reports_error"].(string), "database is locked") {
		t.Errorf("reports_error = %v", p["reports_error"])
	}
	if n := len(dig(t, p, "aether_delivery", "receipts").([]any)); n != defaultRows || dig(t, p, "aether_delivery", "receipts_total") != float64(defaultRows+2) {
		t.Errorf("%d receipts, total %v", n, dig(t, p, "aether_delivery", "receipts_total"))
	}
	if n := len(called(f, protocol.MethodRunPRStatus)); n != evidenceLive {
		t.Fatalf("%d live lookups, want %d", n, evidenceLive)
	}
	skipped := 0
	for _, row := range p["merged"].([]any) {
		pr := dig(t, row, "pull_requests", 0).(map[string]any)
		if pr["source"] != sourceRecord || pr["checked_at"] != "2026-10-20T09:00:00Z" {
			t.Errorf("pull request %v, want Aether's record and its checked_at whether or not the lookup failed", pr)
		}
		if pr["not_checked_live"] == true {
			skipped++
		}
	}
	if skipped != 2 {
		t.Errorf("%d runs not checked live, want 2", skipped)
	}
	for _, row := range p["reviewed"].([]any) {
		e := row.(map[string]any)["error"].(string)
		if !strings.Contains(e, "i/o timeout") || !strings.Contains(e, "could not reach api.github.com") || row.(map[string]any)["checks"] != nil {
			t.Errorf("unreachable GitHub observation = %v", row)
		}
	}
	if len(dig(t, p, "flags", "report_conflicts").([]any)) != 0 || len(dig(t, p, "flags", "open_tasks_in_completed_swarm").([]any)) != 0 {
		t.Errorf("flags = %v, want none for an active swarm with no reports", p["flags"])
	}
}

// One run's evidence: its recorded pull requests, GitHub's checks of the
// open one, its children with their staleness and reports, and the
// conflict between a success report and failing checks.
func TestRunEvidence(t *testing.T) {
	f := newFake(t)
	f.answers[protocol.MethodRunGet] = returns(runWith("run-1", "completed", openPR(2)))
	f.answers[protocol.MethodRunPRStatus] = returns(shown(2, "open"))
	f.answers[protocol.MethodRunPRFeedback] = returns(feedback("FAILURE"))
	f.answers[protocol.MethodWorkspaceTimeline] = timeline(t, reportEvent("run-1", "success", "all green"))
	f.answers[protocol.MethodRunChildren] = returns(protocol.RunChildrenResult{Children: []protocol.Child{
		{RunID: "run-c1", Depth: 1, State: "succeeded", Stale: true, Report: &protocol.ChildReport{Outcome: "success", Summary: injection, At: "2026-10-26T09:00:00Z"}},
		{RunID: "run-c2", Depth: 1, State: "running"},
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
	if c1["stale"] != true || dig(t, c1, "report", "outcome") != "success" || dig(t, out, "untrusted", "child_reports", "run-c1") != injection || strings.Contains(trusted(t, out), injection) {
		t.Errorf("children = %v, untrusted %v", out["children"], out["untrusted"])
	}
	if out["released"].(map[string]any)["state"] != "unknown" {
		t.Errorf("released = %v", out["released"])
	}

	f.answers[protocol.MethodRunGet] = returns(runWith("run-1", "running"))
	f.answers[protocol.MethodRunPRStatus] = returns(shown(4, "MERGED"))
	_, out = run(t, cs, "aether_get_run", map[string]any{"run_id": "run-1", "evidence": true})
	if pr := dig(t, out, "evidence", "pull_requests", 0).(map[string]any); pr["number"] != float64(4) || pr["source"] != sourceGitHub || pr["checked_at"] == nil {
		t.Errorf("a pull request GitHub shows but Aether has not recorded = %v", pr)
	}

	merged := openPR(2)
	merged.State = "merged"
	f.answers[protocol.MethodRunGet] = returns(runWith("run-1", "merged", merged))
	f.answers[protocol.MethodWorkspaceTimeline] = timeline(t)
	f.calls = nil
	_, out = run(t, cs, "aether_get_run", map[string]any{"run_id": "run-1", "evidence": true})
	ev = out["evidence"].(map[string]any)
	if len(called(f, protocol.MethodRunPRFeedback)) != 0 || ev["reviewed"] != nil || ev["report"] != notInReports ||
		ev["closed_as_merged"] == nil || jsonOf(t, ev["conflicts"]) != `[]` {
		t.Errorf("a merged run's evidence = %v; want no live checks and no conflict", ev)
	}
}

// A swarm run with no recorded pull request is looked up too, since its
// agent may have opened one with gh; checks are not paired with a pull
// request that moved between the two lookups.
func TestSwarmProgressFindsUnrecordedPullRequests(t *testing.T) {
	f := newFake(t)
	show := shopSwarm()
	show.Mission.CurrentIntegratorRunID = ""
	f.answers[protocol.MethodMissionShow] = returns(show)
	f.answers[protocol.MethodRunGet] = runsAt(map[string]protocol.RunResult{"run-a": runWith("run-a", "running"), "run-b": runWith("run-b", "running")})
	f.answers[protocol.MethodIntegrationList] = returns(protocol.IntegrationListResult{})
	f.answers[protocol.MethodRunPRStatus] = answerTo(func(p protocol.RunPRStatusParams) any {
		if p.RunID == "run-b" {
			return protocol.RunPRStatusResult{Error: "no pull request for branch aether/run-b"}
		}
		res := shown(9, "OPEN")
		res.PullRequest.HeadOID = "aaa"
		return res
	})
	fb := feedback("FAILURE")
	fb.PullRequest.Number, fb.PullRequest.HeadOID = 9, "bbb"
	f.answers[protocol.MethodRunPRFeedback] = answerTo(func(p protocol.RunPRFeedbackParams) any {
		if p.RunID == "run-b" {
			return protocol.RunPRFeedbackResult{Error: "no pull request for branch aether/run-b"}
		}
		return fb
	})
	_, out := run(t, connectorSession(t, f), "aether_get_swarm", map[string]any{"swarm_id": "mis-1", "evidence": true})
	p := out["progress"].(map[string]any)
	ma := byRun(t, p["merged"], "run-a")
	if dig(t, ma, "pull_requests", 0, "number") != float64(9) || dig(t, ma, "pull_requests", 0, "source") != sourceGitHub {
		t.Errorf("run-a's pull request opened with gh = %v", ma)
	}
	ra := byRun(t, p["reviewed"], "run-a")
	if e, _ := ra["error"].(string); !strings.Contains(e, "changed between lookups") || dig(t, ra, "pr", "head_oid") != "bbb" {
		t.Errorf("checks of a moved pull request = %v, want them with the head they belong to and the move named", ra)
	}
	if mb := byRun(t, p["merged"], "run-b"); !strings.Contains(mb["pr_lookup_error"].(string), "no pull request") || len(mb["pull_requests"].([]any)) != 0 {
		t.Errorf("run-b with no pull request = %v", mb)
	}
}

// An acceptance of an earlier task revision does not count once the task
// moves on; superseded attempts and abandoned tasks raise no conflict. The
// integrator generation names earlier integrators that decided a
// submission, and a finished run with no recorded pull request is not
// looked up.
func TestSwarmProgressAcceptanceFollowsRevision(t *testing.T) {
	f := newFake(t)
	show := shopSwarm()
	show.Mission.Phase, show.Mission.CurrentIntegratorRunID, show.Mission.IntegratorGeneration = "active", "run-int2", 3
	show.Tasks[0].CurrentRevision = 2
	abandoned := "2026-10-26T10:00:00Z"
	show.Tasks[1].Status, show.Tasks[1].AbandonedAt = "abandoned", &abandoned
	show.Attempts = append(show.Attempts, protocol.Attempt{ID: "att-c", TaskID: "task-1", TaskRevision: 1, RunID: "run-c", State: "superseded"})
	f.answers[protocol.MethodMissionShow] = returns(show)
	f.answers[protocol.MethodRunGet] = runsAt(map[string]protocol.RunResult{
		"run-int": runWith("run-int", "completed"), "run-int2": runWith("run-int2", "running"),
		"run-a": runWith("run-a", "completed"), "run-b": runWith("run-b", "completed"), "run-c": runWith("run-c", "completed"),
	})
	f.answers[protocol.MethodRunPRStatus] = returns(protocol.RunPRStatusResult{Identity: "ana", Error: "no pull request for branch aether/run-int2"})
	f.answers[protocol.MethodRunPRFeedback] = returns(protocol.RunPRFeedbackResult{Identity: "ana", Error: "no pull request for branch aether/run-int2"})
	f.answers[protocol.MethodIntegrationList] = returns(protocol.IntegrationListResult{})
	f.answers[protocol.MethodWorkspaceTimeline] = timeline(t, reportEvent("run-a", "success", "done"), reportEvent("run-b", "success", "done"),
		reportEvent("run-c", "success", "done"))
	_, out := run(t, connectorSession(t, f), "aether_get_swarm", map[string]any{"swarm_id": "mis-1", "evidence": true})
	p := out["progress"].(map[string]any)

	sub := dig(t, p, "implemented", "attempts", 0, "submission").(map[string]any)
	if sub["accepted"] != false || sub["accepted_for_revision"] != float64(1) {
		t.Errorf("submission accepted for revision 1 of a task now at 2 = %v", sub)
	}
	flags := dig(t, p, "flags", "report_conflicts")
	if got := jsonOf(t, byRun(t, flags, "run-a")["conflicts"]); got != `["success_without_accepted_submission"]` {
		t.Errorf("run-a conflicts = %s", got)
	}
	if len(flags.([]any)) != 1 {
		t.Errorf("conflicts = %v, want none for the abandoned task's run-b or the superseded run-c", flags)
	}
	if byRun(t, p["merged"], "run-int")["role"] != "earlier integrator" || byRun(t, p["merged"], "run-int2")["role"] != "integrator" {
		t.Errorf("merged = %v", p["merged"])
	}
	if note, _ := p["integrator_note"].(string); !strings.Contains(note, "generation 3: 1 earlier integrator runs") {
		t.Errorf("integrator_note = %q", note)
	}
	if got := ranFor(f, protocol.MethodRunPRStatus); !slices.Equal(got, []string{"run-int2"}) {
		t.Errorf("live lookups for %v, want only the unfinished integrator", got)
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
	f.answers[protocol.MethodRunGet] = returns(runWith("run-1", "completed", openPR(2), docs))
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

	f.answers[protocol.MethodRunGet] = returns(runWith("run-1", "running", openPR(2)))
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

// Work on a task abandoned after its acceptance is not accepted; a review
// list GitHub cut off leaves approvals unknown, since it may omit a later
// verdict; and a pull request found in an extra repository keeps that
// repository for follow-up lookups.
func TestEvidenceAbandonedPartialAndExtraRepo(t *testing.T) {
	f := newFake(t)
	show := shopSwarm()
	abandoned := "2026-10-26T10:00:00Z"
	show.Tasks[0].Status, show.Tasks[0].AbandonedAt = "abandoned", &abandoned
	f.answers[protocol.MethodMissionShow] = returns(show)
	f.answers[protocol.MethodRunGet] = runsAt(map[string]protocol.RunResult{})
	f.answers[protocol.MethodRunPRStatus] = returns(protocol.RunPRStatusResult{Identity: "ana", Error: "no pull request"})
	f.answers[protocol.MethodRunPRFeedback] = returns(protocol.RunPRFeedbackResult{Identity: "ana", Error: "no pull request"})
	f.answers[protocol.MethodIntegrationList] = returns(protocol.IntegrationListResult{})
	cs := connectorSession(t, f)
	_, out := run(t, cs, "aether_get_swarm", map[string]any{"swarm_id": "mis-1", "evidence": true})
	if sub := dig(t, out, "progress", "implemented", "attempts", 0, "submission").(map[string]any); sub["accepted"] != false {
		t.Errorf("submission of an abandoned task = %v, want not accepted", sub)
	}

	extra := openPR(2)
	extra.Repo = "ws-extra"
	f.answers[protocol.MethodRunGet] = returns(runWith("run-1", "running", extra))
	f.answers[protocol.MethodRunPRStatus] = answerTo(func(p protocol.RunPRStatusParams) any {
		if p.Repo != "ws-extra" {
			return protocol.RunPRStatusResult{Identity: "ana", Error: "no pull request"}
		}
		return shown(5, "OPEN")
	})
	cut := feedback("SUCCESS")
	cut.PullRequest.Number, cut.Truncated = 5, true
	f.answers[protocol.MethodRunPRFeedback] = returns(cut)
	_, out = run(t, cs, "aether_get_run", map[string]any{"run_id": "run-1", "evidence": true})
	ev := out["evidence"].(map[string]any)
	if r := dig(t, ev, "reviewed").(map[string]any); r["approvals"] != nil || r["partial"] != true {
		t.Errorf("reviewed from a cut-off review list = %v, want approvals unknown and partial", r)
	}
	if pr := dig(t, ev, "pull_requests", 0).(map[string]any); pr["number"] != float64(5) || pr["repo"] != "ws-extra" {
		t.Errorf("pull request found in the extra repository = %v, want its repo kept", pr)
	}
}
