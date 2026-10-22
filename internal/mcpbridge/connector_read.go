package mcpbridge

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/protocol"
)

const (
	nextRuns       = "call aether_list_runs for run ids"
	nextSwarms     = "call aether_list_swarms for swarm ids"
	nextWorkspaces = "call aether_list_workspaces for workspace ids"
	nextAttention  = "call aether_needs_attention for what is pending"
	defaultRows    = 20
	maxRows        = 100
	maxText        = 600
)

type agentsInput struct {
	Harness string `json:"harness,omitempty" jsonschema:"list this agent's models instead"`
}

type connectorListRunsInput struct {
	WorkspaceID string `json:"workspace_id,omitempty"`
	Mine        *bool  `json:"mine,omitempty" jsonschema:"only runs you launched; default true"`
	ActiveOnly  bool   `json:"active_only,omitempty"`
	NeedsYou    bool   `json:"needs_you,omitempty" jsonschema:"only runs waiting on you"`
	Query       string `json:"query,omitempty" jsonschema:"words to find in title or task"`
	Limit       int    `json:"limit,omitempty" jsonschema:"default 20"`
}

type runIDInput struct {
	RunID string `json:"run_id"`
}

type historyInput struct {
	RunID  string `json:"run_id"`
	Before string `json:"before,omitempty" jsonschema:"next_before from the previous page"`
	Limit  int    `json:"limit,omitempty" jsonschema:"default 20"`
}

type workspaceFilterInput struct {
	WorkspaceID string `json:"workspace_id,omitempty"`
}

type activityInput struct {
	Period      string `json:"period,omitempty" jsonschema:"today (default), yesterday or week"`
	TimeZone    string `json:"time_zone,omitempty" jsonschema:"IANA zone of the user, such as Europe/Berlin; default UTC"`
	WorkspaceID string `json:"workspace_id,omitempty"`
}

type swarmsInput struct {
	WorkspaceID string `json:"workspace_id,omitempty"`
	Limit       int    `json:"limit,omitempty" jsonschema:"default 20"`
}

type swarmInput struct {
	SwarmID string `json:"swarm_id"`
}

type changesInput struct {
	RunID       string `json:"run_id"`
	IncludeDiff bool   `json:"include_diff,omitempty" jsonschema:"add the uncommitted diff, cut to 12000 characters"`
}

type overlapsInput struct {
	RunID string `json:"run_id,omitempty" jsonschema:"only this run's overlaps"`
}

type memoryInput struct {
	WorkspaceID string `json:"workspace_id"`
	Words       string `json:"words,omitempty"`
	Kind        string `json:"kind,omitempty" jsonschema:"decision, convention, gotcha, preference or outcome"`
	File        string `json:"file,omitempty" jsonschema:"only notes citing this path"`
}

var readTools = []connectorTool{
	connectorToolOf("aether_list_workspaces", "List workspaces", ScopeRead, true, "",
		"Use this when you need a workspace (repository) id, before launching.", listWorkspaces),
	connectorToolOf("aether_list_agents", "List agents", ScopeRead, true, "call aether_list_agents with no harness for agent names",
		"Use this when choosing an agent to launch: name, installed, signed in, enhanced mode. With harness, lists that agent's models.", listAgents),
	connectorToolOf("aether_list_runs", "List runs", ScopeRead, true, nextWorkspaces,
		"Use this when the user asks about their runs. Newest first, with state, why it waits and a link.", listRuns),
	connectorToolOf("aether_get_run", "Get run", ScopeRead, true, nextRuns,
		"Use this when the user asks about one run, and to poll a launched run: state, what it waits on, latest report, pull request."+untrustedRule, getRun),
	connectorToolOf("aether_run_history", "Read run history", ScopeRead, true, nextRuns,
		"Use this when the user asks what a run said or did: a page of its conversation or terminal, oldest first, with its reports."+untrustedRule, runHistory),
	connectorToolOf("aether_needs_attention", "What needs me", ScopeRead, true, nextWorkspaces,
		"Use this when the user asks what needs them: questions, approvals, blocked, failed and finished runs, each with the tool that answers it."+untrustedRule, needsAttention),
	connectorToolOf("aether_activity_summary", "Activity summary", ScopeRead, true, "",
		"Use this when the user asks what their agents did today, yesterday or this week: counts, finished runs, pull requests, reports."+untrustedRule, activitySummary),
	connectorToolOf("aether_list_swarms", "List swarms", ScopeRead, true, nextWorkspaces,
		"Use this when the user asks about swarms: a goal an integrator agent splits into tasks for worker agents.", listSwarms),
	connectorToolOf("aether_get_swarm", "Get swarm", ScopeRead, true, nextSwarms,
		"Use this when the user asks about one swarm: phase, tasks, workers, questions, recent messages."+untrustedRule, getSwarm),
	connectorToolOf("aether_run_changes", "Run changes", ScopeRead, true, nextRuns,
		"Use this when the user asks what a run changed: commits, ahead/behind, predicted conflicts, changed files, evidence."+untrustedRule, runChanges),
	connectorToolOf("aether_pr_status", "Pull request status", ScopeRead, true, nextRuns,
		"Use this when the user asks about a run's pull request: CI checks, reviews, comments, mergeability."+untrustedRule, prStatus),
	connectorToolOf("aether_overlaps", "Overlapping runs", ScopeRead, true, nextRuns,
		"Use this when coordinating runs: active runs that touch the same files.", overlaps),
	connectorToolOf("aether_memory_search", "Search team memory", ScopeRead, true, nextWorkspaces,
		"Use this when the user asks what the team decided or learned: a workspace's memory notes, best first."+untrustedRule, memorySearch),
	connectorToolOf("search", "Search runs and swarms", ScopeRead, true, "",
		"Use this when looking for runs or swarms by words in their task or goal.", search),
	connectorToolOf("fetch", "Fetch run or swarm", ScopeRead, true, "call search for ids",
		"Use this when you need the full text of a search result.", fetch),
}

func listWorkspaces(ctx context.Context, c calls, _ struct{}) (string, any, error) {
	var res protocol.WorkspaceListResult
	if err := c.read(ctx, protocol.MethodWorkspaceList, nil, &res); err != nil {
		return "", nil, err
	}
	type row struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		BaseBranch string `json:"base_branch"`
	}
	rows := make([]row, 0, len(res.Workspaces))
	for _, w := range res.Workspaces {
		rows = append(rows, row{ID: w.ID, Name: w.Name, BaseBranch: w.BaseBranch})
	}
	return fmt.Sprintf("%d workspaces.", len(rows)), map[string]any{"workspaces": rows}, nil
}

func listAgents(ctx context.Context, c calls, in agentsInput) (string, any, error) {
	if in.Harness != "" {
		var res protocol.AgentModelsResult
		if err := c.read(ctx, protocol.MethodAgentModels, protocol.AgentModelsParams{Name: in.Harness}, &res); err != nil {
			return "", nil, err
		}
		return fmt.Sprintf("%d models for %s.", len(res.Models), in.Harness), map[string]any{"harness": in.Harness, "models": res.Models}, nil
	}
	var res protocol.AgentListResult
	if err := c.read(ctx, protocol.MethodAgentList, nil, &res); err != nil {
		return "", nil, err
	}
	type row struct {
		Name        string `json:"name"`
		Installed   bool   `json:"installed"`
		SignedIn    *bool  `json:"signed_in,omitempty"`
		Enhanced    bool   `json:"enhanced"`
		DefaultMode string `json:"default_mode"`
	}
	rows := make([]row, 0, len(res.Agents))
	for _, a := range res.Agents {
		rows = append(rows, row{Name: a.Name, Installed: a.Installed, SignedIn: a.LoginFound,
			Enhanced: a.Enhanced != "none" && a.EnhancedInstalled, DefaultMode: a.DefaultMode})
	}
	return fmt.Sprintf("%d agents.", len(rows)), map[string]any{"agents": rows}, nil
}

type runRow struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	State       string `json:"state"`
	Activity    string `json:"activity,omitempty"`
	Reason      string `json:"reason,omitempty"`
	NeedsYou    string `json:"needs_you,omitempty"`
	WorkspaceID string `json:"workspace_id"`
	Harness     string `json:"harness"`
	MissionID   string `json:"mission_id,omitempty"`
	CreatedAt   string `json:"created_at"`
	URL         string `json:"url,omitempty"`
}

func runTitle(r protocol.Run) string {
	return cmp.Or(r.Title, clip(r.Task, 80))
}

func rowOf(r protocol.Run, l links) runRow {
	return runRow{ID: r.ID, Title: runTitle(r), State: r.Status, Activity: r.Activity, Reason: r.Reason,
		WorkspaceID: r.WorkspaceID, Harness: r.Harness, MissionID: r.MissionID, CreatedAt: r.CreatedAt, URL: l.run(r.ID)}
}

func rowLimit(n int) int {
	if n <= 0 {
		return defaultRows
	}
	return min(n, maxRows)
}

func listRuns(ctx context.Context, c calls, in connectorListRunsInput) (string, any, error) {
	l, err := dashboardLinks(ctx, c.read)
	if err != nil {
		return "", nil, err
	}
	params := struct {
		protocol.RunListParams
		Mine bool `json:"mine,omitempty"`
	}{protocol.RunListParams{WorkspaceID: in.WorkspaceID, ActiveOnly: in.ActiveOnly}, in.Mine == nil || *in.Mine}
	var res protocol.RunListResult
	if err = c.read(ctx, protocol.MethodRunList, params, &res); err != nil {
		return "", nil, err
	}
	items, err := attention(ctx, c.read, in.WorkspaceID)
	if err != nil {
		return "", nil, err
	}
	waiting := map[string]string{}
	for _, it := range items {
		waiting[it.RunID] = it.Reason
	}
	words := strings.Fields(strings.ToLower(in.Query))
	slices.SortStableFunc(res.Runs, func(a, b protocol.Run) int { return strings.Compare(b.CreatedAt, a.CreatedAt) })
	rows := []runRow{}
	for _, r := range res.Runs {
		reason, needs := waiting[r.ID]
		if (in.NeedsYou && !needs) || !matchesAll(strings.ToLower(r.Title+" "+r.Task), words) {
			continue
		}
		row := rowOf(r, l)
		row.NeedsYou = reason
		if rows = append(rows, row); len(rows) == rowLimit(in.Limit) {
			break
		}
	}
	return fmt.Sprintf("%d runs.", len(rows)), map[string]any{"runs": rows}, nil
}

func matchesAll(text string, words []string) bool {
	for _, w := range words {
		if !strings.Contains(text, w) {
			return false
		}
	}
	return true
}

func getRunRecord(ctx context.Context, read Call, id string) (protocol.Run, error) {
	var res protocol.RunResult
	if err := read(ctx, protocol.MethodRunGet, protocol.RunIDParams{RunID: id}, &res); err != nil {
		return protocol.Run{}, err
	}
	return res.Run, nil
}

func attention(ctx context.Context, read Call, workspaceID string) ([]protocol.AttentionItem, error) {
	var res protocol.AttentionListResult
	if err := read(ctx, protocol.MethodAttentionList, protocol.AttentionListParams{WorkspaceID: workspaceID}, &res); err != nil {
		return nil, err
	}
	return res.Items, nil
}

// attentionRow is an attention.list item with what answers it. answer_id
// is the id aether_answer or aether_decide_approval takes.
type attentionRow struct {
	Kind        string                     `json:"kind"`
	RunID       string                     `json:"run_id"`
	MissionID   string                     `json:"mission_id,omitempty"`
	WorkspaceID string                     `json:"workspace_id"`
	Title       string                     `json:"title"`
	Harness     string                     `json:"harness"`
	Reason      string                     `json:"reason"`
	Since       string                     `json:"since,omitempty"`
	AnswerWith  string                     `json:"answer_with"`
	AnswerID    string                     `json:"answer_id,omitempty"`
	Options     []protocol.AttentionOption `json:"options,omitempty"`
	LinkURL     string                     `json:"link_url,omitempty"`
	URL         string                     `json:"url,omitempty"`
	Untrusted   *attentionText             `json:"untrusted,omitempty"`
}

type attentionText struct {
	Text   string          `json:"text,omitempty"`
	Detail string          `json:"detail,omitempty"`
	Schema json.RawMessage `json:"schema,omitempty"`
}

func attentionRowOf(it protocol.AttentionItem, l links) attentionRow {
	row := attentionRow{Kind: it.Kind, RunID: it.RunID, MissionID: it.MissionID, WorkspaceID: it.WorkspaceID,
		Title: it.Title, Harness: it.Harness, Reason: it.Reason, Since: it.Since, URL: l.run(it.RunID)}
	switch {
	case it.Approval != nil:
		row.AnswerID = it.Approval.ID
		row.Untrusted = &attentionText{Text: it.Approval.Action, Detail: it.Approval.Detail}
	case it.Request != nil:
		row.AnswerID, row.LinkURL = it.Request.ID, it.Request.URL
		row.Untrusted = &attentionText{Text: it.Request.Title, Schema: it.Request.Schema}
		if it.Kind == protocol.AttentionKindQuestion {
			row.Options = it.Request.Options
		}
	case it.Message != nil:
		row.AnswerID = it.Message.ID
		row.Untrusted = &attentionText{Text: it.Message.Body}
	}
	row.AnswerWith = answerWith(it)
	return row
}

func answerWith(it protocol.AttentionItem) string {
	const dashboard = "the run in the Aether dashboard"
	switch it.Kind {
	case protocol.AttentionKindApproval:
		return "aether_decide_approval"
	case protocol.AttentionKindPermission:
		if it.Request != nil && len(it.Request.Options) > 0 {
			return "aether_decide_approval"
		}
		return "the run's terminal in the Aether dashboard"
	case protocol.AttentionKindQuestion:
		switch {
		case it.Request == nil || len(it.Request.Options) == 0:
			return "the run's terminal in the Aether dashboard"
		case it.Request.URL != "":
			return "open link_url, then aether_answer with an option"
		}
		return "aether_answer"
	case protocol.AttentionKindSwarmQuestion, protocol.AttentionKindRoomQuestion:
		if it.Message != nil {
			return "aether_answer"
		}
		return dashboard
	case protocol.AttentionKindNeedsInput, protocol.AttentionKindBlocked:
		return "aether_message_run"
	case protocol.AttentionKindReady:
		return "aether_pr_status, then aether_mark_reviewed or aether_close_run"
	case protocol.AttentionKindFailed:
		return "aether_get_run, then aether_message_run or aether_close_run"
	case protocol.AttentionKindIntegratorDown:
		return "aether_get_swarm"
	}
	return dashboard
}

func needsAttention(ctx context.Context, c calls, in workspaceFilterInput) (string, any, error) {
	l, err := dashboardLinks(ctx, c.read)
	if err != nil {
		return "", nil, err
	}
	items, err := attention(ctx, c.read, in.WorkspaceID)
	if err != nil {
		return "", nil, err
	}
	rows := make([]attentionRow, 0, len(items))
	for _, it := range items {
		rows = append(rows, attentionRowOf(it, l))
	}
	if len(rows) == 0 {
		return "Nothing needs you.", map[string]any{"items": rows}, nil
	}
	return fmt.Sprintf("%d items need you, oldest first.", len(rows)), map[string]any{"items": rows}, nil
}

type report struct {
	RunID      string `json:"run_id,omitempty"`
	Outcome    string `json:"outcome"`
	Summary    string `json:"summary"`
	NextAction string `json:"next_action,omitempty"`
	At         string `json:"at"`
}

// reports are the runs' coordination reports in a workspace, newest
// first: the last 100 messages, narrowed by run or mission.
func reports(ctx context.Context, read Call, p protocol.CoordMessagesListParams) ([]report, error) {
	p.Limit = maxRows
	var res protocol.CoordMessagesListResult
	if err := read(ctx, protocol.MethodCoordMessagesList, p, &res); err != nil {
		return nil, err
	}
	out := []report{}
	for _, m := range res.Messages {
		if m.Kind == protocol.CoordMessageKindReport && (p.RunID == "" || m.FromRunID == p.RunID) {
			out = append(out, report{RunID: m.FromRunID, Outcome: m.Outcome, Summary: clip(m.Summary, maxText),
				NextAction: clip(m.NextAction, maxText), At: m.CreatedAt})
		}
	}
	return out, nil
}

type pullRequest struct {
	Number int    `json:"number"`
	URL    string `json:"url"`
	State  string `json:"state"`
	Title  string `json:"title"`
	Draft  bool   `json:"draft"`
}

func prOf(p *protocol.RunPullRequest) *pullRequest {
	if p == nil {
		return nil
	}
	return &pullRequest{Number: p.Number, URL: p.URL, State: p.State, Title: p.Title, Draft: p.Draft}
}

func getRun(ctx context.Context, c calls, in runIDInput) (string, any, error) {
	r, err := getRunRecord(ctx, c.read, in.RunID)
	if err != nil {
		return "", nil, err
	}
	l, err := dashboardLinks(ctx, c.read)
	if err != nil {
		return "", nil, err
	}
	items, err := attention(ctx, c.read, r.WorkspaceID)
	if err != nil {
		return "", nil, err
	}
	reps, err := reports(ctx, c.read, protocol.CoordMessagesListParams{WorkspaceID: r.WorkspaceID, RunID: r.ID})
	if err != nil {
		return "", nil, err
	}
	out := map[string]any{"run": rowOf(r, l), "task": r.Task, "mode": r.Mode, "model": r.Model, "branch": r.Branch,
		"paused": r.Paused, "pending_inputs": len(r.PendingInputs), "finished_at": r.FinishedAt, "review": r.Review}
	summary := fmt.Sprintf("Run %s is %s.", r.ID, r.Status)
	for _, it := range items {
		if it.RunID == r.ID {
			out["needs_you"] = attentionRowOf(it, l)
			summary += " Needs you: " + it.Reason + "."
		}
	}
	if len(reps) > 0 {
		out["untrusted"] = map[string]any{"latest_report": reps[0]}
	}
	var pr protocol.RunPRStatusResult
	switch err := c.read(ctx, protocol.MethodRunPRStatus, protocol.RunPRStatusParams{RunID: r.ID}, &pr); {
	case err != nil:
		out["pr_error"] = err.Error()
	case pr.PullRequest != nil:
		out["pr"] = prOf(pr.PullRequest)
	case pr.Error != "":
		out["pr_error"] = pr.Error
	}
	return summary, out, nil
}

func runHistory(ctx context.Context, c calls, in historyInput) (string, any, error) {
	r, err := getRunRecord(ctx, c.read, in.RunID)
	if err != nil {
		return "", nil, err
	}
	limit := min(rowLimit(in.Limit), 50)
	reps, err := reports(ctx, c.read, protocol.CoordMessagesListParams{WorkspaceID: r.WorkspaceID, RunID: r.ID})
	if err != nil {
		return "", nil, err
	}
	untrusted := map[string]any{"reports": reps}
	out := map[string]any{"run_id": r.ID, "mode": r.Mode, "untrusted": untrusted}
	if r.ACP {
		var before int64
		if in.Before != "" {
			if before, err = strconv.ParseInt(in.Before, 10, 64); err != nil {
				return "", nil, &protocol.Error{Code: protocol.CodeInvalidParams, Message: fmt.Sprintf("before %q is not a next_before this run returned", in.Before)}
			}
		}
		var res protocol.RunACPHistoryResult
		if err := c.read(ctx, protocol.MethodRunACPHistory, protocol.RunACPHistoryParams{RunID: r.ID, BeforeSeq: before, Limit: limit}, &res); err != nil {
			return "", nil, err
		}
		type frame struct {
			Seq  int64  `json:"seq"`
			Item string `json:"item"`
		}
		frames := make([]frame, 0, len(res.Frames))
		for _, f := range res.Frames {
			frames = append(frames, frame{Seq: f.Seq, Item: clip(string(f.Item), 2000)})
		}
		untrusted["conversation"] = frames
		if res.TruncatedBefore || len(frames) == limit {
			out["next_before"] = strconv.FormatInt(res.OldestSeq, 10)
		}
		return fmt.Sprintf("%d conversation items of run %s.", len(frames), r.ID), out, nil
	}
	var res protocol.TerminalHistoryResult
	if err := c.read(ctx, protocol.MethodTerminalHistory, protocol.TerminalHistoryParams{RunID: r.ID, Before: in.Before, Limit: limit}, &res); err != nil {
		return "", nil, err
	}
	lines := make([]string, 0, len(res.Lines))
	for _, line := range res.Lines {
		lines = append(lines, clip(line.Text, maxText))
	}
	untrusted["terminal"] = lines
	if res.HasMore {
		out["next_before"] = res.NextCursor
	}
	return fmt.Sprintf("%d terminal lines of run %s.", len(lines), r.ID), out, nil
}

// periodRange is [from, to) for today, yesterday or the last seven days
// in zone, ending tomorrow at midnight.
func periodRange(period, zone string, now time.Time) (time.Time, time.Time, error) {
	loc, err := time.LoadLocation(zone)
	if err != nil || zone == "Local" {
		return time.Time{}, time.Time{}, &protocol.Error{Code: protocol.CodeInvalidParams, Message: fmt.Sprintf("time_zone %q is not an IANA time zone such as Europe/Berlin", zone)}
	}
	now = now.In(loc)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	switch period {
	case "", "today":
		return today, today.AddDate(0, 0, 1), nil
	case "yesterday":
		return today.AddDate(0, 0, -1), today, nil
	case "week":
		return today.AddDate(0, 0, -6), today.AddDate(0, 0, 1), nil
	}
	return time.Time{}, time.Time{}, &protocol.Error{Code: protocol.CodeInvalidParams, Message: fmt.Sprintf("period %q: want today, yesterday or week", period)}
}

func activitySummary(ctx context.Context, c calls, in activityInput) (string, any, error) {
	from, to, err := periodRange(in.Period, in.TimeZone, time.Now())
	if err != nil {
		return "", nil, err
	}
	l, err := dashboardLinks(ctx, c.read)
	if err != nil {
		return "", nil, err
	}
	var res protocol.InsightsResult
	if err := c.read(ctx, protocol.MethodInsightsGet, protocol.InsightsParams{WorkspaceID: in.WorkspaceID, Scope: protocol.InsightsScopeMine,
		From: from.Format(time.RFC3339), To: to.Format(time.RFC3339), TimeZone: in.TimeZone}, &res); err != nil {
		return "", nil, err
	}
	type finished struct {
		RunID   string `json:"run_id"`
		Title   string `json:"title"`
		Status  string `json:"status"`
		Reason  string `json:"reason,omitempty"`
		PRURL   string `json:"pr_url,omitempty"`
		PRState string `json:"pr_state,omitempty"`
		URL     string `json:"url,omitempty"`
	}
	recent := []finished{}
	inRecent := map[string]bool{}
	workspaces := []string{}
	for _, r := range res.Recent[:min(len(res.Recent), defaultRows)] {
		recent = append(recent, finished{RunID: r.RunID, Title: r.Title, Status: r.Status, Reason: r.Reason, PRURL: r.PRURL, PRState: r.PRState, URL: l.run(r.RunID)})
		inRecent[r.RunID] = true
		if !slices.Contains(workspaces, r.WorkspaceID) {
			workspaces = append(workspaces, r.WorkspaceID)
		}
	}
	latest := []report{}
	seen := map[string]bool{}
	for _, ws := range workspaces {
		reps, err := reports(ctx, c.read, protocol.CoordMessagesListParams{WorkspaceID: ws})
		if err != nil {
			return "", nil, err
		}
		for _, rep := range reps {
			if inRecent[rep.RunID] && !seen[rep.RunID] {
				seen[rep.RunID] = true
				latest = append(latest, rep)
			}
		}
	}
	t := res.Totals
	out := map[string]any{"from": res.From, "to": res.To, "time_zone": res.TimeZone,
		"totals":        map[string]int{"launched": t.Launched, "completed": t.Completed, "merged": t.Merged, "failed": t.Failed, "active": t.Active},
		"pull_requests": res.PullRequests, "swarms": map[string]int{"launched": res.Swarms.Launched, "completed": res.Swarms.Completed},
		"needs_you": res.NeedsYou, "finished_runs": recent, "untrusted": map[string]any{"reports": latest}}
	return fmt.Sprintf("%d launched, %d completed, %d merged, %d failed, %d active; %d pull requests merged.",
		t.Launched, t.Completed, t.Merged, t.Failed, t.Active, res.PullRequests.Merged), out, nil
}

type swarmRow struct {
	ID              string `json:"id"`
	WorkspaceID     string `json:"workspace_id"`
	Objective       string `json:"objective"`
	Phase           string `json:"phase"`
	OpenQuestions   int    `json:"open_questions"`
	IntegratorRunID string `json:"integrator_run_id,omitempty"`
	CreatedAt       string `json:"created_at"`
	URL             string `json:"url,omitempty"`
}

// allSwarms lists the swarms of one workspace, or of every workspace when
// workspaceID is empty, newest first.
func allSwarms(ctx context.Context, read Call, workspaceID string) ([]protocol.Mission, error) {
	ids := []string{workspaceID}
	if workspaceID == "" {
		var res protocol.WorkspaceListResult
		if err := read(ctx, protocol.MethodWorkspaceList, nil, &res); err != nil {
			return nil, err
		}
		ids = ids[:0]
		for _, w := range res.Workspaces {
			ids = append(ids, w.ID)
		}
	}
	var out []protocol.Mission
	for _, id := range ids {
		var res protocol.MissionListResult
		if err := read(ctx, protocol.MethodMissionList, protocol.MissionListParams{WorkspaceID: id, Limit: maxRows}, &res); err != nil {
			return nil, err
		}
		out = append(out, res.Missions...)
	}
	slices.SortStableFunc(out, func(a, b protocol.Mission) int { return strings.Compare(b.CreatedAt, a.CreatedAt) })
	return out, nil
}

func listSwarms(ctx context.Context, c calls, in swarmsInput) (string, any, error) {
	l, err := dashboardLinks(ctx, c.read)
	if err != nil {
		return "", nil, err
	}
	missions, err := allSwarms(ctx, c.read, in.WorkspaceID)
	if err != nil {
		return "", nil, err
	}
	rows := []swarmRow{}
	for _, m := range missions[:min(len(missions), rowLimit(in.Limit))] {
		rows = append(rows, swarmRow{ID: m.ID, WorkspaceID: m.WorkspaceID, Objective: clip(m.Objective, 200), Phase: m.Phase,
			OpenQuestions: m.OpenQuestions, IntegratorRunID: m.CurrentIntegratorRunID, CreatedAt: m.CreatedAt, URL: l.swarm(m.ID)})
	}
	return fmt.Sprintf("%d swarms.", len(rows)), map[string]any{"swarms": rows}, nil
}

func getSwarm(ctx context.Context, c calls, in swarmInput) (string, any, error) {
	var res protocol.MissionShowResult
	if err := c.read(ctx, protocol.MethodMissionShow, protocol.MissionShowParams{MissionID: in.SwarmID}, &res); err != nil {
		return "", nil, err
	}
	l, err := dashboardLinks(ctx, c.read)
	if err != nil {
		return "", nil, err
	}
	m := res.Mission
	var msgs protocol.CoordMessagesListResult
	if err := c.read(ctx, protocol.MethodCoordMessagesList, protocol.CoordMessagesListParams{WorkspaceID: m.WorkspaceID, MissionID: m.ID, Limit: 10}, &msgs); err != nil {
		return "", nil, err
	}
	type task struct {
		ID       string   `json:"id"`
		Status   string   `json:"status"`
		Title    string   `json:"title,omitempty"`
		Blockers []string `json:"blockers,omitempty"`
	}
	type worker struct {
		RunID   string `json:"run_id"`
		TaskID  string `json:"task_id"`
		State   string `json:"state"`
		Harness string `json:"harness"`
		URL     string `json:"url,omitempty"`
	}
	type question struct {
		ID       string `json:"id"`
		Body     string `json:"body"`
		Answer   string `json:"answer,omitempty"`
		Answered bool   `json:"answered"`
	}
	type message struct {
		FromRunID string `json:"from_run_id"`
		ToRunID   string `json:"to_run_id"`
		Kind      string `json:"kind"`
		Body      string `json:"body"`
		At        string `json:"at"`
	}
	tasks := make([]task, 0, len(res.Tasks))
	for _, t := range res.Tasks {
		row := task{ID: t.ID, Status: t.Status}
		if t.Revision != nil {
			row.Title = t.Revision.Title
		}
		for _, b := range t.Blockers {
			row.Blockers = append(row.Blockers, b.Action)
		}
		tasks = append(tasks, row)
	}
	workers := make([]worker, 0, len(res.Attempts))
	for _, a := range res.Attempts {
		workers = append(workers, worker{RunID: a.RunID, TaskID: a.TaskID, State: a.State, Harness: a.Harness, URL: l.run(a.RunID)})
	}
	questions := make([]question, 0, len(res.Questions))
	for _, q := range res.Questions {
		questions = append(questions, question{ID: q.ID, Body: clip(q.Body, maxText), Answer: clip(q.Answer, maxText), Answered: q.AnsweredAt != nil})
	}
	messages := make([]message, 0, len(msgs.Messages))
	for _, msg := range msgs.Messages {
		messages = append(messages, message{FromRunID: msg.FromRunID, ToRunID: msg.ToRunID, Kind: msg.Kind,
			Body: clip(cmp.Or(msg.Summary, msg.Body), maxText), At: msg.CreatedAt})
	}
	out := map[string]any{"swarm": swarmRow{ID: m.ID, WorkspaceID: m.WorkspaceID, Objective: m.Objective, Phase: m.Phase,
		OpenQuestions: m.OpenQuestions, IntegratorRunID: m.CurrentIntegratorRunID, CreatedAt: m.CreatedAt, URL: l.swarm(m.ID)},
		"workers": workers, "untrusted": map[string]any{"tasks": tasks, "questions": questions, "messages": messages}}
	if m.IntegratorLaunchError != "" {
		out["integrator_launch_error"] = m.IntegratorLaunchError
	}
	return fmt.Sprintf("Swarm %s is %s: %d tasks, %d workers, %d open questions.", m.ID, m.Phase, len(tasks), len(workers), m.OpenQuestions), out, nil
}

func runChanges(ctx context.Context, c calls, in changesInput) (string, any, error) {
	r, err := getRunRecord(ctx, c.read, in.RunID)
	if err != nil {
		return "", nil, err
	}
	out := map[string]any{"run_id": r.ID, "branch": r.Branch}
	untrusted := map[string]any{}
	summary := fmt.Sprintf("Run %s:", r.ID)
	var log protocol.RunGitLogResult
	switch err := c.read(ctx, protocol.MethodRunGitLog, protocol.RunGitLogParams{RunID: r.ID, Limit: defaultRows}, &log); {
	case err != nil:
		out["git_error"] = err.Error()
	case log.Error != "":
		out["git_error"] = log.Error
	default:
		type commit struct {
			Short   string `json:"short"`
			Subject string `json:"subject"`
			Author  string `json:"author"`
			At      string `json:"at"`
		}
		commits := make([]commit, 0, len(log.Commits))
		for _, cm := range log.Commits {
			commits = append(commits, commit{Short: cm.Short, Subject: clip(cm.Subject, 200), Author: cm.Author, At: cm.AuthoredAt})
		}
		conflicts := make([]string, 0, len(log.Conflicts))
		for _, cf := range log.Conflicts {
			conflicts = append(conflicts, cf.Path)
		}
		out["base_ref"], out["ahead"], out["behind"], out["conflicts"] = log.BaseRef, log.Ahead, log.Behind, conflicts
		if log.ConflictsError != "" {
			out["conflicts_error"] = log.ConflictsError
		}
		untrusted["commits"] = commits
		summary += fmt.Sprintf(" %d commits ahead, %d behind, %d predicted conflicts.", log.Ahead, log.Behind, len(conflicts))
	}
	var ev protocol.EvidencePacketListResult
	switch err := c.read(ctx, protocol.MethodRunEvidenceList, protocol.RunEvidenceListParams{WorkspaceID: r.WorkspaceID, RunID: r.ID, Limit: 1}, &ev); {
	case err != nil:
		out["evidence_error"] = err.Error()
	case len(ev.Packets) > 0:
		p := ev.Packets[0]
		out["changed_files"] = p.ChangedFiles
		untrusted["evidence"] = map[string]any{"captured_at": p.CapturedAt, "verification_notes": clip(p.VerificationNotes, maxText),
			"unresolved": p.UnresolvedFacts, "next_action": clip(p.NextAction, maxText)}
		summary += fmt.Sprintf(" %d changed files in its latest evidence.", len(p.ChangedFiles))
	}
	if in.IncludeDiff {
		var diff protocol.RunGitDiffResult
		if err := c.read(ctx, protocol.MethodRunGitDiff, protocol.RunGitDiffParams{RunID: r.ID}, &diff); err != nil {
			out["diff_error"] = err.Error()
		} else {
			untrusted["diff"] = clip(diff.Output.Stdout, 12000)
		}
	}
	out["untrusted"] = untrusted
	return summary, out, nil
}

func prStatus(ctx context.Context, c calls, in runIDInput) (string, any, error) {
	var res protocol.RunPRFeedbackResult
	if err := c.read(ctx, protocol.MethodRunPRFeedback, protocol.RunPRFeedbackParams{RunID: in.RunID, Limit: 30}, &res); err != nil {
		return "", nil, err
	}
	if res.PullRequest == nil {
		msg := cmp.Or(res.Error, "run "+in.RunID+" has no pull request")
		return "", nil, &protocol.Error{Code: protocol.CodeNotFound, Message: msg + "; aether_message_run can ask its agent to open one"}
	}
	var passed, pending int
	failing := []protocol.RunPRCheck{}
	for _, ch := range res.Checks {
		switch strings.ToUpper(ch.Conclusion) {
		case "SUCCESS", "NEUTRAL", "SKIPPED":
			passed++
		case "":
			pending++
		default:
			failing = append(failing, protocol.RunPRCheck{Name: ch.Name, Conclusion: ch.Conclusion, URL: ch.URL})
		}
	}
	type review struct {
		Author string `json:"author"`
		State  string `json:"state"`
		Body   string `json:"body,omitempty"`
	}
	type comment struct {
		Author string `json:"author"`
		Path   string `json:"path,omitempty"`
		Line   *int   `json:"line,omitempty"`
		Body   string `json:"body"`
		URL    string `json:"url"`
	}
	reviews := make([]review, 0, len(res.Reviews))
	for _, rv := range res.Reviews {
		reviews = append(reviews, review{Author: rv.Author, State: rv.State, Body: clip(rv.Body, maxText)})
	}
	comments := make([]comment, 0, len(res.Comments)+len(res.ReviewComments))
	for _, cm := range res.Comments {
		comments = append(comments, comment{Author: cm.Author, Body: clip(cm.Body, maxText), URL: cm.URL})
	}
	for _, cm := range res.ReviewComments {
		comments = append(comments, comment{Author: cm.Author, Path: cm.Path, Line: cm.Line, Body: clip(cm.Body, maxText), URL: cm.URL})
	}
	pr := prOf(res.PullRequest)
	out := map[string]any{"run_id": in.RunID, "pr": pr, "mergeable": res.Mergeable, "merge_state": res.MergeStateStatus,
		"checks":    map[string]any{"passed": passed, "pending": pending, "failing": failing},
		"untrusted": map[string]any{"reviews": reviews, "comments": comments}}
	if res.Error != "" {
		out["error"] = res.Error
	}
	return fmt.Sprintf("PR #%d is %s: %d checks passed, %d failing, %d pending; %d reviews, %d comments; merge state %s.",
		pr.Number, pr.State, passed, len(failing), pending, len(reviews), len(comments), cmp.Or(res.MergeStateStatus, "unknown")), out, nil
}

func overlaps(ctx context.Context, c calls, in overlapsInput) (string, any, error) {
	var res protocol.RunOverlapsResult
	if err := c.read(ctx, protocol.MethodRunOverlaps, nil, &res); err != nil {
		return "", nil, err
	}
	l, err := dashboardLinks(ctx, c.read)
	if err != nil {
		return "", nil, err
	}
	type peer struct {
		RunID string   `json:"run_id"`
		Files []string `json:"files"`
		URL   string   `json:"url,omitempty"`
	}
	type overlap struct {
		RunID string `json:"run_id"`
		URL   string `json:"url,omitempty"`
		With  []peer `json:"with"`
	}
	rows := []overlap{}
	for _, o := range res.Overlaps {
		if in.RunID != "" && o.RunID != in.RunID {
			continue
		}
		row := overlap{RunID: o.RunID, URL: l.run(o.RunID)}
		for _, p := range o.With {
			row.With = append(row.With, peer{RunID: p.RunID, Files: p.Files, URL: l.run(p.RunID)})
		}
		rows = append(rows, row)
	}
	return fmt.Sprintf("%d runs overlap; aether_message_run can tell one about another.", len(rows)), map[string]any{"overlaps": rows}, nil
}

func memorySearch(ctx context.Context, c calls, in memoryInput) (string, any, error) {
	var res protocol.MemorySearchResult
	if err := c.read(ctx, protocol.MethodMemorySearch, protocol.MemorySearchParams{WorkspaceID: in.WorkspaceID, Words: in.Words, Kind: in.Kind, File: in.File}, &res); err != nil {
		return "", nil, err
	}
	type note struct {
		ID     string   `json:"id"`
		Kind   string   `json:"kind"`
		Title  string   `json:"title"`
		Body   string   `json:"body"`
		Files  []string `json:"files,omitempty"`
		Author string   `json:"author,omitempty"`
		RunID  string   `json:"run_id,omitempty"`
		PRURL  string   `json:"pr_url,omitempty"`
		Pinned bool     `json:"pinned"`
	}
	notes := make([]note, 0, len(res.Items))
	for _, m := range res.Items {
		notes = append(notes, note{ID: m.ID, Kind: m.Kind, Title: m.Title, Body: clip(m.Body, 1200), Files: m.Files,
			Author: m.AuthorName, RunID: m.RunID, PRURL: m.PRURL, Pinned: m.PinnedAt != nil})
	}
	return fmt.Sprintf("%d notes.", len(notes)), map[string]any{"untrusted": map[string]any{"notes": notes}}, nil
}
