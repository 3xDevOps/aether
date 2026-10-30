package ops

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/protocol"
)

const (
	notInReports = "not in the last 100 reports"
	notTracked   = "Aether does not track releases or deployments of this repository"
	sourceRecord = "aether record"
	sourceGitHub = "github"
	sourceAether = "aether git"
)

// untracked is what an evidence read says of releases and deployments,
// which Aether has no source for.
var untracked = map[string]string{"state": "unknown", "reason": notTracked}

// mergedPR is a run's pull request: as Aether recorded it at checked_at,
// or as GitHub showed it then when source is github.
type mergedPR struct {
	protocol.RecordedPullRequest
	Source         string `json:"source"`
	NotCheckedLive bool   `json:"not_checked_live,omitempty"`
}

// observation is what GitHub showed of a run's pull request at
// checked_at: its checks and the latest review verdict per reviewer.
type observation struct {
	RunID            string         `json:"run_id"`
	CheckedAt        string         `json:"checked_at"`
	Source           string         `json:"source"`
	PR               *pullRequest   `json:"pr,omitempty"`
	Checks           map[string]int `json:"checks,omitempty"`
	Approvals        *int           `json:"approvals,omitempty"`
	ChangesRequested int            `json:"changes_requested"`
	MergeState       string         `json:"merge_state,omitempty"`
	Partial          bool           `json:"partial,omitempty"`
	Error            string         `json:"error,omitempty"`
	repo             string
	// shown is the pull request an errorless lookup with a GitHub login
	// returned; a failed or recorded-only lookup leaves it nil.
	shown *protocol.RunPullRequest
}

// liveShown is pr when the lookup that returned it reached GitHub as
// identity without an error. A finished run's lookup may answer from
// Aether's record without asking GitHub, and then has no identity.
func liveShown(identity, lookupErr string, pr *protocol.RunPullRequest) *protocol.RunPullRequest {
	if identity == "" || lookupErr != "" {
		return nil
	}
	return pr
}

// observe asks GitHub, through the run, about its pull request in repo,
// reusing status when the caller already looked it up; a failed lookup is
// the observation's error.
func observe(ctx context.Context, read Call, runID, repo string, status *protocol.RunPRStatusResult) observation {
	o := observation{RunID: runID, Source: sourceGitHub, CheckedAt: time.Now().UTC().Format(time.RFC3339), repo: repo}
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
	o.PR, o.shown = prOf(status.PullRequest), liveShown(status.Identity, status.Error, status.PullRequest)
	var fb protocol.RunPRFeedbackResult
	if err := read(ctx, protocol.MethodRunPRFeedback, protocol.RunPRFeedbackParams{RunID: runID, Repo: repo, Limit: 30}, &fb); err != nil {
		errs = append(errs, "checks and reviews: "+err.Error())
	} else {
		if fb.Error != "" {
			errs = append(errs, "checks and reviews: "+fb.Error)
		}
		if shown := liveShown(fb.Identity, fb.Error, fb.PullRequest); shown != nil {
			if prior := o.shown; prior != nil && (prior.Number != shown.Number || !strings.EqualFold(prior.Repository, shown.Repository) || prior.HeadOID != shown.HeadOID) {
				errs = append(errs, fmt.Sprintf("the pull request changed between lookups, from #%d at %s to #%d at %s; read again", prior.Number, prior.HeadOID, shown.Number, shown.HeadOID))
			}
			o.PR, o.shown = prOf(shown), shown
			b := fb.Blockers()
			o.Checks = map[string]int{"total": len(fb.Checks), "failing": b.FailingChecks, "pending": b.PendingChecks}
			o.ChangesRequested, o.MergeState, o.Partial = b.ChangesRequested, fb.MergeStateStatus, b.Partial
			// A cut-off review list may omit a reviewer's later verdict, so its
			// approvals are not current; Partial says so.
			if !b.Partial {
				latest := map[string]string{}
				for _, rv := range fb.Reviews {
					if rv.State == "APPROVED" || rv.State == "CHANGES_REQUESTED" || rv.State == "DISMISSED" {
						latest[rv.Author] = rv.State
					}
				}
				approvals := 0
				for _, state := range latest {
					if state == "APPROVED" {
						approvals++
					}
				}
				o.Approvals = &approvals
			}
		}
	}
	o.Error = clip(strings.Join(errs, "; "), maxText)
	return o
}

// include puts the pull request GitHub showed at into prs: in place of the
// recorded one, or first when Aether has not recorded it, as for one opened
// with gh in the run. A recorded merge is final and stays as recorded.
func include(prs []mergedPR, shown *protocol.RunPullRequest, repo string, at time.Time) []mergedPR {
	if shown == nil {
		return prs
	}
	i := slices.IndexFunc(prs, func(pr mergedPR) bool {
		return pr.Number == shown.Number && strings.EqualFold(pr.Repository, shown.Repository)
	})
	if i < 0 {
		return append([]mergedPR{{RecordedPullRequest: protocol.RecordedPullRequest{Repo: repo, Repository: shown.Repository, Number: shown.Number, URL: shown.URL,
			State: shown.State, MergedAt: shown.MergedAt, CheckedAt: at}, Source: sourceGitHub}}, prs...)
	}
	if !merged(prs[i].State) {
		prs[i].State, prs[i].MergedAt, prs[i].CheckedAt, prs[i].Source = shown.State, shown.MergedAt, at, sourceGitHub
	}
	return prs
}

// markUnchecked flags each pull request in prs not merged that no live
// lookup confirmed, and counts them. o is the run's observation, nil when
// it was not looked up.
func markUnchecked(prs []mergedPR, o *observation) int {
	n := 0
	for i := range prs {
		if merged(prs[i].State) || prs[i].Source == sourceGitHub || o != nil && o.shown == nil && prs[i].Repo == o.repo {
			continue
		}
		prs[i].NotCheckedLive = true
		n++
	}
	return n
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
// evidence.
func conflicts(r protocol.Run, rep *report, prs []mergedPR, o *observation) []string {
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

// addRunEvidence adds getRun's evidence: the run's recorded pull requests,
// GitHub's checks and reviews of one not merged, and conflicts with its
// latest report, and returns what it could not confirm. status is getRun's
// lookup of the run's own repository, whose pull request is listed even
// when Aether has not recorded it.
func addRunEvidence(ctx context.Context, read Call, r protocol.Run, recordedPRs []protocol.RecordedPullRequest, reps []report,
	status *protocol.RunPRStatusResult, out map[string]any) string {
	asOf, lookupErr := time.Now().UTC(), status.Error
	prs := include(recorded(recordedPRs), liveShown(status.Identity, status.Error, status.PullRequest), "", asOf)
	ev := map[string]any{"as_of": asOf.Format(time.RFC3339)}
	var o *observation
	if repo, open := unmerged(prs); open {
		if repo != "" {
			status = nil
		}
		got := observe(ctx, read, r.ID, repo, status)
		prs = include(prs, got.shown, repo, asOf)
		o = &got
		ev["reviewed"] = got
	}
	unchecked := min(markUnchecked(prs, o), 1)
	ev["pull_requests"] = prs[:min(len(prs), defaultRows)]
	if len(prs) > defaultRows {
		ev["pull_requests_total"] = len(prs)
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
	ev["conflicts"] = conflicts(r, rep, prs, o)
	out["evidence"], out["released"], out["deployed"] = ev, untracked, untracked
	failed := 0
	if o != nil && o.Error != "" || o == nil && lookupErr != "" {
		failed = 1
	}
	return caveats(failed, unchecked)
}

// caveats is what an evidence summary could not confirm, so that it does
// not read as clean when lookups failed.
func caveats(failed, unchecked int) string {
	var parts []string
	if failed > 0 {
		parts = append(parts, fmt.Sprintf("%d GitHub lookups returned an error", failed))
	}
	if unchecked > 0 {
		parts = append(parts, fmt.Sprintf("%d runs have pull requests not checked live", unchecked))
	}
	if len(parts) == 0 {
		return ""
	}
	return " Not confirmed: " + strings.Join(parts, ", ") + "."
}
