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
	"github.com/3xDevOps/Aether/internal/protocol"
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
	e := newTestEnv(t, func(c *Config) { c.Store = db })
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
			StateRunsMeasured: 1, MedianWorkingSeconds: 55 * 60, MedianWaitingSeconds: 300, MedianResponseSeconds: 300}},
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

	if err := db.DeleteRun(ctx, "top1"); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := raw.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM run_state_transitions WHERE run_id = 'top1')
		+ (SELECT COUNT(*) FROM run_pull_requests WHERE run_id = 'top1')`).Scan(&left); err != nil || left != 0 {
		t.Errorf("rows left after deleting the run = %d, %v; want 0", left, err)
	}
}

func recentIDs(runs []protocol.InsightsRecentRun) []string {
	ids := make([]string, len(runs))
	for i, r := range runs {
		ids[i] = r.RunID
	}
	return ids
}
