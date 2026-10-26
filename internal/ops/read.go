package ops

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/coordhooks"
	"github.com/3xDevOps/Aether/internal/protocol"
)

const (
	nextRuns       = "call aether_list_runs for run ids"
	nextSwarms     = "call aether_list_swarms for swarm ids"
	nextWorkspaces = "call aether_list_workspaces for workspace ids"
	nextAttention  = "call aether_needs_attention for what is pending"
	defaultRows    = 20
	maxRows        = 100
	maxAttention   = 30
	maxText        = 600
	maxReport      = 200
	maxFeedback    = 300
	maxSwarmPages  = 5
)

type connectorListRunsInput struct {
	WorkspaceID string `json:"workspace_id,omitempty"`
	Mine        *bool  `json:"mine,omitempty"`
	ActiveOnly  bool   `json:"active_only,omitempty"`
	NeedsYou    bool   `json:"needs_you,omitempty"`
	Query       string `json:"query,omitempty" jsonschema:"words in title, task, workspace or branch"`
	Limit       int    `json:"limit,omitempty" jsonschema:"default 20"`
}

type runIDInput struct {
	RunID string `json:"run_id"`
}

type historyInput struct {
	RunID  string `json:"run_id"`
	Before string `json:"before,omitempty" jsonschema:"next_before of the previous page"`
	Limit  int    `json:"limit,omitempty" jsonschema:"default 20"`
}

type workspaceFilterInput struct {
	WorkspaceID string `json:"workspace_id,omitempty"`
}

type activityInput struct {
	Period      string `json:"period,omitempty" jsonschema:"default today"`
	TimeZone    string `json:"time_zone,omitempty" jsonschema:"the user's IANA zone; default UTC"`
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
	RunID         string `json:"run_id"`
	Repo          string `json:"repo,omitempty" jsonschema:"an extra repo's workspace_id from repos; default the primary"`
	CommitsOffset int    `json:"commits_offset,omitempty" jsonschema:"next_commits_offset of the previous page"`
	IncludeDiff   bool   `json:"include_diff,omitempty" jsonschema:"add the uncommitted diff"`
	DiffFile      int    `json:"diff_file,omitempty" jsonschema:"next_diff_file of the previous page"`
}

type overlapsInput struct {
	RunID string `json:"run_id,omitempty"`
}

type memoryInput struct {
	WorkspaceID string `json:"workspace_id"`
	Words       string `json:"words,omitempty"`
	Kind        string `json:"kind,omitempty"`
	File        string `json:"file,omitempty" jsonschema:"only notes citing this path"`
	All         bool   `json:"all,omitempty" jsonschema:"include notes that were replaced"`
}

var readOps = []Op{
	opOf("aether_list_workspaces", "List workspaces", ScopeRead, true, "",
		"Use this when you need a workspace (repository) id, before launching.", listWorkspaces).
		offer(TierRead).calling(protocol.MethodWorkspaceList),
	opOf("aether_list_agents", "List agents", ScopeRead, true, "",
		"Use this when choosing an agent to launch.", listAgents).
		offer(TierRead).calling(protocol.MethodAgentList),
	opOf("aether_list_runs", "List runs", ScopeRead, true, nextWorkspaces,
		"Use this when the user asks what is happening, about their runs, or about a run by what it works on (query). Newest first."+untrustedRule, listRuns).
		offer(TierRead).calling(protocol.MethodServerInfo, protocol.MethodWorkspaceList, protocol.MethodRunList, protocol.MethodAttentionList, protocol.MethodWorkspaceTimeline).
		defaultOn(Connector, "mine", true, "only runs you launched; default true").
		defaultOn(Assistant, "mine", false, "only runs the member launched; default false, every run they can see"),
	opOf("aether_get_run", "Get run", ScopeRead, true, nextRuns,
		"Use this when the user asks about one run, and to poll a launched run."+untrustedRule, getRun).
		offer(TierRead).calling(protocol.MethodRunGet, protocol.MethodServerInfo, protocol.MethodAttentionList, protocol.MethodWorkspaceTimeline, protocol.MethodRunPRStatus),
	opOf("aether_run_history", "Read run history", ScopeRead, true, nextRuns,
		"Use this when the user asks what a run said or did: its conversation or terminal, oldest first, and reports."+untrustedRule, runHistory).
		offer(TierRead).calling(protocol.MethodRunGet, protocol.MethodWorkspaceTimeline, protocol.MethodRunACPHistory, protocol.MethodTerminalHistory),
	opOf("aether_needs_attention", "What needs me", ScopeRead, true, nextWorkspaces,
		"Use this when the user asks what needs them; each item names the tool that answers it."+untrustedRule, needsAttention).
		offer(TierRead).calling(protocol.MethodServerInfo, protocol.MethodAttentionList),
	opOf("aether_activity_summary", "Activity summary", ScopeRead, true, "",
		"Use this when the user asks what their agents did today, yesterday or this week."+untrustedRule, activitySummary).
		enum("period", "today", "yesterday", "week").
		offer(TierRead).calling(protocol.MethodServerInfo, protocol.MethodInsightsGet, protocol.MethodWorkspaceTimeline),
	opOf("aether_list_swarms", "List swarms", ScopeRead, true, nextWorkspaces,
		"Use this when the user asks about their swarms.", listSwarms).
		offer(TierRead).calling(protocol.MethodServerInfo, protocol.MethodWorkspaceList, protocol.MethodMissionList),
	opOf("aether_get_swarm", "Get swarm", ScopeRead, true, nextSwarms,
		"Use this when the user asks about one swarm, and to poll a launched swarm."+untrustedRule, getSwarm).
		offer(TierRead).calling(protocol.MethodMissionShow, protocol.MethodServerInfo, protocol.MethodCoordMessagesList),
	opOf("aether_run_changes", "Run changes", ScopeRead, true, nextRuns,
		"Use this when the user asks what a run changed: commits, predicted conflicts and, with include_diff, the uncommitted diff by file. Page with next_commits_offset and next_diff_file."+untrustedRule, runChanges).
		offer(TierRead).calling(protocol.MethodRunGet, protocol.MethodRunGitLog, protocol.MethodRunEvidenceList, protocol.MethodRunGitDiff),
	opOf("aether_pr_status", "Pull request status", ScopeRead, true, nextRuns,
		"Use this when the user asks about a run's pull request, CI or review feedback. To act on feedback, aether_message_run its run_id; the agent can read the PR itself."+untrustedRule, prStatus).
		offer(TierRead).calling(protocol.MethodRunPRFeedback),
	opOf("aether_overlaps", "Overlapping runs", ScopeRead, true, nextRuns,
		"Use this when coordinating running agents so they don't change the same files: pairs of active runs touching the same files, with titles. Then aether_message_run each run about the other."+untrustedRule, overlaps).
		offer(TierRead).calling(protocol.MethodRunOverlaps, protocol.MethodServerInfo, protocol.MethodWorkspaceList, protocol.MethodRunList),
	opOf("aether_memory_search", "Search team memory", ScopeRead, true, nextWorkspaces,
		"Use this when the user asks what the team decided or learned in a workspace. Each note has its author, run, harness and the commit it was written at."+untrustedRule+" "+coordhooks.MemoryRule, memorySearch).
		enum("kind", "decision", "convention", "gotcha", "preference", "outcome").
		offer(TierRead).calling(protocol.MethodMemorySearch),
	opOf("search", "Search runs and swarms", ScopeRead, true, "",
		"Use this when ChatGPT deep research needs runs or swarms by words; in chat use aether_list_runs with query."+searchRule, search).
		calling(protocol.MethodServerInfo, protocol.MethodRunList, protocol.MethodWorkspaceList, protocol.MethodMissionList),
	opOf("fetch", "Fetch run or swarm", ScopeRead, true, "call search for ids",
		"Use this when deep research needs the full text of a search result."+searchRule, fetch).
		calling(protocol.MethodServerInfo, protocol.MethodRunGet, protocol.MethodWorkspaceTimeline, protocol.MethodMissionShow),
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

func listAgents(ctx context.Context, c calls, _ struct{}) (string, any, error) {
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

// runRow is a run in a list. Its title, the server's reason and the last
// report are written by agents, so they sit under untrusted; needs_you is
// a fixed label for the attention kind.
type runRow struct {
	ID          string   `json:"id"`
	State       string   `json:"state"`
	Activity    string   `json:"activity,omitempty"`
	NeedsYou    string   `json:"needs_you,omitempty"`
	WorkspaceID string   `json:"workspace_id"`
	Workspace   string   `json:"workspace,omitempty"`
	Harness     string   `json:"harness"`
	MissionID   string   `json:"mission_id,omitempty"`
	CreatedAt   string   `json:"created_at"`
	URL         string   `json:"url,omitempty"`
	Untrusted   *runText `json:"untrusted,omitempty"`
}

type runText struct {
	Title      string `json:"title,omitempty"`
	Reason     string `json:"reason,omitempty"`
	LastReport string `json:"last_report,omitempty"`
}

func runTitle(r protocol.Run) string {
	return cmp.Or(clip(r.Title, maxReport), clip(r.Task, 80))
}

func rowOf(r protocol.Run, l links, workspaces map[string]string) runRow {
	return runRow{ID: r.ID, State: r.Status, Activity: r.Activity, WorkspaceID: r.WorkspaceID, Workspace: workspaces[r.WorkspaceID],
		Harness: r.Harness, MissionID: r.MissionID, CreatedAt: r.CreatedAt, URL: l.run(r.ID),
		Untrusted: &runText{Title: runTitle(r), Reason: clip(r.Reason, maxReport)}}
}

func rowLimit(n int) int {
	if n <= 0 {
		return defaultRows
	}
	return min(n, maxRows)
}

func workspaceNames(ctx context.Context, read Call) (map[string]string, error) {
	var res protocol.WorkspaceListResult
	if err := read(ctx, protocol.MethodWorkspaceList, nil, &res); err != nil {
		return nil, err
	}
	names := map[string]string{}
	for _, w := range res.Workspaces {
		names[w.ID] = w.Name
	}
	return names, nil
}

func listRuns(ctx context.Context, c calls, in connectorListRunsInput) (string, any, error) {
	l, err := dashboardLinks(ctx, c.read)
	if err != nil {
		return "", nil, err
	}
	names, err := workspaceNames(ctx, c.read)
	if err != nil {
		return "", nil, err
	}
	params := struct {
		protocol.RunListParams
		Mine bool `json:"mine,omitempty"`
	}{protocol.RunListParams{WorkspaceID: in.WorkspaceID, ActiveOnly: in.ActiveOnly}, *in.Mine}
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
		waiting[it.RunID] = attentionLabel(it.Kind)
	}
	words := strings.Fields(strings.ToLower(in.Query))
	slices.SortStableFunc(res.Runs, func(a, b protocol.Run) int { return strings.Compare(b.CreatedAt, a.CreatedAt) })
	rows, total := []runRow{}, 0
	for _, r := range res.Runs {
		label, needs := waiting[r.ID]
		if (in.NeedsYou && !needs) || !matchesAll(strings.ToLower(strings.Join([]string{r.Title, r.Task, names[r.WorkspaceID], r.Branch}, " ")), words) {
			continue
		}
		if total++; len(rows) == rowLimit(in.Limit) {
			continue
		}
		row := rowOf(r, l, names)
		row.NeedsYou = label
		rows = append(rows, row)
	}
	if err := addLastReports(ctx, c.read, rows); err != nil {
		return "", nil, err
	}
	out := map[string]any{"runs": rows}
	summary := fmt.Sprintf("%d runs.", total)
	if total > len(rows) {
		out["total"], out["truncated"] = total, true
		summary = fmt.Sprintf("%d of %d runs.", len(rows), total)
	}
	return summary, out, nil
}

func addLastReports(ctx context.Context, read Call, rows []runRow) error {
	latest := map[string]report{}
	done := map[string]bool{}
	for _, row := range rows {
		if done[row.WorkspaceID] {
			continue
		}
		done[row.WorkspaceID] = true
		reps, err := reports(ctx, read, row.WorkspaceID, "")
		if err != nil {
			return err
		}
		for _, rep := range reps {
			if _, ok := latest[rep.RunID]; !ok {
				latest[rep.RunID] = rep
			}
		}
	}
	for i := range rows {
		if rep, ok := latest[rows[i].ID]; ok {
			rows[i].Untrusted.LastReport = rep.Outcome + ": " + clip(rep.Summary, maxReport)
		}
	}
	return nil
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

type report struct {
	RunID      string `json:"run_id,omitempty"`
	Outcome    string `json:"outcome"`
	Summary    string `json:"summary"`
	NextAction string `json:"next_action,omitempty"`
	At         string `json:"at"`
}

// timelineEvent is events.TypeTimeline, whose report entries every
// aether_report of any run is projected to.
const timelineEvent = "workspace.timeline"

// reports are the newest reports in a workspace's last 100 timeline
// entries, of one run when runID is set, newest first.
func reports(ctx context.Context, read Call, workspaceID, runID string) ([]report, error) {
	var res protocol.WorkspaceTimelineResult
	if err := read(ctx, protocol.MethodWorkspaceTimeline, protocol.WorkspaceTimelineParams{WorkspaceID: workspaceID, RunID: runID,
		Types: []string{timelineEvent}, Newest: true, Limit: maxRows}, &res); err != nil {
		return nil, err
	}
	out := []report{}
	for _, ev := range slices.Backward(res.Events) {
		var p struct {
			Kind       string `json:"kind"`
			Outcome    string `json:"outcome"`
			Summary    string `json:"summary"`
			NextAction string `json:"next_action"`
		}
		if json.Unmarshal(ev.Payload, &p) != nil || p.Kind != "report" {
			continue
		}
		out = append(out, report{RunID: ev.RunID, Outcome: p.Outcome, Summary: clip(p.Summary, maxText),
			NextAction: clip(p.NextAction, maxText), At: ev.Time})
	}
	return out, nil
}

type pullRequest struct {
	Number     int    `json:"number"`
	URL        string `json:"url"`
	State      string `json:"state"`
	Draft      bool   `json:"draft"`
	BaseBranch string `json:"base_branch,omitempty"`
	HeadOID    string `json:"head_oid,omitempty"`
}

func prOf(p *protocol.RunPullRequest) *pullRequest {
	if p == nil {
		return nil
	}
	return &pullRequest{Number: p.Number, URL: p.URL, State: p.State, Draft: p.Draft, BaseBranch: p.BaseBranch, HeadOID: p.HeadOID}
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
	reps, err := reports(ctx, c.read, r.WorkspaceID, r.ID)
	if err != nil {
		return "", nil, err
	}
	row := rowOf(r, l, nil)
	untrusted := map[string]any{"run": row.Untrusted, "task": clip(r.Task, maxText)}
	row.Untrusted = nil
	out := map[string]any{"run": row, "mode": r.Mode, "model": r.Model, "branch": r.Branch,
		"paused": r.Paused, "pending_inputs": len(r.PendingInputs), "finished_at": r.FinishedAt, "review": r.Review, "untrusted": untrusted}
	summary := fmt.Sprintf("Run %s is %s.", r.ID, r.Status)
	for _, it := range items {
		if it.RunID == r.ID {
			out["needs_you"] = attentionRowOf(it, l)
			summary += " " + attentionLabel(it.Kind) + "."
		}
	}
	if len(reps) > 0 {
		untrusted["latest_report"] = reps[0]
	}
	var pr protocol.RunPRStatusResult
	if err := c.read(ctx, protocol.MethodRunPRStatus, protocol.RunPRStatusParams{RunID: r.ID}, &pr); err != nil {
		pr.Error = err.Error()
	}
	if pr.PullRequest != nil {
		out["pr"] = prOf(pr.PullRequest)
		untrusted["pr_title"] = clip(pr.PullRequest.Title, maxReport)
	}
	if pr.Error != "" {
		out["pr_error"] = pr.Error
	}
	return summary, out, nil
}

const historyExpired = "The server no longer keeps this run's oldest history, so its start is missing."

func runHistory(ctx context.Context, c calls, in historyInput) (string, any, error) {
	r, err := getRunRecord(ctx, c.read, in.RunID)
	if err != nil {
		return "", nil, err
	}
	limit := min(rowLimit(in.Limit), 50)
	reps, err := reports(ctx, c.read, r.WorkspaceID, r.ID)
	if err != nil {
		return "", nil, err
	}
	untrusted := map[string]any{"reports": reps}
	out := map[string]any{"run_id": r.ID, "mode": r.Mode, "untrusted": untrusted}
	if r.ACP {
		var before int64
		if in.Before != "" {
			if before, err = strconv.ParseInt(in.Before, 10, 64); err != nil {
				return "", nil, invalid("before %q is not a next_before this run returned", in.Before)
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
		if len(frames) > 0 && frames[0].Seq > res.OldestSeq {
			out["next_before"] = strconv.FormatInt(frames[0].Seq, 10)
		}
		if res.TruncatedBefore {
			out["truncated_before"], out["note"] = true, historyExpired
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
	if res.TruncatedBefore {
		out["truncated_before"], out["note"] = true, historyExpired
	}
	return fmt.Sprintf("%d terminal lines of run %s.", len(lines), r.ID), out, nil
}

// periodRange is [from, to) for today, yesterday or the last seven days
// in zone, ending tomorrow at midnight.
func periodRange(period, zone string, now time.Time) (time.Time, time.Time, error) {
	loc, err := time.LoadLocation(zone)
	if err != nil || zone == "Local" {
		return time.Time{}, time.Time{}, invalid("time_zone %q is not an IANA time zone such as Europe/Berlin", zone)
	}
	now = now.In(loc)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	switch period {
	case "yesterday":
		return today.AddDate(0, 0, -1), today, nil
	case "week":
		return today.AddDate(0, 0, -6), today.AddDate(0, 0, 1), nil
	}
	return today, today.AddDate(0, 0, 1), nil
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
		Status  string `json:"status"`
		PRURL   string `json:"pr_url,omitempty"`
		PRState string `json:"pr_state,omitempty"`
		URL     string `json:"url,omitempty"`
	}
	type finishedText struct {
		RunID  string `json:"run_id"`
		Title  string `json:"title,omitempty"`
		Reason string `json:"reason,omitempty"`
	}
	recent, texts := []finished{}, []finishedText{}
	inRecent := map[string]bool{}
	workspaces := []string{}
	for _, r := range res.Recent[:min(len(res.Recent), defaultRows)] {
		recent = append(recent, finished{RunID: r.RunID, Status: r.Status, PRURL: r.PRURL, PRState: r.PRState, URL: l.run(r.RunID)})
		texts = append(texts, finishedText{RunID: r.RunID, Title: clip(r.Title, maxReport), Reason: clip(r.Reason, maxReport)})
		inRecent[r.RunID] = true
		if !slices.Contains(workspaces, r.WorkspaceID) {
			workspaces = append(workspaces, r.WorkspaceID)
		}
	}
	latest := []report{}
	seen := map[string]bool{}
	for _, ws := range workspaces {
		reps, err := reports(ctx, c.read, ws, "")
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
		"needs_you": res.NeedsYou, "finished_runs": recent, "untrusted": map[string]any{"finished_runs": texts, "reports": latest}}
	return fmt.Sprintf("%d launched, %d completed, %d merged, %d failed, %d active; %d pull requests merged.",
		t.Launched, t.Completed, t.Merged, t.Failed, t.Active, res.PullRequests.Merged), out, nil
}

type swarmRow struct {
	ID              string `json:"id"`
	WorkspaceID     string `json:"workspace_id"`
	Phase           string `json:"phase"`
	OpenQuestions   int    `json:"open_questions"`
	IntegratorRunID string `json:"integrator_run_id,omitempty"`
	CreatedAt       string `json:"created_at"`
	URL             string `json:"url,omitempty"`
	Untrusted       struct {
		Objective string `json:"objective"`
	} `json:"untrusted"`
}

func swarmRowOf(m protocol.Mission, l links, objective int) swarmRow {
	row := swarmRow{ID: m.ID, WorkspaceID: m.WorkspaceID, Phase: m.Phase, OpenQuestions: m.OpenQuestions,
		IntegratorRunID: m.CurrentIntegratorRunID, CreatedAt: m.CreatedAt, URL: l.swarm(m.ID)}
	row.Untrusted.Objective = clip(m.Objective, objective)
	return row
}

// allSwarms lists the swarms of one workspace, or of every workspace when
// workspaceID is empty, newest first, up to maxSwarmPages pages of
// maxRows per workspace.
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
		cursor := ""
		for range maxSwarmPages {
			var res protocol.MissionListResult
			if err := read(ctx, protocol.MethodMissionList, protocol.MissionListParams{WorkspaceID: id, Limit: maxRows, Before: cursor}, &res); err != nil {
				return nil, err
			}
			out = append(out, res.Missions...)
			if cursor = res.NextCursor; cursor == "" {
				break
			}
		}
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
		rows = append(rows, swarmRowOf(m, l, maxReport))
	}
	out := map[string]any{"swarms": rows}
	if len(missions) > len(rows) {
		out["total"], out["truncated"] = len(missions), true
		return fmt.Sprintf("%d of %d swarms.", len(rows), len(missions)), out, nil
	}
	return fmt.Sprintf("%d swarms.", len(rows)), out, nil
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
		AttemptID string `json:"attempt_id"`
		RunID     string `json:"run_id,omitempty"`
		TaskID    string `json:"task_id"`
		State     string `json:"state"`
		Harness   string `json:"harness"`
		HeldBy    string `json:"control_held_by,omitempty"`
		URL       string `json:"url,omitempty"`
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
			row.Title = clip(t.Revision.Title, maxReport)
		}
		for _, b := range t.Blockers {
			row.Blockers = append(row.Blockers, clip(b.Action, maxReport))
		}
		tasks = append(tasks, row)
	}
	type workerError struct {
		AttemptID string `json:"attempt_id"`
		LastError string `json:"last_error"`
	}
	workers := make([]worker, 0, len(res.Attempts))
	var errs []workerError
	for _, a := range res.Attempts {
		w := worker{AttemptID: a.ID, RunID: a.RunID, TaskID: a.TaskID, State: a.State, Harness: a.Harness, URL: l.run(a.RunID)}
		if a.LastError != "" {
			errs = append(errs, workerError{AttemptID: a.ID, LastError: clip(a.LastError, maxText)})
		}
		if a.TakeoverActive {
			w.HeldBy = a.TakeoverMemberID
		}
		workers = append(workers, w)
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
	out := map[string]any{"swarm": swarmRowOf(m, l, maxText), "integrator_generation": m.IntegratorGeneration,
		"workers": workers, "untrusted": map[string]any{"tasks": tasks, "questions": questions, "messages": messages, "worker_errors": errs}}
	if m.IntegratorLaunchError != "" {
		out["integrator_launch_error"] = m.IntegratorLaunchError
	}
	return fmt.Sprintf("Swarm %s is %s: %d tasks, %d workers, %d open questions.", m.ID, m.Phase, len(tasks), len(workers), m.OpenQuestions), out, nil
}

// diffPage bounds one page of a run's diff: its files, the text of each
// and of the whole page.
const (
	diffPageFiles = 10
	diffFileText  = 6000
	diffPageText  = 12000
)

func runChanges(ctx context.Context, c calls, in changesInput) (string, any, error) {
	r, err := getRunRecord(ctx, c.read, in.RunID)
	if err != nil {
		return "", nil, err
	}
	if in.CommitsOffset < 0 || in.CommitsOffset >= protocol.MaxRunGitLogLimit {
		return "", nil, invalid("commits_offset must be between 0 and %d", protocol.MaxRunGitLogLimit-1)
	}
	out := map[string]any{"run_id": r.ID, "branch": r.Branch}
	if in.Repo != "" {
		out["repo"] = in.Repo
	}
	if len(r.Repos) > 1 {
		type repo struct {
			WorkspaceID string `json:"workspace_id"`
			Name        string `json:"name"`
			Branch      string `json:"branch"`
			Primary     bool   `json:"primary,omitempty"`
		}
		repos := make([]repo, 0, len(r.Repos))
		for _, rp := range r.Repos {
			repos = append(repos, repo{rp.WorkspaceID, rp.Name, rp.Branch, rp.Primary})
		}
		out["repos"] = repos
	}
	untrusted := map[string]any{}
	summary := fmt.Sprintf("Run %s:", r.ID)
	var log protocol.RunGitLogResult
	limit := min(in.CommitsOffset+defaultRows, protocol.MaxRunGitLogLimit)
	switch err := c.read(ctx, protocol.MethodRunGitLog, protocol.RunGitLogParams{RunID: r.ID, Repo: in.Repo, Limit: limit}, &log); {
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
		page := log.Commits[min(in.CommitsOffset, len(log.Commits)):]
		commits := make([]commit, 0, len(page))
		for _, cm := range page {
			commits = append(commits, commit{Short: cm.Short, Subject: clip(cm.Subject, maxReport), Author: cm.Author, At: cm.AuthoredAt})
		}
		if next := in.CommitsOffset + len(commits); len(log.Commits) == limit && next < min(log.Ahead, protocol.MaxRunGitLogLimit) {
			out["next_commits_offset"] = next
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
	if in.Repo == "" {
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
	}
	if in.IncludeDiff {
		var diff protocol.RunGitDiffResult
		switch err := c.read(ctx, protocol.MethodRunGitDiff, protocol.RunGitDiffParams{RunID: r.ID, Repo: in.Repo}, &diff); {
		case err != nil:
			out["diff_error"] = err.Error()
		case diff.Error != "":
			out["diff_error"] = diff.Error
		default:
			files := diffFiles(diff.Output.Stdout)
			if in.DiffFile < 0 || (in.DiffFile > 0 && in.DiffFile >= len(files)) {
				return "", nil, invalid("diff_file %d is past the diff's %d files", in.DiffFile, len(files))
			}
			var page []string
			size := 0
			for _, f := range files[in.DiffFile:] {
				f = clip(f, diffFileText)
				if len(page) == diffPageFiles || (len(page) > 0 && size+len(f) > diffPageText) {
					break
				}
				page, size = append(page, f), size+len(f)
			}
			out["diff_files"] = len(files)
			if next := in.DiffFile + len(page); next < len(files) {
				out["next_diff_file"] = next
			}
			untrusted["diff"] = page
		}
	}
	out["untrusted"] = untrusted
	return summary, out, nil
}

// diffFiles splits a git diff into one part per file.
func diffFiles(diff string) []string {
	var files []string
	for part := range strings.SplitSeq(diff, "\ndiff --git ") {
		if part == "" {
			continue
		}
		if len(files) > 0 {
			part = "diff --git " + part
		}
		files = append(files, part)
	}
	return files
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
	type check struct {
		Name       string `json:"name"`
		Conclusion string `json:"conclusion"`
		URL        string `json:"url,omitempty"`
	}
	var passed, pending int
	failing := []check{}
	for _, ch := range res.Checks {
		switch strings.ToUpper(ch.Conclusion) {
		case "SUCCESS", "NEUTRAL", "SKIPPED":
			passed++
		case "":
			pending++
		default:
			failing = append(failing, check{Name: clip(ch.Name, maxReport), Conclusion: ch.Conclusion, URL: ch.URL})
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
		reviews = append(reviews, review{Author: rv.Author, State: rv.State, Body: clip(rv.Body, maxFeedback)})
	}
	comments := make([]comment, 0, len(res.Comments)+len(res.ReviewComments))
	for _, cm := range res.Comments {
		comments = append(comments, comment{Author: cm.Author, Body: clip(cm.Body, maxFeedback), URL: cm.URL})
	}
	for _, cm := range res.ReviewComments {
		comments = append(comments, comment{Author: cm.Author, Path: cm.Path, Line: cm.Line, Body: clip(cm.Body, maxFeedback), URL: cm.URL})
	}
	pr := prOf(res.PullRequest)
	out := map[string]any{"run_id": in.RunID, "pr": pr, "mergeable": res.Mergeable, "merge_state": res.MergeStateStatus,
		"checks":    map[string]int{"passed": passed, "pending": pending, "failing": len(failing)},
		"untrusted": map[string]any{"title": clip(res.PullRequest.Title, maxReport), "failing_checks": failing, "reviews": reviews, "comments": comments}}
	if res.Error != "" {
		out["error"] = res.Error
	}
	counted := ""
	if res.Truncated {
		out["truncated"] = true
		counted = " (truncated: at least these many)"
	}
	return fmt.Sprintf("PR #%d is %s: %d checks passed, %d failing, %d pending; %d reviews, %d comments%s; merge state %s.",
		pr.Number, pr.State, passed, len(failing), pending, len(reviews), len(comments), counted, cmp.Or(res.MergeStateStatus, "unknown")), out, nil
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
	names, err := workspaceNames(ctx, c.read)
	if err != nil {
		return "", nil, err
	}
	params := struct {
		protocol.RunListParams
		Mine bool `json:"mine"`
	}{protocol.RunListParams{ActiveOnly: true}, false}
	var active protocol.RunListResult
	if err := c.read(ctx, protocol.MethodRunList, params, &active); err != nil {
		return "", nil, err
	}
	runs := map[string]protocol.Run{}
	for _, r := range active.Runs {
		runs[r.ID] = r
	}
	type side struct {
		RunID     string `json:"run_id"`
		Workspace string `json:"workspace,omitempty"`
		URL       string `json:"url,omitempty"`
	}
	type pair struct {
		Runs      [2]side  `json:"runs"`
		Files     []string `json:"files"`
		Untrusted struct {
			Titles [2]string `json:"titles"`
		} `json:"untrusted"`
	}
	sideOf := func(id string) side {
		return side{RunID: id, Workspace: names[runs[id].WorkspaceID], URL: l.run(id)}
	}
	// run.overlaps reports every pair from both sides; the pair is kept
	// from the side whose run id sorts first.
	pairs := []pair{}
	for _, o := range res.Overlaps {
		for _, p := range o.With {
			if o.RunID > p.RunID || (in.RunID != "" && in.RunID != o.RunID && in.RunID != p.RunID) {
				continue
			}
			row := pair{Runs: [2]side{sideOf(o.RunID), sideOf(p.RunID)}, Files: p.Files}
			row.Untrusted.Titles = [2]string{runTitle(runs[o.RunID]), runTitle(runs[p.RunID])}
			pairs = append(pairs, row)
		}
	}
	return fmt.Sprintf("%d overlapping pairs of active runs.", len(pairs)), map[string]any{"pairs": pairs}, nil
}

func memorySearch(ctx context.Context, c calls, in memoryInput) (string, any, error) {
	var res protocol.MemorySearchResult
	if err := c.read(ctx, protocol.MethodMemorySearch, protocol.MemorySearchParams{WorkspaceID: in.WorkspaceID, Words: in.Words, Kind: in.Kind, File: in.File, All: in.All}, &res); err != nil {
		return "", nil, err
	}
	notes := make([]note, 0, len(res.Items))
	for _, m := range res.Items {
		notes = append(notes, noteOf(m, maxText))
	}
	return fmt.Sprintf("%d notes.", len(notes)), map[string]any{"untrusted": map[string]any{"notes": notes}}, nil
}
