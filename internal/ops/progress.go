package ops

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/3xDevOps/Aether/internal/protocol"
)

const (
	// evidenceFanOut is how many runs an evidence read asks the server about
	// at once, and evidenceLive how many of them it checks on GitHub.
	evidenceFanOut = 4
	evidenceLive   = 5
	notInReports   = "not in the last 100 reports"
	notTracked     = "Aether does not track releases or deployments of this repository"
	sourceRecord   = "aether record"
	sourceGitHub   = "github"
	sourceAether   = "aether git"
)

// untracked is what an evidence read says of releases and deployments,
// which Aether has no source for.
var untracked = map[string]string{"state": "unknown", "reason": notTracked}

// mergedPR is a run's pull request: as Aether recorded it at checked_at,
// or as GitHub showed it then when source is github.
type mergedPR struct {
	protocol.RecordedPullRequest
	Source string `json:"source"`
}

// observation is what GitHub showed of a run's pull request at
// checked_at: its checks and the latest review verdict per reviewer.
type observation struct {
	RunID            string         `json:"run_id"`
	CheckedAt        string         `json:"checked_at"`
	Source           string         `json:"source"`
	PR               *pullRequest   `json:"pr,omitempty"`
	Checks           map[string]int `json:"checks,omitempty"`
	Approvals        int            `json:"approvals"`
	ChangesRequested int            `json:"changes_requested"`
	MergeState       string         `json:"merge_state,omitempty"`
	Partial          bool           `json:"partial,omitempty"`
	Error            string         `json:"error,omitempty"`
	shown            *protocol.RunPullRequest
}

// observe asks GitHub, through the run, about its pull request in repo,
// reusing status when the caller already looked it up; a failed lookup is
// the observation's error.
func observe(ctx context.Context, read Call, runID, repo string, status *protocol.RunPRStatusResult) observation {
	o := observation{RunID: runID, Source: sourceGitHub, CheckedAt: time.Now().UTC().Format(time.RFC3339)}
	var errs []string
	if status == nil {
		status = &protocol.RunPRStatusResult{}
		if err := read(ctx, protocol.MethodRunPRStatus, protocol.RunPRStatusParams{RunID: runID, Repo: repo}, status); err != nil {
			status.Error = err.Error()
		}
	}
	if status.Error != "" {
		errs = append(errs, "pull request status: "+status.Error)
	}
	o.PR, o.shown = prOf(status.PullRequest), status.PullRequest
	var fb protocol.RunPRFeedbackResult
	if err := read(ctx, protocol.MethodRunPRFeedback, protocol.RunPRFeedbackParams{RunID: runID, Repo: repo, Limit: 30}, &fb); err != nil {
		errs = append(errs, "checks and reviews: "+err.Error())
	} else {
		if fb.Error != "" {
			errs = append(errs, "checks and reviews: "+fb.Error)
		}
		if o.PR == nil {
			o.PR, o.shown = prOf(fb.PullRequest), fb.PullRequest
		}
		if fb.PullRequest != nil {
			b := fb.Blockers()
			o.Checks = map[string]int{"total": len(fb.Checks), "failing": b.FailingChecks, "pending": b.PendingChecks}
			o.ChangesRequested, o.MergeState, o.Partial = b.ChangesRequested, fb.MergeStateStatus, b.Partial
			latest := map[string]string{}
			for _, rv := range fb.Reviews {
				if rv.State == "APPROVED" || rv.State == "CHANGES_REQUESTED" || rv.State == "DISMISSED" {
					latest[rv.Author] = rv.State
				}
			}
			for _, state := range latest {
				if state == "APPROVED" {
					o.Approvals++
				}
			}
		}
	}
	o.Error = clip(strings.Join(errs, "; "), maxText)
	return o
}

// refresh replaces each recorded pull request GitHub just showed with what
// it showed.
func refresh(prs []mergedPR, o observation) {
	shown := o.shown
	if shown == nil {
		return
	}
	at, _ := time.Parse(time.RFC3339, o.CheckedAt)
	for i, pr := range prs {
		if pr.Number == shown.Number && (shown.Repository == "" || strings.EqualFold(pr.Repository, shown.Repository)) {
			prs[i].State, prs[i].MergedAt, prs[i].CheckedAt, prs[i].Source = shown.State, shown.MergedAt, at, sourceGitHub
		}
	}
}

func merged(state string) bool { return strings.EqualFold(state, "merged") }

// unmerged is the repo of the newest of prs not merged, and whether there
// is one.
func unmerged(prs []mergedPR) (string, bool) {
	for _, pr := range prs {
		if !merged(pr.State) {
			return pr.Repo, true
		}
	}
	return "", false
}

func recorded(prs []protocol.RecordedPullRequest) []mergedPR {
	out := make([]mergedPR, 0, len(prs))
	for _, pr := range prs {
		out = append(out, mergedPR{RecordedPullRequest: pr, Source: sourceRecord})
	}
	return out
}

// conflicts are where a run's own report disagrees with the other
// evidence. accepted is nil for a run that submits nothing.
func conflicts(r protocol.Run, rep *report, prs []mergedPR, o *observation, accepted *bool) []string {
	out := []string{}
	if r.Status == "merged" && len(prs) > 0 && !slices.ContainsFunc(prs, func(pr mergedPR) bool { return merged(pr.State) }) {
		out = append(out, "closed_as_merged_but_pr_not_merged")
	}
	if rep == nil || rep.Outcome != "success" {
		return out
	}
	if o != nil && o.Checks != nil {
		if o.Checks["failing"] > 0 {
			out = append(out, "success_with_failing_checks")
		}
		if o.Checks["pending"] > 0 {
			out = append(out, "success_with_pending_checks")
		}
		if o.ChangesRequested > 0 {
			out = append(out, "success_with_changes_requested")
		}
	}
	if accepted != nil && !*accepted {
		out = append(out, "success_without_accepted_submission")
	}
	return out
}

// closedAs says what closing the run as merged means: a person said so,
// not GitHub.
func closedAs(r protocol.Run) string {
	if r.Status == "merged" {
		return "a person closed the run as merged; this is not a GitHub merge"
	}
	return ""
}

// fanOut calls f for 0..n-1, evidenceFanOut at a time.
func fanOut(n int, f func(i int)) {
	sem := make(chan struct{}, evidenceFanOut)
	var wg sync.WaitGroup
	for i := range n {
		sem <- struct{}{}
		wg.Go(func() {
			defer func() { <-sem }()
			f(i)
		})
	}
	wg.Wait()
}

// addRunEvidence adds getRun's evidence: the run's recorded pull requests,
// GitHub's checks and reviews of one not merged, and conflicts with its
// latest report. status is getRun's lookup of the run's own repository.
func addRunEvidence(ctx context.Context, read Call, r protocol.Run, recordedPRs []protocol.RecordedPullRequest, reps []report,
	status *protocol.RunPRStatusResult, out map[string]any) {
	prs := recorded(recordedPRs)
	ev := map[string]any{"as_of": time.Now().UTC().Format(time.RFC3339), "pull_requests": prs}
	var o *observation
	if repo, open := unmerged(prs); open {
		if repo != "" {
			status = nil
		}
		got := observe(ctx, read, r.ID, repo, status)
		refresh(prs, got)
		o = &got
		ev["reviewed"] = got
	}
	if why := closedAs(r); why != "" {
		ev["closed_as_merged"] = why
	}
	var rep *report
	if len(reps) > 0 {
		rep = &reps[0]
		ev["report"] = map[string]string{"outcome": rep.Outcome, "at": rep.At}
	} else {
		ev["report"] = notInReports
	}
	ev["conflicts"] = conflicts(r, rep, prs, o, nil)
	out["evidence"], out["released"], out["deployed"] = ev, untracked, untracked
}

type swarmRun struct {
	run      protocol.Run
	prs      []mergedPR
	err      string
	children *childSummary
	childErr string
	observed *observation
}

// childSummary counts a run's descendants by state.
type childSummary struct {
	Total  int            `json:"total"`
	Stale  int            `json:"stale,omitempty"`
	States map[string]int `json:"states"`
}

func summarizeChildren(ctx context.Context, read Call, id string) (*childSummary, string) {
	var res protocol.RunChildrenResult
	if err := read(ctx, protocol.MethodRunChildren, protocol.RunChildrenParams{RunID: id}, &res); err != nil {
		if noChildRuns(err) {
			return nil, ""
		}
		return nil, err.Error()
	}
	if len(res.Children) == 0 {
		return nil, ""
	}
	s := &childSummary{Total: len(res.Children), States: map[string]int{}}
	for _, ch := range res.Children {
		s.States[ch.State]++
		if ch.Stale {
			s.Stale++
		}
	}
	return s, ""
}

// swarmProgress is getSwarm's evidence of what was asked, done, reviewed
// and merged, from mission.show and each of the swarm's runs.
func swarmProgress(ctx context.Context, read Call, res protocol.MissionShowResult, untrusted map[string]any) (map[string]any, string) {
	m := res.Mission
	asOf := time.Now().UTC().Format(time.RFC3339)
	type taskRow struct {
		ID              string   `json:"id"`
		Status          string   `json:"status"`
		Revision        int      `json:"revision"`
		DependsOn       []string `json:"depends_on,omitempty"`
		PlanAcceptedBy  string   `json:"plan_accepted_by,omitempty"`
		PlanAcceptedAt  *string  `json:"plan_accepted_at,omitempty"`
		PendingRevision int      `json:"pending_revision,omitempty"`
		UpdatedAt       string   `json:"updated_at"`
	}
	type taskText struct {
		ID        string `json:"id"`
		Title     string `json:"title"`
		Objective string `json:"objective"`
	}
	tasks, texts, open := []taskRow{}, []taskText{}, []string{}
	for _, t := range res.Tasks {
		if m.Phase == "completed" && t.Status != "done" && t.Status != "abandoned" {
			open = append(open, t.ID)
		}
		if len(tasks) == defaultRows {
			continue
		}
		row := taskRow{ID: t.ID, Status: t.Status, Revision: t.CurrentRevision, UpdatedAt: t.UpdatedAt}
		for _, d := range t.Dependencies {
			row.DependsOn = append(row.DependsOn, d.DependsOnTaskID)
		}
		if rv := t.Revision; rv != nil {
			if rv.AcceptedByMemberID != "" {
				row.PlanAcceptedBy = "member " + rv.AcceptedByMemberID
			} else if rv.AcceptedByRunID != "" {
				row.PlanAcceptedBy = "run " + rv.AcceptedByRunID
			}
			row.PlanAcceptedAt = rv.AcceptedAt
			texts = append(texts, taskText{ID: t.ID, Title: clip(rv.Title, maxReport), Objective: clip(rv.Objective, maxReport)})
		}
		if t.PendingRevision != nil {
			row.PendingRevision = t.PendingRevision.Revision
		}
		tasks = append(tasks, row)
	}
	untrusted["progress_tasks"] = texts

	type submissionRow struct {
		ID              string `json:"id"`
		State           string `json:"state"`
		AcceptedBy      string `json:"accepted_by,omitempty"`
		AcceptedAt      string `json:"accepted_at,omitempty"`
		ScopeViolations int    `json:"scope_violations,omitempty"`
		ScopeDecision   string `json:"scope_disposition,omitempty"`
		CreatedAt       string `json:"created_at"`
	}
	type attemptRow struct {
		TaskID     string         `json:"task_id"`
		AttemptID  string         `json:"attempt_id"`
		RunID      string         `json:"run_id,omitempty"`
		State      string         `json:"state"`
		StartedAt  *string        `json:"started_at,omitempty"`
		FinishedAt *string        `json:"finished_at,omitempty"`
		Submission *submissionRow `json:"submission,omitempty"`
	}
	newest := map[string]protocol.Submission{}
	for _, s := range res.Submissions {
		if prev, ok := newest[s.AttemptID]; !ok || s.CreatedAt > prev.CreatedAt {
			newest[s.AttemptID] = s
		}
	}
	accepted := map[string]bool{}
	attempts := []attemptRow{}
	for _, a := range res.Attempts {
		row := attemptRow{TaskID: a.TaskID, AttemptID: a.ID, RunID: a.RunID, State: a.State, StartedAt: a.StartedAt, FinishedAt: a.FinishedAt}
		if _, seen := accepted[a.RunID]; !seen && a.RunID != "" {
			accepted[a.RunID] = false
		}
		if s, ok := newest[a.ID]; ok {
			sub := &submissionRow{ID: s.ID, State: s.State, ScopeViolations: len(s.ScopeViolations), CreatedAt: s.CreatedAt}
			if acc := s.Acceptance; acc != nil {
				sub.AcceptedBy, sub.AcceptedAt, sub.ScopeDecision = "integrator run "+acc.AcceptedByRunID, acc.AcceptedAt, acc.ScopeDisposition
				if a.RunID != "" {
					accepted[a.RunID] = true
				}
			}
			row.Submission = sub
		}
		if len(attempts) < defaultRows {
			attempts = append(attempts, row)
		}
	}

	ids := []string{}
	if m.CurrentIntegratorRunID != "" {
		ids = append(ids, m.CurrentIntegratorRunID)
	}
	for _, a := range res.Attempts {
		if a.RunID != "" && !slices.Contains(ids, a.RunID) {
			ids = append(ids, a.RunID)
		}
	}
	runsTotal := len(ids)
	ids = ids[:min(len(ids), defaultRows)]
	runs := make([]swarmRun, len(ids))
	fanOut(len(ids), func(i int) {
		var got protocol.RunResult
		if err := read(ctx, protocol.MethodRunGet, protocol.RunIDParams{RunID: ids[i]}, &got); err != nil {
			runs[i] = swarmRun{run: protocol.Run{ID: ids[i]}, err: err.Error()}
		} else {
			runs[i] = swarmRun{run: got.Run, prs: recorded(got.PullRequests)}
		}
		runs[i].children, runs[i].childErr = summarizeChildren(ctx, read, ids[i])
	})
	var live []int
	for i := range runs {
		if _, open := unmerged(runs[i].prs); open && len(live) < evidenceLive {
			live = append(live, i)
		}
	}
	fanOut(len(live), func(j int) {
		sr := &runs[live[j]]
		repo, _ := unmerged(sr.prs)
		o := observe(ctx, read, sr.run.ID, repo, nil)
		refresh(sr.prs, o)
		sr.observed = &o
	})

	reps, repErr := reports(ctx, read, m.WorkspaceID, "")
	latest := map[string]report{}
	for _, rep := range reps {
		if _, ok := latest[rep.RunID]; !ok {
			latest[rep.RunID] = rep
		}
	}
	type mergedRow struct {
		RunID          string     `json:"run_id"`
		RunState       string     `json:"run_state,omitempty"`
		ClosedAsMerged string     `json:"closed_as_merged,omitempty"`
		PullRequests   []mergedPR `json:"pull_requests"`
		NotCheckedLive bool       `json:"not_checked_live,omitempty"`
		Error          string     `json:"error,omitempty"`
	}
	type reportRow struct {
		RunID   string `json:"run_id"`
		Outcome string `json:"outcome,omitempty"`
		At      string `json:"at,omitempty"`
		Missing string `json:"missing,omitempty"`
	}
	type conflictRow struct {
		RunID     string   `json:"run_id"`
		Conflicts []string `json:"conflicts"`
	}
	type childRow struct {
		RunID string `json:"run_id"`
		*childSummary
		Error string `json:"error,omitempty"`
	}
	mergedRows, reviewed, reportRows, childRows, conflictRows := []mergedRow{}, []observation{}, []reportRow{}, []childRow{}, []conflictRow{}
	reportTexts := map[string]report{}
	for _, sr := range runs {
		id := sr.run.ID
		row := mergedRow{RunID: id, RunState: sr.run.Status, ClosedAsMerged: closedAs(sr.run), PullRequests: sr.prs, Error: sr.err}
		if _, open := unmerged(sr.prs); open && sr.observed == nil {
			row.NotCheckedLive = true
		}
		mergedRows = append(mergedRows, row)
		if sr.observed != nil {
			reviewed = append(reviewed, *sr.observed)
		}
		var rep *report
		if got, ok := latest[id]; ok {
			rep = &got
			reportRows = append(reportRows, reportRow{RunID: id, Outcome: got.Outcome, At: got.At})
			reportTexts[id] = report{Summary: clip(got.Summary, maxReport), NextAction: clip(got.NextAction, maxReport), Outcome: got.Outcome, At: got.At}
		} else if repErr == nil {
			reportRows = append(reportRows, reportRow{RunID: id, Missing: notInReports})
		}
		if sr.children != nil || sr.childErr != "" {
			childRows = append(childRows, childRow{RunID: id, childSummary: sr.children, Error: sr.childErr})
		}
		var acc *bool
		if got, worker := accepted[id]; worker {
			acc = &got
		}
		if cs := conflicts(sr.run, rep, sr.prs, sr.observed, acc); len(cs) > 0 {
			conflictRows = append(conflictRows, conflictRow{RunID: id, Conflicts: cs})
		}
	}
	untrusted["progress_reports"] = reportTexts

	type receipt struct {
		CandidateID string `json:"candidate_id"`
		ReceiptID   string `json:"receipt_id"`
		Result      string `json:"result"`
		DeliveredAt string `json:"delivered_at"`
	}
	delivery := map[string]any{"source": sourceAether, "note": "a delivery lands in Aether's own git, not on GitHub"}
	var cands protocol.IntegrationListResult
	if err := read(ctx, protocol.MethodIntegrationList, protocol.IntegrationListParams{WorkspaceID: m.WorkspaceID, MissionID: m.ID, Limit: maxRows}, &cands); err != nil {
		delivery["error"] = err.Error()
	} else {
		receipts := []receipt{}
		for _, cd := range cands.Candidates {
			if rc := cd.DeliveryReceipt; rc != nil {
				receipts = append(receipts, receipt{CandidateID: cd.CandidateID, ReceiptID: rc.ReceiptID, Result: string(rc.Result), DeliveredAt: rc.CreatedAt.UTC().Format(time.RFC3339)})
			}
		}
		delivery["receipts"], delivery["checked_at"] = receipts, asOf
	}

	progress := map[string]any{"as_of": asOf,
		"requested":   map[string]any{"tasks": tasks, "tasks_total": len(res.Tasks)},
		"implemented": map[string]any{"attempts": attempts, "attempts_total": len(res.Attempts)},
		"reviewed":    reviewed, "merged": mergedRows, "aether_delivery": delivery,
		"reports": reportRows, "children": childRows,
		"flags": map[string]any{"open_tasks_in_completed_swarm": open, "report_conflicts": conflictRows}}
	if runsTotal > len(runs) {
		progress["runs_total"] = runsTotal
	}
	if repErr != nil {
		progress["reports_error"] = repErr.Error()
	}
	return progress, fmt.Sprintf(" Evidence: %d open tasks in a completed swarm, %d runs whose reports conflict with the evidence.", len(open), len(conflictRows))
}
