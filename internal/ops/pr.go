package ops

import (
	"cmp"
	"context"
	"fmt"
	"strings"

	"github.com/3xDevOps/Aether/internal/protocol"
)

type prCreateInput struct {
	RunID        string `json:"run_id"`
	Repo         string `json:"repo,omitempty" jsonschema:"an extra repo's workspace_id from aether_run_changes; default the primary"`
	Title        string `json:"title"`
	Body         string `json:"body"`
	Draft        bool   `json:"draft,omitempty"`
	ExpectedHead string `json:"expected_head,omitempty" jsonschema:"at least 7 characters of the commit the user reviewed; fails if the branch moved"`
}

type prMergeInput struct {
	RunID           string `json:"run_id"`
	Repo            string `json:"repo,omitempty"`
	ExpectedHeadOID string `json:"expected_head_oid" jsonschema:"pr.head_oid from aether_pr_status, at least 7 characters"`
	Method          string `json:"method,omitempty" jsonschema:"default squash"`
}

var prOps = []Op{
	opOf("aether_pr_create", "Open pull request", ScopeControl, true, nextRuns,
		"Use this when the user wants a pull request opened from a run's pushed branch into the workspace's base branch, where the dashboard would propose. An open one is returned instead of opening another.", prCreate).
		hints(false, true).offer(TierConfirmed).
		calling(protocol.MethodServerInfo, protocol.MethodRunPRStatus, protocol.MethodRunPRCreate),
	opOf("aether_pr_merge", "Merge pull request", ScopeControl, true, nextRuns,
		"Use this when the user wants a run's pull request merged on GitHub after reviewing it with aether_pr_status. Refuses if the head moved from expected_head_oid; GitHub refuses failing required checks and unresolved reviews.", prMerge).
		enum("method", "squash", "merge", "rebase").hints(true, true).offer(TierConfirmed).
		calling(protocol.MethodServerInfo, protocol.MethodRunPRFeedback, protocol.MethodRunPRMerge),
}

func prCreate(ctx context.Context, c calls, in prCreateInput) (string, any, error) {
	if in.ExpectedHead != "" && len(in.ExpectedHead) < 7 {
		return "", nil, invalid("expected_head takes at least 7 characters of the commit")
	}
	l, err := dashboardLinks(ctx, c.read)
	if err != nil {
		return "", nil, err
	}
	var status protocol.RunPRStatusResult
	if err := c.read(ctx, protocol.MethodRunPRStatus, protocol.RunPRStatusParams{RunID: in.RunID, Repo: in.Repo}, &status); err != nil {
		return "", nil, err
	}
	if status.Error != "" {
		return "", nil, &protocol.Error{Code: protocol.CodeInvalidState, Message: status.Error + "; aether_message_run can ask the run's agent to fix its branch"}
	}
	if pr := status.PullRequest; pr != nil && pr.State == "open" {
		return fmt.Sprintf("Run %s already has pull request #%d open: %s.", in.RunID, pr.Number, pr.URL),
			map[string]any{"run_id": in.RunID, "pr": prOf(pr), "created": false, "url": l.run(in.RunID), "untrusted": map[string]any{"title": clip(pr.Title, maxReport)}}, nil
	}
	if status.Expected == nil || status.Target == nil {
		return "", nil, &protocol.Error{Code: protocol.CodeInvalidState, Message: fmt.Sprintf("run %s's checkout gave no branch and target to open a pull request from; open it in the Aether dashboard", in.RunID)}
	}
	if in.ExpectedHead != "" && !strings.HasPrefix(status.Expected.Head, in.ExpectedHead) {
		return "", nil, &protocol.Error{Code: protocol.CodeConflict, Message: fmt.Sprintf("run %s's branch %s moved to %s since %s; review aether_run_changes again",
			in.RunID, status.Expected.Branch, status.Expected.Head, in.ExpectedHead)}
	}
	var res protocol.RunPRCreateResult
	if err := c.act(ctx, protocol.MethodRunPRCreate, protocol.RunPRCreateParams{RunID: in.RunID, Repo: in.Repo, Expected: *status.Expected, Target: *status.Target,
		Title: in.Title, Body: in.Body, Draft: in.Draft, ExpectedLogin: status.Identity}, &res); err != nil {
		return "", nil, err
	}
	out := map[string]any{"run_id": in.RunID, "created": res.Created, "url": l.run(in.RunID)}
	if res.PullRequest != nil {
		out["pr"] = prOf(res.PullRequest)
	}
	switch {
	case res.CreationUncertain:
		out["write"] = writePending
		return fmt.Sprintf("GitHub may have opened the pull request for run %s: %s; call aether_pr_status before trying again.", in.RunID, cmp.Or(res.Error, "its answer was lost")), out, nil
	case res.Error != "":
		return "", nil, &protocol.Error{Code: protocol.CodeInvalidState, Message: res.Error + "; if the branch is not pushed, aether_message_run can ask the run's agent to push it"}
	case res.PullRequest == nil:
		return "", nil, &protocol.Error{Code: protocol.CodeInternal, Message: "run.pr.create returned no pull request and no error"}
	}
	return fmt.Sprintf("Opened pull request #%d for run %s: %s.", res.PullRequest.Number, in.RunID, res.PullRequest.URL), out, nil
}

// prMerge merges the pull request the user reviewed at expected_head_oid
// and nothing else. Its refusals carry what GitHub reports blocking the
// merge; that text names checks and reviewers and never decides anything.
func prMerge(ctx context.Context, c calls, in prMergeInput) (string, any, error) {
	if len(in.ExpectedHeadOID) < 7 {
		return "", nil, invalid("expected_head_oid takes at least 7 characters of pr.head_oid from aether_pr_status")
	}
	l, err := dashboardLinks(ctx, c.read)
	if err != nil {
		return "", nil, err
	}
	var fb protocol.RunPRFeedbackResult
	if err := c.read(ctx, protocol.MethodRunPRFeedback, protocol.RunPRFeedbackParams{RunID: in.RunID, Repo: in.Repo, Limit: 30}, &fb); err != nil {
		return "", nil, err
	}
	pr := fb.PullRequest
	if pr == nil {
		return "", nil, &protocol.Error{Code: protocol.CodeNotFound, Message: cmp.Or(fb.Error, "run "+in.RunID+" has no pull request") + "; aether_pr_create opens one"}
	}
	out := map[string]any{"run_id": in.RunID, "pr": prOf(pr), "url": l.run(in.RunID)}
	switch {
	case pr.State == "merged":
		out["merged"] = true
		return fmt.Sprintf("Pull request #%d is already merged.", pr.Number), out, nil
	case pr.State != "open":
		return "", nil, &protocol.Error{Code: protocol.CodeInvalidState, Message: fmt.Sprintf("pull request #%d is %s, not open", pr.Number, pr.State)}
	case !strings.HasPrefix(pr.HeadOID, in.ExpectedHeadOID):
		return "", nil, &protocol.Error{Code: protocol.CodeConflict, Message: fmt.Sprintf(
			"pull request #%d's head moved from %s to %s; review it again with aether_pr_status before merging%s", pr.Number, in.ExpectedHeadOID, pr.HeadOID, blockers(fb))}
	}
	var res protocol.RunPRMergeResult
	if err := c.act(ctx, protocol.MethodRunPRMerge, protocol.RunPRMergeParams{RunID: in.RunID, Repo: in.Repo, Method: cmp.Or(in.Method, "squash"),
		ExpectedHead: pr.HeadOID, Repository: pr.Repository, Number: pr.Number, ExpectedLogin: fb.Identity, ExpectedBase: pr.BaseBranch}, &res); err != nil {
		return "", nil, err
	}
	if res.Error != "" || !res.Merged {
		return "", nil, &protocol.Error{Code: protocol.CodeInvalidState, Message: fmt.Sprintf("GitHub did not merge pull request #%d: %s%s",
			pr.Number, cmp.Or(res.Error, "it is not merged"), blockers(fb))}
	}
	out["merged"] = true
	if res.PullRequest != nil {
		out["pr"] = prOf(res.PullRequest)
	}
	return fmt.Sprintf("Merged pull request #%d into %s.", pr.Number, pr.BaseBranch), out, nil
}

// blockers counts what fb reports could block a merge. Check names and
// reviewers are GitHub users' text, so aether_pr_status returns them under
// untrusted and the refusal only counts them.
func blockers(fb protocol.RunPRFeedbackResult) string {
	failing, pending := 0, 0
	for _, ch := range fb.Checks {
		switch strings.ToUpper(ch.Conclusion) {
		case "SUCCESS", "NEUTRAL", "SKIPPED":
		case "":
			pending++
		default:
			failing++
		}
	}
	latest := map[string]string{}
	for _, rv := range fb.Reviews {
		latest[rv.Author] = rv.State
	}
	changes := 0
	for _, state := range latest {
		if state == "CHANGES_REQUESTED" {
			changes++
		}
	}
	var parts []string
	if failing > 0 {
		parts = append(parts, fmt.Sprintf("%d failing checks", failing))
	}
	if pending > 0 {
		parts = append(parts, fmt.Sprintf("%d pending checks", pending))
	}
	if changes > 0 {
		parts = append(parts, fmt.Sprintf("%d reviewers requesting changes", changes))
	}
	if fb.MergeStateStatus != "" && fb.MergeStateStatus != "CLEAN" {
		parts = append(parts, "merge state "+fb.MergeStateStatus)
	}
	if len(parts) == 0 {
		return ""
	}
	return "; GitHub reports " + strings.Join(parts, ", ") + "; aether_pr_status names them"
}
