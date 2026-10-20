package store

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

func TestInsightsMigrationMarksSwarmRuns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aether.db")
	raw := openLegacy(t, path, 66)
	raw.SetMaxOpenConns(1)
	if _, err := raw.Exec(`PRAGMA foreign_keys = OFF;
		INSERT INTO members (id, display_name, public_key, color, role, created_at) VALUES ('m1', 'Ada', ?, '#e6194b', 'admin', 1);
		INSERT INTO workspaces (id, name, created_at) VALUES ('w1', 'proj', 1);
		INSERT INTO runs (id, workspace_id, member_id, task, harness, mode, status, branch, worktree, created_at)
		VALUES ('top', 'w1', 'm1', 't', 'claude', 'tui', 'completed', '', '', 1),
		       ('worker', 'w1', 'm1', 't', 'claude', 'tui', 'completed', '', '', 1),
		       ('first', 'w1', 'm1', 't', 'claude', 'tui', 'failed', '', '', 1),
		       ('second', 'w1', 'm1', 't', 'claude', 'tui', 'running', '', '', 1);
		INSERT INTO missions (id, workspace_id, objective, accountable_human_id, idempotency_key, created_at, updated_at, phase, current_integrator_run_id)
		VALUES ('mis', 'w1', 'o', 'm1', 'k', 1, 1, 'active', 'second');
		INSERT INTO mission_create_receipts (workspace_id, idempotency_key, mission_id, objective, accountable_human_id,
			integrator_account_member_id, integrator_harness, integrator_mode, execution_choices, created_at, initial_run_id)
		VALUES ('w1', 'k', 'mis', 'o', 'm1', 'm1', 'claude', 'tui', '[]', 1, 'first');
		INSERT INTO mission_attempts (id, mission_id, task_id, task_revision, number, dispatch_key, state, run_id, created_at, reserved_at)
		VALUES ('a1', 'mis', 't', 1, 1, 'd1', 'completed', 'worker', 1, 1), ('a2', 'mis', 't', 1, 2, 'd2', 'reserved', NULL, 1, 1);
		PRAGMA foreign_keys = ON;`, testKey(t, "")); err != nil {
		t.Fatalf("seed v66 rows: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = db.Close() }()
	want := map[string]bool{"top": false, "worker": true, "first": true, "second": true}
	for id, swarm := range want {
		var got bool
		if err := db.db.QueryRowContext(context.Background(), `SELECT swarm_run FROM runs WHERE id = ?`, id).Scan(&got); err != nil || got != swarm {
			t.Errorf("run %s swarm_run = %v, %v; want %v", id, got, err, swarm)
		}
	}
}

func TestSwarmFinishedMigrationBackfillsFinishedSwarms(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aether.db")
	raw := openLegacy(t, path, len(migrations)-1)
	if _, err := raw.Exec(`
		INSERT INTO members (id, display_name, public_key, color, role, created_at) VALUES ('m1', 'Ada', ?, '#e6194b', 'admin', 1);
		INSERT INTO workspaces (id, name, created_at) VALUES ('w1', 'proj', 1);
		INSERT INTO missions (id, workspace_id, objective, accountable_human_id, idempotency_key, created_at, updated_at, phase)
		VALUES ('done', 'w1', 'o', 'm1', 'k1', 1, 10, 'completed'), ('cut', 'w1', 'o', 'm1', 'k2', 1, 20, 'cancelled'),
		       ('live', 'w1', 'o', 'm1', 'k3', 1, 30, 'active');`, testKey(t, "")); err != nil {
		t.Fatalf("seed missions: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = db.Close() }()
	for id, want := range map[string]any{"done": int64(10), "cut": int64(20), "live": nil} {
		var got any
		if err := db.db.QueryRowContext(context.Background(), `SELECT finished_at FROM missions WHERE id = ?`, id).Scan(&got); err != nil || got != want {
			t.Errorf("mission %s finished_at = %v, %v; want %v", id, got, err, want)
		}
	}
}

func TestRunActivityChangeRecordsTransition(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	r := mustCreateRun(t, db, mustCreateWorkspace(t, db).ID, mustCreateMember(t, db).ID, domain.RunRunning)
	for _, activity := range []domain.RunActivity{domain.ActivityWorking, domain.ActivityWaitingTools, domain.ActivityWaitingTools} {
		if _, err := db.UpdateRunStatus(ctx, r.ID, domain.RunRunning, "", activity, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	r, err := db.GetRun(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	r.Activity = domain.ActivityNeedsYou
	if err = db.UpdateRun(ctx, r); err != nil {
		t.Fatal(err)
	}
	rows, err := db.db.QueryContext(ctx, `SELECT from_status || '>' || to_status || ' ' || activity FROM run_state_transitions WHERE run_id = ? ORDER BY rowid`, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close() //nolint:errcheck // read-only iteration
	var got []string
	for rows.Next() {
		var row string
		if err := rows.Scan(&row); err != nil {
			t.Fatal(err)
		}
		got = append(got, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := []string{"running>running working", "running>running waiting-tools", "running>running needs-you"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("transitions = %q, want %q", got, want)
	}
}

func TestInsightsOutcomes(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	la, laErr := time.LoadLocation("America/Los_Angeles")
	if laErr != nil {
		t.Fatal(laErr)
	}
	at := func(s string) time.Time {
		t.Helper()
		v, err := time.Parse(time.RFC3339Nano, s)
		if err != nil {
			t.Fatal(err)
		}
		return v.UTC()
	}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.db.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	adaMember, bobMember := mustCreateMember(t, db), mustCreateTeammate(t, db)
	ada, bob := adaMember.ID, bobMember.ID
	cyMember := &domain.Member{DisplayName: "Cy", PublicKey: testKey(t, "cy@laptop"), Color: "#3cb44b", Role: domain.RoleCollaborator}
	if err := db.CreateMember(ctx, cyMember); err != nil {
		t.Fatal(err)
	}
	workspace := func(name string) domain.WorkspaceID {
		t.Helper()
		w := &domain.Workspace{Name: name, BaseBranch: "main"}
		if err := db.CreateWorkspace(ctx, w); err != nil {
			t.Fatal(err)
		}
		return w.ID
	}
	one, two := workspace("one"), workspace("two")
	run := func(id string, owner domain.MemberID, ws domain.WorkspaceID, harness string, status domain.RunStatus, finished string) {
		t.Helper()
		r := &domain.Run{ID: domain.RunID(id), WorkspaceID: ws, MemberID: owner, Task: id, Harness: harness,
			Mode: domain.LaunchHeadless, Status: status, CreatedAt: at("2026-03-01T00:00:00Z")}
		if finished != "" {
			f := at(finished)
			r.FinishedAt = &f
		}
		if err := db.CreateRunWithID(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	// Days are 03-07, 03-08 (23 hours: clocks skip 02:00 PST to 03:00 PDT)
	// and 03-09, in Los Angeles.
	run("r1", ada, one, "claude", domain.RunCompleted, "2026-03-07T23:59:59.999999999-08:00")
	run("r2", ada, one, "claude", domain.RunFailed, "2026-03-08T01:59:59-08:00")
	run("r3", ada, one, "claude", domain.RunMerged, "2026-03-08T03:00:00-07:00")
	run("r4", ada, one, "claude", domain.RunInterrupted, "2026-03-09T00:00:00-07:00")
	run("r5", ada, one, "claude", domain.RunRunning, "")
	run("r6", ada, one, "claude", domain.RunCompleted, "2026-03-10T00:00:00-07:00")
	run("swarm-worker", ada, one, "claude", domain.RunCompleted, "2026-03-08T12:00:00-07:00")
	exec(`UPDATE runs SET swarm_run = 1 WHERE id = 'swarm-worker'`)
	run("swarm-integrator", bob, two, "codex", domain.RunCompleted, "2026-03-09T14:00:00-07:00")
	exec(`UPDATE runs SET swarm_run = 1 WHERE id = 'swarm-integrator'`)
	run("swarm-running", ada, one, "claude", domain.RunRunning, "")
	exec(`UPDATE runs SET swarm_run = 1 WHERE id = 'swarm-running'`)
	run("r7", bob, two, "claude", domain.RunAbandoned, "2026-03-09T12:00:00-07:00")
	run("r8", bob, two, "codex", domain.RunCompleted, "2026-03-09T13:00:00-07:00")

	for id, in := range map[string][2]int64{"r1": {100, 50}, "r2": {10, 5}} {
		if err := db.PutRunCost(ctx, &RunCost{RunID: domain.RunID(id), WorkspaceID: one, MemberID: ada, InputTokens: in[0], OutputTokens: in[1], Metered: true}); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.PutRunCost(ctx, &RunCost{RunID: "r8", WorkspaceID: two, MemberID: bob}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []*RunCost{
		{RunID: "swarm-worker", WorkspaceID: one, MemberID: ada, InputTokens: 7, OutputTokens: 3, Metered: true},
		{RunID: "swarm-integrator", WorkspaceID: two, MemberID: bob, InputTokens: 20, OutputTokens: 20, Metered: true},
		{RunID: "swarm-running", WorkspaceID: one, MemberID: ada, InputTokens: 1000, OutputTokens: 1000, Metered: true},
	} {
		if err := db.PutRunCost(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	mergedPR := func(runID string, number int) {
		t.Helper()
		merged := at("2026-03-08T00:00:00Z")
		pr := &RunPullRequest{RunID: domain.RunID(runID), Repository: "acme/app", Number: number, URL: "u", State: "merged", MergedAt: &merged, ObservedAt: merged}
		if err := db.RecordRunPullRequest(ctx, pr); err != nil {
			t.Fatal(err)
		}
	}
	mergedPR("r1", 1)
	mergedPR("swarm-worker", 2)

	mission := func(id string, owner domain.MemberID, ws domain.WorkspaceID, phase, updated string) {
		t.Helper()
		exec(`INSERT INTO missions (id, workspace_id, objective, accountable_human_id, idempotency_key, created_at, updated_at, phase, finished_at)
			VALUES (?, ?, 'o', ?, ?, ?, ?, ?, CASE WHEN ? IN ('completed', 'cancelled') THEN ? END)`,
			id, ws, owner, id, at("2026-03-01T00:00:00Z").UnixNano(), at(updated).UnixNano(), phase, phase, at(updated).UnixNano())
	}
	mission("m1", ada, one, "completed", "2026-03-07T12:00:00-08:00")
	mission("m2", ada, one, "cancelled", "2026-03-09T09:00:00-07:00")
	mission("m3", bob, two, "completed", "2026-03-09T10:00:00-07:00")
	mission("m4", ada, one, "active", "2026-03-08T10:00:00-07:00")
	mission("m5", ada, one, "completed", "2026-03-10T00:00:00-07:00")
	// Later updates to a finished swarm, such as replacing its integrator or
	// changing its accepted tasks, must not move it to another day.
	exec(`UPDATE missions SET updated_at = ? WHERE phase IN ('completed', 'cancelled')`, at("2026-03-11T12:00:00Z").UnixNano())
	exec(`INSERT INTO mission_create_receipts (workspace_id, idempotency_key, mission_id, objective, accountable_human_id,
		integrator_account_member_id, integrator_harness, integrator_mode, execution_choices, created_at, initial_run_id)
		VALUES (?, 'm1', 'm1', 'o', ?, ?, 'claude', 'headless', '[]', 0, 'swarm-worker')`, one, ada, ada)

	query := func(tz *time.Location, from, to string, f func(*InsightsQuery)) *Insights {
		t.Helper()
		q := InsightsQuery{From: at(from), To: at(to), Now: at("2026-03-12T00:00:00Z")}
		for day := q.From.In(tz); day.Before(q.To); day = day.AddDate(0, 0, 1) {
			next := day.AddDate(0, 0, 1)
			if next.After(q.To) {
				next = q.To
			}
			q.Buckets = append(q.Buckets, Interval{Start: day, End: next})
		}
		q.Previous = Interval{Start: q.From.Add(-q.To.Sub(q.From)), End: q.From}
		q.Days = []Interval{{Start: q.From, End: q.To}}
		f(&q)
		got, err := db.Insights(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	const from, to = "2026-03-07T00:00:00-08:00", "2026-03-10T00:00:00-07:00"
	sum := func(got []InsightsOutcome) InsightsOutcome {
		var s InsightsOutcome
		for _, o := range got {
			s.Done += o.Done
			s.Failed += o.Failed
			s.Abandoned += o.Abandoned
		}
		return s
	}
	type perDay struct{ runs, swarms []InsightsOutcome }
	days := func(got *Insights) (all, byMember perDay, names []string) {
		for i, c := range got.Series {
			all.runs = append(all.runs, InsightsOutcome{Done: c.Completed + c.Merged, Failed: c.Failed, Abandoned: c.Abandoned + c.Interrupted})
			byMember.runs = append(byMember.runs, InsightsOutcome{})
			byMember.swarms = append(byMember.swarms, InsightsOutcome{})
			for _, m := range got.Members {
				for _, pair := range []struct {
					sum *InsightsOutcome
					o   InsightsOutcome
				}{{&byMember.runs[i], m.Runs[i]}, {&byMember.swarms[i], m.Swarms[i]}} {
					pair.sum.Done += pair.o.Done
					pair.sum.Failed += pair.o.Failed
					pair.sum.Abandoned += pair.o.Abandoned
				}
			}
		}
		for _, m := range got.Members {
			names = append(names, m.Name)
		}
		return all, byMember, names
	}
	o := func(done, failed, abandoned int) InsightsOutcome {
		return InsightsOutcome{Done: done, Failed: failed, Abandoned: abandoned}
	}
	check := func(name string, got, want any) {
		t.Helper()
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s = %+v, want %+v", name, got, want)
		}
	}

	everyone := query(la, from, to, func(*InsightsQuery) {})
	all, byMember, names := days(everyone)
	check("everyone run days", all.runs, []InsightsOutcome{o(1, 0, 0), o(1, 1, 0), o(1, 0, 2)})
	check("stacked run days equal the totals", byMember.runs, all.runs)
	check("everyone swarm days", byMember.swarms, []InsightsOutcome{o(1, 0, 0), o(0, 0, 0), o(1, 0, 1)})
	check("members", names, []string{"Ada", "Bob", "Cy"})
	check("member with no activity", []any{sum(everyone.Members[2].Runs), sum(everyone.Members[2].Swarms), len(everyone.Members[2].Runs)}, []any{o(0, 0, 0), o(0, 0, 0), 3})
	check("ada runs", everyone.Members[0].Runs, []InsightsOutcome{o(1, 0, 0), o(1, 1, 0), o(0, 0, 1)})
	check("ada swarms", everyone.Members[0].Swarms, []InsightsOutcome{o(1, 0, 0), o(0, 0, 0), o(0, 0, 1)})
	check("bob runs", everyone.Members[1].Runs, []InsightsOutcome{o(0, 0, 0), o(0, 0, 0), o(1, 0, 1)})
	check("bob swarms", everyone.Members[1].Swarms, []InsightsOutcome{o(0, 0, 0), o(0, 0, 0), o(1, 0, 0)})
	check("everyone totals vs days", sum(all.runs), o(everyone.Totals.Completed+everyone.Totals.Merged, everyone.Totals.Failed, everyone.Totals.Abandoned+everyone.Totals.Interrupted))
	check("everyone success", everyone.Success, InsightsSuccess{RunsSucceeded: 2, RunsFinished: 6, SwarmsSucceeded: 1, SwarmsFinished: 3})
	check("everyone cost by agent", everyone.Cost.ByAgent, []*InsightsCostAgent{
		{Harness: "claude", MeteredRuns: 2, UnmeteredRuns: 3, InputTokens: 110, OutputTokens: 55, SwarmTokens: 10},
		{Harness: "codex", UnmeteredRuns: 1, SwarmTokens: 40},
	})
	check("everyone cost totals", []any{everyone.Cost.MeteredRuns, everyone.Cost.UnmeteredRuns, everyone.Cost.InputTokens, everyone.Cost.OutputTokens}, []any{2, 4, int64(110), int64(55)})
	check("everyone swarm tokens", everyone.Cost.SwarmTokens, int64(50))

	mine := query(la, from, to, func(q *InsightsQuery) { q.Member = ada })
	_, byMember, names = days(mine)
	check("mine members", names, []string{"Ada"})
	check("mine run days", mine.Members[0].Runs, []InsightsOutcome{o(1, 0, 0), o(1, 1, 0), o(0, 0, 1)})
	check("mine swarm days", byMember.swarms, []InsightsOutcome{o(1, 0, 0), o(0, 0, 0), o(0, 0, 1)})
	check("mine success", mine.Success, InsightsSuccess{RunsSucceeded: 2, RunsFinished: 4, SwarmsSucceeded: 1, SwarmsFinished: 2})
	check("mine cost by agent", mine.Cost.ByAgent, []*InsightsCostAgent{{Harness: "claude", MeteredRuns: 2, UnmeteredRuns: 2, InputTokens: 110, OutputTokens: 55, SwarmTokens: 10}})

	scoped := query(la, from, to, func(q *InsightsQuery) { q.Workspace = two })
	all, byMember, _ = days(scoped)
	check("workspace run days", all.runs, []InsightsOutcome{o(0, 0, 0), o(0, 0, 0), o(1, 0, 1)})
	check("workspace stacked run days", byMember.runs, all.runs)
	check("workspace swarm days", byMember.swarms, []InsightsOutcome{o(0, 0, 0), o(0, 0, 0), o(1, 0, 0)})
	check("workspace success", scoped.Success, InsightsSuccess{RunsFinished: 2, SwarmsFinished: 1})
	check("workspace cost by agent", scoped.Cost.ByAgent, []*InsightsCostAgent{
		{Harness: "codex", UnmeteredRuns: 1, SwarmTokens: 40}, {Harness: "claude", UnmeteredRuns: 1},
	})

	utc := query(time.UTC, "2026-03-08T00:00:00Z", "2026-03-10T00:00:00Z", func(*InsightsQuery) {})
	all, byMember, _ = days(utc)
	check("utc run days", all.runs, []InsightsOutcome{o(2, 1, 0), o(1, 0, 2)})
	check("utc stacked run days", byMember.runs, all.runs)
	check("utc swarm days", byMember.swarms, []InsightsOutcome{o(0, 0, 0), o(1, 0, 1)})
	empty := query(la, "2026-02-01T00:00:00-08:00", "2026-02-03T00:00:00-08:00", func(*InsightsQuery) {})
	all, byMember, _ = days(empty)
	check("zero-filled days", [][]InsightsOutcome{all.runs, byMember.runs, byMember.swarms}, [][]InsightsOutcome{
		{o(0, 0, 0), o(0, 0, 0)}, {o(0, 0, 0), o(0, 0, 0)}, {o(0, 0, 0), o(0, 0, 0)}})
	check("empty success", empty.Success, InsightsSuccess{})

	// 2026-11-01 has 25 hours: 01:30 happens in PDT and again in PST.
	run("f1", ada, one, "claude", domain.RunCompleted, "2026-11-01T01:30:00-07:00")
	run("f2", ada, one, "claude", domain.RunFailed, "2026-11-01T01:30:00-08:00")
	run("f3", ada, one, "claude", domain.RunCompleted, "2026-11-01T23:59:59-08:00")
	run("f4", ada, one, "claude", domain.RunCompleted, "2026-11-02T00:00:00-08:00")
	fall := query(la, "2026-10-31T00:00:00-07:00", "2026-11-03T00:00:00-08:00", func(*InsightsQuery) {})
	all, byMember, _ = days(fall)
	check("fall back days", all.runs, []InsightsOutcome{o(0, 0, 0), o(2, 1, 0), o(1, 0, 0)})
	check("fall back stacked days", byMember.runs, all.runs)
}

func TestInsightsMembersSkipMergedAccounts(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	now := time.Now().UTC()
	a := mustCreateMember(t, db)
	b := &domain.Member{DisplayName: "Ada on GitHub", TailnetLogin: "ada@", Color: "#3cb44b", Role: domain.RoleCollaborator}
	if err := db.CreateMember(ctx, b); err != nil {
		t.Fatal(err)
	}
	c := &domain.Member{DisplayName: "Cy", TailnetLogin: "cy@", Color: "#ffe119", Role: domain.RoleAdmin}
	if err := db.CreateMember(ctx, c); err != nil {
		t.Fatal(err)
	}
	w := mustCreateWorkspace(t, db)
	for _, owner := range []domain.MemberID{a.ID, b.ID} {
		finished := now
		r := &domain.Run{WorkspaceID: w.ID, MemberID: owner, Task: "t", Harness: "claude", Mode: domain.LaunchHeadless,
			Status: domain.RunCompleted, CreatedAt: now.Add(-time.Minute), FinishedAt: &finished}
		if err := db.CreateRun(ctx, r); err != nil {
			t.Fatal(err)
		}
	}

	if err := db.CreateMergeCode(ctx, a.ID, "code", now, now.Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	plan, err := db.PlanMerge(ctx, "code", b.ID, "account", now)
	if err != nil {
		t.Fatal(err)
	}
	pm, err := db.ProposeMerge(ctx, MergeProposal{CodeHash: "code", Proposer: b.ID, Keep: a.ID, Digest: plan.Digest, Policy: "account",
		Now: now, Expires: now.Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.AcceptMerge(ctx, MergeAccept{ProposalID: pm.ProposalID, Caller: a.ID, Digest: plan.Digest, Policy: "account", Now: now,
		ConfirmedByKind: "ssh", ConfirmedBy: "test"}); err != nil {
		t.Fatal(err)
	}

	day := Interval{Start: now.Add(-time.Hour).Truncate(time.Hour), End: now.Add(time.Hour)}
	q := InsightsQuery{From: day.Start, To: day.End, Now: now, Buckets: []Interval{day}, Days: []Interval{day},
		Previous: Interval{Start: day.Start.Add(-day.End.Sub(day.Start)), End: day.Start}}
	got, err := db.Insights(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	listed, err := db.ListMembers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Members) != len(listed) || len(got.Members) != 2 || got.Members[0].ID != a.ID {
		t.Fatalf("insights members = %d, ListMembers = %d; want the survivor and Cy", len(got.Members), len(listed))
	}
	var stacked int
	for _, m := range got.Members {
		stacked += m.Runs[0].Done
	}
	if total := got.Series[0].Completed; stacked != total || total != 2 {
		t.Fatalf("members stack %d finished runs, totals say %d; want both 2", stacked, total)
	}
}
