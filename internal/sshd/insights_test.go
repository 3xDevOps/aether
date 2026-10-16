package sshd

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/domain"
	githubservice "github.com/3xDevOps/Aether/internal/github"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/runrepo"
	"github.com/3xDevOps/Aether/internal/scheduler"
	"github.com/3xDevOps/Aether/internal/store"
	"github.com/3xDevOps/Aether/internal/store/storetest"
)

// TestInsightsGet seeds two members' runs across the 2026-03-08 Los Angeles
// DST change and checks every insights.get number against hand counts.
func TestInsightsGet(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "aether.db")
	db, err := storetest.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	raw, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(0)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := raw.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}

	at := func(s string) time.Time {
		t.Helper()
		v, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatal(err)
		}
		return v.UTC()
	}
	ptr := func(s string) *time.Time {
		v := at(s)
		return &v
	}
	gh := newGitHubRepositoryFixture()
	gh.pr = githubservice.PullRequest{URL: "https://github.com/acme/app/pull/4", State: "merged", MergedAt: ptr("2026-03-09T08:00:00-07:00")}
	e := newTestEnv(t, func(c *Config) {
		c.Store = db
		c.Services.RunRepo = goneRunRepo{}
		c.Services.GitHub = gh
	})
	member := func(name string) domain.MemberID {
		m := &domain.Member{DisplayName: name, PublicKey: string(ssh.MarshalAuthorizedKey(newSigner(t).PublicKey())), Color: "#e6194b", Role: domain.RoleCollaborator}
		if err := db.CreateMember(ctx, m); err != nil {
			t.Fatal(err)
		}
		return m.ID
	}
	alice, bob := member("alice"), member("bob")
	workspace := func(name string) domain.WorkspaceID {
		w := &domain.Workspace{Name: name, BaseBranch: "main"}
		if err := db.CreateWorkspace(ctx, w); err != nil {
			t.Fatal(err)
		}
		return w.ID
	}
	alpha, beta := workspace("alpha"), workspace("beta")
	run := func(id string, owner domain.MemberID, ws domain.WorkspaceID, harness, model string, status domain.RunStatus, reason, created string, started, finished *time.Time) {
		t.Helper()
		r := &domain.Run{ID: domain.RunID(id), WorkspaceID: ws, MemberID: owner, Task: id, Title: "title " + id,
			Harness: harness, Model: model, Mode: domain.LaunchHeadless, Status: status, Reason: reason,
			CreatedAt: at(created), StartedAt: started, FinishedAt: finished}
		if err := db.CreateRunWithID(ctx, r); err != nil {
			t.Fatal(err)
		}
	}

	// Swarms reserve their runs' IDs before creating them.
	nanos := func(s string) int64 { return at(s).UnixNano() }
	mission := func(id string, owner domain.MemberID, ws domain.WorkspaceID, phase, created string) {
		exec(`INSERT INTO missions (id, workspace_id, objective, accountable_human_id, idempotency_key, created_at, updated_at, phase)
			VALUES (?, ?, 'o', ?, ?, ?, ?, ?)`, id, ws, owner, id, nanos(created), nanos(created), phase)
	}
	mission("m1", alice, alpha, "completed", "2026-03-07T11:30:00-08:00")
	mission("m2", bob, beta, "cancelled", "2026-03-08T12:00:00-07:00")
	exec(`UPDATE missions SET current_integrator_run_id = 'i2' WHERE id = 'm1'`)
	exec(`INSERT INTO mission_create_receipts (workspace_id, idempotency_key, mission_id, objective, accountable_human_id,
		integrator_account_member_id, integrator_harness, integrator_mode, execution_choices, created_at, initial_run_id)
		VALUES (?, 'm1', 'm1', 'o', ?, ?, 'claude', 'headless', '[]', 0, 'i1')`, alpha, alice, alice)
	exec(`INSERT INTO mission_integrator_replacements (mission_id, idempotency_key, account_member_id, harness, mode, generation, run_id, created_at)
		VALUES ('m1', 'r1', ?, 'claude', 'headless', 2, 'i2', 0)`, alice)
	attempt := func(id, mission, runID string, number int) {
		exec(`INSERT INTO mission_attempts (id, mission_id, task_id, task_revision, number, dispatch_key, state, run_id, created_at, reserved_at)
			VALUES (?, ?, 't', 1, ?, ?, 'completed', ?, 0, 0)`, id, mission, number, id, runID)
	}
	attempt("a1", "m1", "w1", 1)
	attempt("a2", "m1", "w1b", 2)
	attempt("a3", "m2", "wb", 1)
	// Reserved, but the launch never created the run.
	attempt("a4", "m1", "ghost", 3)

	// Alice's top-level runs. top2 starts at 23:30 PDT on 03-08, which is
	// 03-09 in UTC; top4 ends at 01:00 PST on 03-08, before the change.
	run("top1", alice, alpha, "claude", "", domain.RunCompleted, "", "2026-03-07T10:00:00-08:00", ptr("2026-03-07T10:01:00-08:00"), ptr("2026-03-07T11:01:00-08:00"))
	run("top2", alice, alpha, "codex", "gpt-5", domain.RunFailed, "", "2026-03-08T23:30:00-07:00", nil, ptr("2026-03-09T00:30:00-07:00"))
	run("top3", alice, beta, "claude", "", domain.RunRunning, "", "2026-03-09T09:00:00-07:00", ptr("2026-03-09T09:00:00-07:00"), nil)
	run("top4", alice, alpha, "claude", "", domain.RunAbandoned, "killed", "2026-03-06T12:00:00-08:00", ptr("2026-03-06T12:00:00-08:00"), ptr("2026-03-08T01:00:00-08:00"))
	run("top6", alice, alpha, "claude", "", domain.RunNeedsAttention, "", "2026-03-01T12:00:00-08:00", ptr("2026-03-01T12:00:00-08:00"), nil)
	// Alice's swarm: a failed worker, its retry, and two integrator
	// generations; none of them is top-level.
	run("w1", alice, alpha, "claude", "", domain.RunFailed, "", "2026-03-07T12:00:00-08:00", ptr("2026-03-07T12:00:00-08:00"), ptr("2026-03-07T13:00:00-08:00"))
	run("w1b", alice, alpha, "claude", "", domain.RunCompleted, "", "2026-03-07T13:00:00-08:00", ptr("2026-03-07T13:00:00-08:00"), ptr("2026-03-07T14:00:00-08:00"))
	run("i1", alice, alpha, "claude", "", domain.RunFailed, "", "2026-03-07T11:30:00-08:00", ptr("2026-03-07T11:30:00-08:00"), ptr("2026-03-07T11:45:00-08:00"))
	run("i2", alice, alpha, "claude", "", domain.RunNeedsAttention, "", "2026-03-07T11:45:00-08:00", ptr("2026-03-07T11:45:00-08:00"), nil)
	// Bob's runs and swarm.
	run("top5", bob, beta, "claude", "opus", domain.RunCompleted, "", "2026-03-08T09:00:00-07:00", ptr("2026-03-08T09:00:00-07:00"), ptr("2026-03-08T10:00:00-07:00"))
	run("top7", bob, beta, "claude", "opus", domain.RunNeedsAttention, "", "2026-03-09T08:00:00-07:00", ptr("2026-03-09T08:00:00-07:00"), nil)
	run("wb", bob, beta, "claude", "", domain.RunAbandoned, "", "2026-03-08T12:00:00-07:00", ptr("2026-03-08T12:00:00-07:00"), ptr("2026-03-08T13:00:00-07:00"))

	exec(`INSERT INTO mission_acceptances (submission_id, mission_id, task_id, task_revision, accepted_set_version, integrator_generation, accepted_by_run_id, accepted_at)
		VALUES ('s1', 'm1', 't', 1, 1, 2, 'i2', 0)`)

	transition := func(runID, from, to, when string) {
		exec(`INSERT INTO run_state_transitions (run_id, workspace_id, from_status, to_status, reason, at) VALUES (?, ?, ?, ?, '', ?)`,
			runID, alpha, from, to, nanos(when))
	}
	transition("top1", "queued", "provisioning", "2026-03-07T10:00:30-08:00")
	transition("top1", "provisioning", "running", "2026-03-07T10:01:00-08:00")
	transition("top1", "running", "needs-attention", "2026-03-07T10:31:00-08:00")
	transition("top1", "needs-attention", "running", "2026-03-07T10:36:00-08:00")
	transition("top1", "running", "completed", "2026-03-07T11:01:00-08:00")
	transition("top1", "completed", "merged", "2026-03-07T12:01:00-08:00")
	// A relaunch rolled back before it ran restores the row's
	// status_changed_at, so it sorts before the relaunch and leaves the
	// session before it measured.
	transition("top1", "merged", "running", "2026-03-07T12:30:00-08:00")
	transition("top1", "running", "merged", "2026-03-07T12:01:00-08:00")
	transition("top1", "merged", "merged", "2026-03-07T13:00:00-08:00")
	// Relaunched after completing: only the last session is measured.
	transition("top4", "queued", "provisioning", "2026-03-06T11:59:00-08:00")
	transition("top4", "provisioning", "running", "2026-03-06T12:00:00-08:00")
	transition("top4", "running", "completed", "2026-03-06T14:00:00-08:00")
	transition("top4", "completed", "running", "2026-03-07T23:00:00-08:00")
	transition("top4", "running", "needs-attention", "2026-03-07T23:30:00-08:00")
	transition("top4", "needs-attention", "abandoned", "2026-03-08T01:00:00-08:00")
	// Recorded only from the middle of the run, so not measured.
	transition("top2", "running", "failed", "2026-03-09T00:30:00-07:00")

	cost := func(runID string, segment int, ws domain.WorkspaceID, payer domain.MemberID, usd float64, in, out int64, metered bool) {
		if err := db.PutRunCost(ctx, &store.RunCost{RunID: domain.RunID(runID), Segment: segment, WorkspaceID: ws, MemberID: payer,
			CostUSD: usd, InputTokens: in, OutputTokens: out, Metered: metered}); err != nil {
			t.Fatal(err)
		}
	}
	cost("top1", 0, alpha, alice, 1.5, 1000, 100, true)
	cost("top1", 1, alpha, bob, 0.5, 500, 50, true)
	cost("top2", 0, alpha, alice, 0, 0, 0, false)
	cost("top5", 0, beta, bob, 2, 10, 1, true)
	cost("w1", 0, alpha, alice, 10, 99, 9, true)

	pr := func(runID string, number int, state, observed string, merged *time.Time, opened bool) {
		p := &store.RunPullRequest{RunID: domain.RunID(runID), Repository: "acme/app", Number: number,
			URL: "https://github.com/acme/app/pull/" + runID, State: state, MergedAt: merged, ObservedAt: at(observed)}
		record := db.ObserveRunPullRequest
		if opened {
			record = db.RecordRunPullRequest
		}
		if err := record(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	pr("top1", 1, "open", "2026-03-07T11:00:00-08:00", nil, true)
	pr("top1", 1, "merged", "2026-03-09T13:00:00-07:00", ptr("2026-03-09T12:00:00-07:00"), false)
	pr("w1b", 2, "open", "2026-03-07T13:30:00-08:00", nil, true)
	// Observed but never opened through run.pr.create: not recorded.
	pr("top5", 3, "open", "2026-03-08T10:00:00-07:00", nil, false)

	get := func(caller domain.MemberID, params protocol.InsightsParams) (protocol.InsightsResult, *protocol.Error) {
		t.Helper()
		body, err := json.Marshal(params)
		if err != nil {
			t.Fatal(err)
		}
		out, perr := e.srv.dispatch(ctx, caller, protocol.MethodInsightsGet, body)
		var res protocol.InsightsResult
		if perr == nil {
			if err := json.Unmarshal(out, &res); err != nil {
				t.Fatal(err)
			}
		}
		return res, perr
	}
	week := protocol.InsightsParams{Scope: "mine", From: "2026-03-07T00:00:00-08:00", To: "2026-03-10T00:00:00-07:00", TimeZone: "America/Los_Angeles"}
	with := func(f func(*protocol.InsightsParams)) protocol.InsightsParams {
		p := week
		f(&p)
		return p
	}

	mine, perr := get(alice, week)
	if perr != nil {
		t.Fatalf("insights.get: %+v", perr)
	}
	everyone, perr := get(alice, with(func(p *protocol.InsightsParams) { p.Scope = "everyone" }))
	if perr != nil {
		t.Fatalf("insights.get everyone: %+v", perr)
	}
	bobs, perr := get(bob, week)
	if perr != nil {
		t.Fatalf("insights.get bob: %+v", perr)
	}
	betaOnly, perr := get(alice, with(func(p *protocol.InsightsParams) { p.WorkspaceID = string(beta) }))
	if perr != nil {
		t.Fatalf("insights.get beta: %+v", perr)
	}

	checks := []struct {
		name      string
		got, want any
	}{
		{"bucket", mine.Bucket, "day"},
		{"time zone", mine.TimeZone, "America/Los_Angeles"},
		{"totals", mine.Totals, protocol.InsightsTotals{Launched: 3, Completed: 1, Failed: 1, Abandoned: 1, Killed: 1, Active: 1, Finished: 1}},
		{"previous", mine.Previous, protocol.InsightsTotals{Launched: 1}},
		{"series", mine.Series, []protocol.InsightsSeriesPoint{
			{Start: "2026-03-07", Launched: 1, Completed: 1},
			{Start: "2026-03-08", Launched: 1, Abandoned: 1},
			{Start: "2026-03-09", Launched: 1, Failed: 1},
		}},
		{"heatmap length", len(mine.Heatmap), 182},
		{"heatmap tail", mine.Heatmap[172:], []protocol.InsightsHeatmapDay{
			{Date: "2026-02-28", Runs: 0}, {Date: "2026-03-01", Runs: 1}, {Date: "2026-03-02", Runs: 1},
			{Date: "2026-03-03", Runs: 1}, {Date: "2026-03-04", Runs: 1}, {Date: "2026-03-05", Runs: 1},
			{Date: "2026-03-06", Runs: 2}, {Date: "2026-03-07", Runs: 3}, {Date: "2026-03-08", Runs: 3},
			{Date: "2026-03-09", Runs: 3},
		}},
		{"by workspace", mine.ByWorkspace, []protocol.InsightsWorkspace{
			{WorkspaceID: string(alpha), Name: "alpha", Launched: 2, Finished: 1, Failed: 1},
			{WorkspaceID: string(beta), Name: "beta", Launched: 1},
		}},
		{"by agent", mine.ByAgent, []protocol.InsightsAgent{
			{Harness: "claude", Launched: 2, Finished: 1},
			{Harness: "codex", Model: "gpt-5", Launched: 1, Failed: 1},
		}},
		{"swarms", mine.Swarms, protocol.InsightsSwarms{Launched: 1, Completed: 1, Agents: 4, TasksAccepted: 1}},
		{"durations", mine.Durations, protocol.InsightsDurations{RunsMeasured: 3, MedianRunSeconds: 3600, P90RunSeconds: 37 * 3600,
			StateRunsMeasured: 2, MedianWorkingSeconds: 30 * 60, MedianWaitingSeconds: 300, MedianResponseSeconds: 300}},
		{"cost", mine.Cost, protocol.InsightsCost{MeteredRuns: 1, UnmeteredRuns: 2, CostUSD: 2, InputTokens: 1500, OutputTokens: 150}},
		{"pull requests", mine.PullRequests, protocol.InsightsPullRequests{Opened: 2, Merged: 1}},
		{"recent order", recentIDs(mine.Recent), []string{"top2", "top4", "top1"}},
		{"recent top1", mine.Recent[2], protocol.InsightsRecentRun{RunID: "top1", Title: "title top1", WorkspaceID: string(alpha),
			Status: "completed", Harness: "claude", CreatedAt: "2026-03-07T18:00:00Z", FinishedAt: "2026-03-07T19:01:00Z",
			DurationSeconds: 3600, PRURL: "https://github.com/acme/app/pull/top1", PRState: "merged"}},
		{"needs you", mine.NeedsYou, 2},
		{"coverage", mine.Coverage, protocol.InsightsCoverage{StateTimesSince: "2026-03-07T18:00:30Z", PullRequestsSince: "2026-03-07T19:00:00Z"}},

		{"everyone totals", everyone.Totals, protocol.InsightsTotals{Launched: 5, Completed: 2, Failed: 1, Abandoned: 1, Killed: 1, Active: 2, Finished: 2}},
		{"everyone swarms", everyone.Swarms, protocol.InsightsSwarms{Launched: 2, Completed: 1, Cancelled: 1, Agents: 5, TasksAccepted: 1}},
		{"everyone cost", everyone.Cost, protocol.InsightsCost{MeteredRuns: 2, UnmeteredRuns: 2, CostUSD: 4, InputTokens: 1510, OutputTokens: 151}},
		{"everyone needs you", everyone.NeedsYou, 3},
		{"everyone recent", recentIDs(everyone.Recent), []string{"top2", "top5", "top4", "top1"}},

		{"bob totals", bobs.Totals, protocol.InsightsTotals{Launched: 2, Completed: 1, Active: 1, Finished: 1}},
		{"bob swarms", bobs.Swarms, protocol.InsightsSwarms{Launched: 1, Cancelled: 1, Agents: 1}},
		{"bob recent", recentIDs(bobs.Recent), []string{"top5"}},
		{"bob pull requests", bobs.PullRequests, protocol.InsightsPullRequests{}},
		{"bob needs you", bobs.NeedsYou, 1},

		{"workspace filter", betaOnly.Totals, protocol.InsightsTotals{Launched: 1, Active: 1}},
		{"workspace filter swarms", betaOnly.Swarms, protocol.InsightsSwarms{}},
	}
	for _, c := range checks {
		if !reflect.DeepEqual(c.got, c.want) {
			t.Errorf("%s = %+v, want %+v", c.name, c.got, c.want)
		}
	}

	quarter, perr := get(alice, with(func(p *protocol.InsightsParams) {
		p.From = "2026-01-01T00:00:00-08:00"
		p.To = "2026-05-01T00:00:00-07:00"
	}))
	if perr != nil {
		t.Fatalf("insights.get quarter: %+v", perr)
	}
	launched := 0
	for _, point := range quarter.Series {
		launched += point.Launched
	}
	if quarter.Bucket != "week" || quarter.Series[0].Start != "2025-12-29" || quarter.Series[len(quarter.Series)-1].Start != "2026-04-27" ||
		launched != quarter.Totals.Launched || quarter.Totals.Launched != 5 {
		t.Errorf("quarter = bucket %s, first %s, last %s, series launched %d, totals %+v",
			quarter.Bucket, quarter.Series[0].Start, quarter.Series[len(quarter.Series)-1].Start, launched, quarter.Totals)
	}
	if start := quarter.Series[10].Start; start != "2026-03-09" {
		t.Errorf("week across the DST change starts %s, want 2026-03-09", start)
	}

	for name, params := range map[string]protocol.InsightsParams{
		"to before from":    with(func(p *protocol.InsightsParams) { p.To = p.From }),
		"over 400 days":     with(func(p *protocol.InsightsParams) { p.From = "2025-01-01T00:00:00-08:00" }),
		"unknown time zone": with(func(p *protocol.InsightsParams) { p.TimeZone = "Mars/Olympus_Mons" }),
		"local time zone":   with(func(p *protocol.InsightsParams) { p.TimeZone = "Local" }),
		"bad scope":         with(func(p *protocol.InsightsParams) { p.Scope = "all" }),
		"bad from":          with(func(p *protocol.InsightsParams) { p.From = "2026-03-07" }),
		"unstorable dates": with(func(p *protocol.InsightsParams) {
			p.From, p.To = "2263-01-01T00:00:00Z", "2263-01-02T00:00:00Z"
		}),
	} {
		if _, bad := get(alice, params); bad == nil || bad.Code != protocol.CodeInvalidParams {
			t.Errorf("%s: error = %+v, want invalid params", name, bad)
		}
	}
	if _, missing := get(alice, with(func(p *protocol.InsightsParams) { p.WorkspaceID = "ws-missing" })); missing == nil || missing.Code != protocol.CodeNotFound {
		t.Errorf("unknown workspace: error = %+v, want not found", missing)
	}
	utc, perr := get(alice, with(func(p *protocol.InsightsParams) { p.TimeZone = "" }))
	if perr != nil || utc.TimeZone != "UTC" || utc.Series[0].Start != "2026-03-07" {
		t.Errorf("empty time zone = %+v, %+v; want UTC buckets", utc.TimeZone, perr)
	}

	// A range that is not midnight-aligned is compared with the 47 hours
	// before it, which start after top6 was launched; the three local days
	// it touches would reach back past it.
	shifted, perr := get(alice, with(func(p *protocol.InsightsParams) {
		p.From = "2026-03-03T13:00:00-08:00"
		p.To = "2026-03-05T12:00:00-08:00"
	}))
	if perr != nil || shifted.Previous != (protocol.InsightsTotals{}) {
		t.Errorf("previous of a non-aligned range = %+v, %+v; want none", shifted.Previous, perr)
	}

	// top2 opened a PR that merged after its container was gone; top3,
	// publishing the same branch, recorded the same PR, and a stale open
	// observation of top1's must not undo its merge.
	// top2 ran on bob's shared agent login in alice's home, so gh there used
	// alice's GitHub login.
	exec(`UPDATE runs SET account_member_id = ? WHERE id = 'top2'`, bob)
	if err := db.ShareAccount(ctx, bob, alice); err != nil {
		t.Fatal(err)
	}
	pr("top2", 4, "open", "2026-03-09T00:00:00-07:00", nil, true)
	pr("top3", 4, "open", "2026-03-09T09:30:00-07:00", nil, true)
	pr("top1", 1, "open", "2026-03-09T14:00:00-07:00", nil, false)
	status := func(runID string) (protocol.RunPRStatusResult, *protocol.Error) {
		t.Helper()
		out, statusErr := e.srv.dispatch(ctx, alice, protocol.MethodRunPRStatus, repoParams(t, protocol.RunPRStatusParams{RunID: runID}))
		var res protocol.RunPRStatusResult
		if statusErr == nil {
			if err := json.Unmarshal(out, &res); err != nil {
				t.Fatal(err)
			}
		}
		return res, statusErr
	}
	lookup := []string{string(alice) + " acme/app#4"}
	observed, perr := status("top2")
	if perr != nil || observed.PullRequest == nil || observed.PullRequest.State != "merged" || observed.Identity != "octocat" || observed.AccountMemberID != string(bob) || !reflect.DeepEqual(gh.lookups, lookup) {
		t.Errorf("run.pr.status of a finished run = %+v, %+v, lookups %v; want the merge observed with the run home's GitHub login", observed, perr, gh.lookups)
	}
	again, perr := status("top2")
	if perr != nil || again.PullRequest.State != "merged" || !reflect.DeepEqual(gh.lookups, lookup) {
		t.Errorf("run.pr.status of a merged PR = %+v, %+v, lookups %v; want the recorded state without asking GitHub", again, perr, gh.lookups)
	}
	if _, perr = status("top6"); perr == nil || perr.Code != protocol.CodeInvalidState {
		t.Errorf("run.pr.status without a live environment or a recorded PR: error = %+v, want invalid state", perr)
	}
	merged, perr := get(alice, week)
	if perr != nil || merged.PullRequests != (protocol.InsightsPullRequests{Opened: 3, Merged: 2}) {
		t.Errorf("pull requests after the merge was observed = %+v, %+v", merged.PullRequests, perr)
	}

	for _, table := range []string{"mission_attempts", "mission_create_receipts", "mission_integrator_replacements", "missions"} {
		exec(`DELETE FROM ` + table)
	}
	unlinked, perr := get(alice, week)
	if perr != nil || unlinked.Totals != mine.Totals || !reflect.DeepEqual(recentIDs(unlinked.Recent), recentIDs(mine.Recent)) {
		t.Errorf("after deleting the swarm = totals %+v, recent %v, %+v; want its runs still not top-level", unlinked.Totals, recentIDs(unlinked.Recent), perr)
	}

	top5, getErr := db.GetRun(ctx, "top5")
	if getErr != nil {
		t.Fatal(getErr)
	}
	restored, relaunched := *top5.StatusChangedAt, at("2026-03-10T09:00:00-07:00")
	for _, step := range []struct {
		status domain.RunStatus
		at     time.Time
	}{{domain.RunRunning, relaunched}, {domain.RunCompleted, restored}} {
		top5.Status, top5.StatusChangedAt = step.status, &step.at
		if err := db.UpdateRun(ctx, top5); err != nil {
			t.Fatal(err)
		}
	}
	var stamps []int64
	rows, queryErr := raw.QueryContext(ctx, `SELECT at FROM run_state_transitions WHERE run_id = 'top5' ORDER BY rowid`)
	if queryErr != nil {
		t.Fatal(queryErr)
	}
	for rows.Next() {
		var stamp int64
		if err := rows.Scan(&stamp); err != nil {
			t.Fatal(err)
		}
		stamps = append(stamps, stamp)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if want := []int64{relaunched.UnixNano(), restored.UnixNano()}; !reflect.DeepEqual(stamps, want) {
		t.Errorf("UpdateRun transition times = %v, want the rows' status_changed_at %v", stamps, want)
	}

	if err := db.DeleteRun(ctx, "top1"); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := raw.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM run_state_transitions WHERE run_id = 'top1')
		+ (SELECT COUNT(*) FROM run_pull_requests WHERE run_id = 'top1')`).Scan(&left); err != nil || left != 0 {
		t.Errorf("rows left after deleting the run = %d, %v; want 0", left, err)
	}
}

type goneRunRepo struct{}

func (goneRunRepo) Native() *runrepo.Service { return runrepo.New(nil) }
func (goneRunRepo) Execution(context.Context, domain.Run) (runrepo.Execution, error) {
	return runrepo.Execution{}, scheduler.ErrNoLiveEnvironment
}

func recentIDs(runs []protocol.InsightsRecentRun) []string {
	ids := make([]string, len(runs))
	for i, r := range runs {
		ids[i] = r.RunID
	}
	return ids
}
