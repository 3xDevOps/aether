package sshd

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"slices"
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
	gh.snap = githubservice.Snapshot{State: "merged", Draft: true, MergedAt: ptr("2026-03-09T08:00:00-07:00"), Title: "Fix login", URL: "https://github.com/acme/app/pull/4",
		HeadSHA: "0123456789abcdef0123456789abcdef01234567", BaseRef: "main",
		CheckRuns: []githubservice.CheckRun{{ID: 1, Name: "test", Status: "completed", Conclusion: "failure", SuiteID: 7, App: "GitHub Actions"},
			{ID: 2, Name: "test", Status: "completed", Conclusion: "success", SuiteID: 7, App: "GitHub Actions"},
			{ID: 3, Name: "lint", Status: "in_progress", SuiteID: 7, App: "GitHub Actions"}},
		Statuses: []githubservice.CommitStatus{{ID: 4, Context: "ci/legacy", State: "failure"}},
		Reviews:  []githubservice.Review{{ID: 5, State: "CHANGES_REQUESTED", User: "grace", Body: "rename it…", Clipped: true}},
	}
	e := newTestEnv(t, func(c *Config) {
		c.Store = db
		c.Services.RunRepo = goneRunRepo{}
		c.Services.GitHub = gh
	})
	colors := map[string]string{"alice": "#e6194b", "bob": "#3cb44b", "carol": "#4363d8"}
	member := func(name string) domain.MemberID {
		m := &domain.Member{DisplayName: name, PublicKey: string(ssh.MarshalAuthorizedKey(newSigner(t).PublicKey())), Color: colors[name], Role: domain.RoleCollaborator}
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

	nanos := func(s string) int64 { return at(s).UnixNano() }

	// Alice's top-level runs. top2 starts at 23:30 PDT on 03-08, which is
	// 03-09 in UTC; top4 ends at 01:00 PST on 03-08, before the change.
	run("top1", alice, alpha, "claude", "", domain.RunCompleted, "", "2026-03-07T10:00:00-08:00", ptr("2026-03-07T10:01:00-08:00"), ptr("2026-03-07T11:01:00-08:00"))
	run("top2", alice, alpha, "codex", "gpt-5", domain.RunFailed, "", "2026-03-08T23:30:00-07:00", nil, ptr("2026-03-09T00:30:00-07:00"))
	exec(`UPDATE runs SET title = '', task = ? WHERE id = 'top2'`, "\n  fix the parser  \nthen the printer")
	run("top3", alice, beta, "claude", "", domain.RunRunning, "", "2026-03-09T09:00:00-07:00", ptr("2026-03-09T09:00:00-07:00"), nil)
	run("top4", alice, alpha, "claude", "", domain.RunAbandoned, "killed", "2026-03-06T12:00:00-08:00", ptr("2026-03-06T12:00:00-08:00"), ptr("2026-03-08T01:00:00-08:00"))
	run("top6", alice, alpha, "claude", "", domain.RunNeedsAttention, "", "2026-03-01T12:00:00-08:00", ptr("2026-03-01T12:00:00-08:00"), nil)
	// Needs you counts by domain.Run.NeedsAttention: top3's agent
	// disconnected; paused top6 does not.
	exec(`UPDATE runs SET activity = 'disconnected' WHERE id = 'top3'`)
	exec(`UPDATE runs SET activity = 'needs-you', reason = 'needs input: which?' WHERE id = 'top6'`)
	e.runs.setPaused("top6", true)
	run("top5", bob, beta, "claude", "opus", domain.RunCompleted, "", "2026-03-08T09:00:00-07:00", ptr("2026-03-08T09:00:00-07:00"), ptr("2026-03-08T10:00:00-07:00"))
	run("top7", bob, beta, "claude", "opus", domain.RunNeedsAttention, "", "2026-03-09T08:00:00-07:00", ptr("2026-03-09T08:00:00-07:00"), nil)
	exec(`UPDATE runs SET activity = 'needs-you', reason = 'blocked: no token' WHERE id = 'top7'`)

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

	// carol's run parks waiting on its agents, then asks
	// her: only the needs-you stretch is waiting on her.
	carol := member("carol")
	run("top8", carol, alpha, "claude", "", domain.RunCompleted, "", "2026-03-12T09:59:00-07:00", ptr("2026-03-12T10:00:00-07:00"), ptr("2026-03-12T10:50:00-07:00"))
	step := func(from, to string, activity domain.RunActivity, when string) {
		exec(`INSERT INTO run_state_transitions (run_id, workspace_id, from_status, to_status, reason, activity, at) VALUES ('top8', ?, ?, ?, '', ?, ?)`,
			alpha, from, to, activity, nanos(when))
	}
	step("queued", "provisioning", "", "2026-03-12T09:59:30-07:00")
	step("provisioning", "running", domain.ActivityWorking, "2026-03-12T10:00:00-07:00")
	step("running", "needs-attention", domain.ActivityWaitingAgents, "2026-03-12T10:10:00-07:00")
	step("needs-attention", "needs-attention", domain.ActivityNeedsYou, "2026-03-12T10:30:00-07:00")
	step("needs-attention", "running", domain.ActivityWorking, "2026-03-12T10:35:00-07:00")
	step("running", "completed", "", "2026-03-12T10:50:00-07:00")

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
		{"durations", mine.Durations, protocol.InsightsDurations{RunsMeasured: 3, MedianRunSeconds: 3600, P90RunSeconds: 37 * 3600,
			StateRunsMeasured: 2, MedianWorkingSeconds: 30 * 60, MedianWaitingSeconds: 300, MedianResponseSeconds: 300}},
		{"cost", mine.Cost, protocol.InsightsCost{MeteredRuns: 1, UnmeteredRuns: 2, CostUSD: 2, InputTokens: 1500, OutputTokens: 150, TotalTokens: 1650,
			ByAgent: []protocol.InsightsCostAgent{
				{Harness: "claude", MeteredRuns: 1, UnmeteredRuns: 1, CostUSD: 2, InputTokens: 1500, OutputTokens: 150, TotalTokens: 1650},
				{Harness: "codex", UnmeteredRuns: 1},
			}}},
		{"success", mine.Success, protocol.InsightsSuccess{Runs: protocol.InsightsSuccessCount{Succeeded: 1, Finished: 3}}},
		{"outcomes", mine.Outcomes, protocol.InsightsOutcomes{
			Runs: protocol.InsightsOutcome{Done: 1, Failed: 1, Abandoned: 1},
			Series: []protocol.InsightsOutcomeBucket{
				{Start: "2026-03-07", Runs: protocol.InsightsOutcome{Done: 1}},
				{Start: "2026-03-08", Runs: protocol.InsightsOutcome{Abandoned: 1}},
				{Start: "2026-03-09", Runs: protocol.InsightsOutcome{Failed: 1}},
			},
			Members: []protocol.InsightsMemberOutcome{},
		}},
		{"pull requests", mine.PullRequests, protocol.InsightsPullRequests{Opened: 1, Merged: 1}},
		{"recent order", recentIDs(mine.Recent), []string{"top2", "top4", "top1"}},
		{"untitled recent top2", mine.Recent[0].Title, "fix the parser"},
		{"recent top1", mine.Recent[2], protocol.InsightsRecentRun{RunID: "top1", Title: "title top1", WorkspaceID: string(alpha),
			Status: "completed", Harness: "claude", CreatedAt: "2026-03-07T18:00:00Z", FinishedAt: "2026-03-07T19:01:00Z",
			DurationSeconds: 3600, PRURL: "https://github.com/acme/app/pull/top1", PRState: "merged"}},
		{"needs you", mine.NeedsYou, 1},
		{"coverage", mine.Coverage, protocol.InsightsCoverage{StateTimesSince: "2026-03-07T18:00:30Z", PullRequestsSince: "2026-03-07T19:00:00Z"}},

		{"everyone totals", everyone.Totals, protocol.InsightsTotals{Launched: 5, Completed: 2, Failed: 1, Abandoned: 1, Killed: 1, Active: 2, Finished: 2}},
		{"everyone cost", everyone.Cost, protocol.InsightsCost{MeteredRuns: 2, UnmeteredRuns: 2, CostUSD: 4, InputTokens: 1510, OutputTokens: 151, TotalTokens: 1661,
			ByAgent: []protocol.InsightsCostAgent{
				{Harness: "claude", MeteredRuns: 2, UnmeteredRuns: 1, CostUSD: 4, InputTokens: 1510, OutputTokens: 151, TotalTokens: 1661},
				{Harness: "codex", UnmeteredRuns: 1},
			}}},
		{"everyone success", everyone.Success, protocol.InsightsSuccess{Runs: protocol.InsightsSuccessCount{Succeeded: 1, Finished: 4}}},
		{"everyone outcomes", everyone.Outcomes, protocol.InsightsOutcomes{
			Runs: protocol.InsightsOutcome{Done: 2, Failed: 1, Abandoned: 1},
			Series: []protocol.InsightsOutcomeBucket{
				{Start: "2026-03-07", Runs: protocol.InsightsOutcome{Done: 1}},
				{Start: "2026-03-08", Runs: protocol.InsightsOutcome{Done: 1, Abandoned: 1}},
				{Start: "2026-03-09", Runs: protocol.InsightsOutcome{Failed: 1}},
			},
			Members: []protocol.InsightsMemberOutcome{
				{MemberID: string(alice), DisplayName: "alice", Color: "#e6194b", Runs: protocol.InsightsOutcome{Done: 1, Failed: 1, Abandoned: 1},
					Series: []protocol.InsightsOutcomeBucket{
						{Start: "2026-03-07", Runs: protocol.InsightsOutcome{Done: 1}},
						{Start: "2026-03-08", Runs: protocol.InsightsOutcome{Abandoned: 1}},
						{Start: "2026-03-09", Runs: protocol.InsightsOutcome{Failed: 1}},
					}},
				{MemberID: string(bob), DisplayName: "bob", Color: "#3cb44b", Runs: protocol.InsightsOutcome{Done: 1},
					Series: []protocol.InsightsOutcomeBucket{
						{Start: "2026-03-07"},
						{Start: "2026-03-08", Runs: protocol.InsightsOutcome{Done: 1}},
						{Start: "2026-03-09"},
					}},
				{MemberID: string(carol), DisplayName: "carol", Color: "#4363d8", Series: []protocol.InsightsOutcomeBucket{
					{Start: "2026-03-07"}, {Start: "2026-03-08"}, {Start: "2026-03-09"},
				}},
			},
		}},
		{"everyone needs you", everyone.NeedsYou, 2},
		{"everyone recent", recentIDs(everyone.Recent), []string{"top2", "top5", "top4", "top1"}},

		{"bob totals", bobs.Totals, protocol.InsightsTotals{Launched: 2, Completed: 1, Active: 1, Finished: 1}},
		{"bob recent", recentIDs(bobs.Recent), []string{"top5"}},
		{"bob pull requests", bobs.PullRequests, protocol.InsightsPullRequests{}},
		{"bob needs you", bobs.NeedsYou, 1},

		{"workspace filter", betaOnly.Totals, protocol.InsightsTotals{Launched: 1, Active: 1}},
	}
	for _, c := range checks {
		if !reflect.DeepEqual(c.got, c.want) {
			t.Errorf("%s = %+v, want %+v", c.name, c.got, c.want)
		}
	}

	carols, perr := get(carol, with(func(p *protocol.InsightsParams) {
		p.From, p.To = "2026-03-12T00:00:00-07:00", "2026-03-13T00:00:00-07:00"
	}))
	if want := (protocol.InsightsDurations{RunsMeasured: 1, MedianRunSeconds: 50 * 60, P90RunSeconds: 50 * 60,
		StateRunsMeasured: 1, MedianWorkingSeconds: 45 * 60, MedianWaitingSeconds: 300, MedianResponseSeconds: 300}); perr != nil || carols.Durations != want {
		t.Errorf("durations by activity = %+v, %+v; want %+v", carols.Durations, perr, want)
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

	// top2 opened a PR, closed it, and it was reopened and merged after its
	// container was gone; top3,
	// publishing the same branch, recorded the same PR, and a stale open
	// observation of top1's must not undo its merge.
	// top2 ran on bob's shared agent login in alice's home, so gh there used
	// alice's GitHub login.
	exec(`UPDATE runs SET account_member_id = ? WHERE id = 'top2'`, bob)
	if _, err := db.ShareAccount(ctx, bob, alice); err != nil {
		t.Fatal(err)
	}
	pr("top2", 4, "open", "2026-03-09T00:00:00-07:00", nil, true)
	pr("top2", 4, "closed", "2026-03-09T00:20:00-07:00", nil, false)
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
		t.Errorf("run.pr.status of a finished session = %+v, %+v, lookups %v; want the merge observed with the session home's GitHub login", observed, perr, gh.lookups)
	}
	again, perr := status("top2")
	if perr != nil || again.PullRequest.State != "merged" || !reflect.DeepEqual(gh.lookups, lookup) {
		t.Errorf("run.pr.status of a merged PR = %+v, %+v, lookups %v; want the recorded state without asking GitHub", again, perr, gh.lookups)
	}
	// Its checks and reviews come from GitHub, read with the same login.
	out, perr := e.srv.dispatch(ctx, alice, protocol.MethodRunPRFeedback, repoParams(t, protocol.RunPRFeedbackParams{RunID: "top2", Limit: 30}))
	var feedback protocol.RunPRFeedbackResult
	if perr == nil {
		if err := json.Unmarshal(out, &feedback); err != nil {
			t.Fatal(err)
		}
	}
	wantChecks := []protocol.RunPRCheck{{Name: "test", Status: "COMPLETED", Conclusion: "SUCCESS"}, {Name: "lint", Status: "IN_PROGRESS"},
		{Name: "ci/legacy", Status: "COMPLETED", Conclusion: "FAILURE"}}
	if perr != nil || feedback.Error != "" || !feedback.Truncated || !feedback.CommentsUnavailable || feedback.PullRequest == nil ||
		feedback.PullRequest.HeadOID != gh.snap.HeadSHA || feedback.PullRequest.BaseBranch != "main" || feedback.PullRequest.Title != "Fix login" ||
		feedback.PullRequest.State != "merged" || feedback.PullRequest.MergedAt == nil || !feedback.PullRequest.Draft || feedback.Identity != "octocat" || !reflect.DeepEqual(feedback.Checks, wantChecks) ||
		len(feedback.Reviews) != 1 || feedback.Reviews[0].Author != "grace" || feedback.Reviews[0].State != "CHANGES_REQUESTED" ||
		gh.lookups[len(gh.lookups)-1] != "snapshot "+string(alice)+" acme/app#4 page 2" {
		t.Errorf("run.pr.feedback of a finished session = %+v, %+v, lookups %v; want GitHub's checks and reviews, truncated by the clipped review, comments marked unavailable", feedback, perr, gh.lookups)
	}
	// Bob revoking his share while the read is between pages stops it,
	// with the access error and nothing read after.
	gh.betweenPages = func() {
		if _, err := db.RevokeAccountShare(ctx, bob, alice); err != nil {
			t.Error(err)
		}
	}
	before := len(gh.lookups)
	if out, perr = e.srv.dispatch(ctx, alice, protocol.MethodRunPRFeedback, repoParams(t, protocol.RunPRFeedbackParams{RunID: "top2", Limit: 30})); perr == nil ||
		perr.Code != protocol.CodeDenied || len(gh.lookups) != before+1 {
		t.Errorf("run.pr.feedback revoked between pages = %s, %+v after lookups %v; want denied after one page", out, perr, gh.lookups[before:])
	}
	gh.betweenPages = nil
	if _, err := db.ShareAccount(ctx, bob, alice); err != nil {
		t.Fatal(err)
	}
	// A failed reviews read keeps the checks read before it.
	read := gh.snap
	gh.snap.Reviews, gh.err = nil, &githubservice.ReviewsError{Err: errors.New("github: API request: HTTP 502 Bad Gateway")}
	feedback = protocol.RunPRFeedbackResult{}
	if out, perr = e.srv.dispatch(ctx, alice, protocol.MethodRunPRFeedback, repoParams(t, protocol.RunPRFeedbackParams{RunID: "top2", Limit: 30})); perr == nil {
		if err := json.Unmarshal(out, &feedback); err != nil {
			t.Fatal(err)
		}
	}
	if perr != nil || !feedback.ReviewsUnavailable || !reflect.DeepEqual(feedback.Checks, wantChecks) || len(feedback.Reviews) != 0 ||
		feedback.Error != "read reviews from GitHub: github: API request: HTTP 502 Bad Gateway" || !feedback.Blockers().Partial {
		t.Errorf("run.pr.feedback whose reviews read failed = %+v, %+v; want the checks, reviews marked unavailable with the error", feedback, perr)
	}
	gh.snap, gh.err = read, nil
	lookup = slices.Clone(gh.lookups)
	if _, perr = status("top6"); perr == nil || perr.Code != protocol.CodeInvalidState {
		t.Errorf("run.pr.status without a live environment or a recorded PR: error = %+v, want invalid state", perr)
	}
	paused, perr := status("top3")
	if perr == nil || perr.Code != protocol.CodeInvalidState || perr.Message != scheduler.ErrRunPaused.Error() || !reflect.DeepEqual(gh.lookups, lookup) {
		t.Errorf("run.pr.status of a paused session with a recorded PR = %+v, %+v, lookups %v; want the paused error without asking GitHub", paused, perr, gh.lookups)
	}
	merged, perr := get(alice, week)
	if perr != nil || merged.PullRequests != (protocol.InsightsPullRequests{Opened: 2, Merged: 2}) {
		t.Errorf("pull requests after the merge was observed = %+v, %+v", merged.PullRequests, perr)
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
		t.Errorf("rows left after deleting the session = %d, %v; want 0", left, err)
	}
}

type goneRunRepo struct{}

func (goneRunRepo) Native() *runrepo.Service { return runrepo.New(nil) }
func (goneRunRepo) Execution(_ context.Context, run domain.Run) (runrepo.Execution, error) {
	if !run.Status.Terminal() {
		return runrepo.Execution{}, scheduler.ErrRunPaused
	}
	return runrepo.Execution{}, scheduler.ErrNoLiveEnvironment
}

func recentIDs(runs []protocol.InsightsRecentRun) []string {
	ids := make([]string, len(runs))
	for i, r := range runs {
		ids[i] = r.RunID
	}
	return ids
}
