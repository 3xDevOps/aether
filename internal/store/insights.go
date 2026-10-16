package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

// InsightsStore records the measurements insights.get reads and aggregates
// them. See docs/insights.md for what each number means.
type InsightsStore interface {
	// RecordRunPullRequest stores a pull request a run opened through
	// run.pr.create, with the run's workspace and owner. Recording the same
	// PR again updates its observed state.
	RecordRunPullRequest(ctx context.Context, pr *RunPullRequest) error
	// ObserveRunPullRequest updates the state of a recorded pull request.
	// A PR the run did not open through run.pr.create is not recorded.
	ObserveRunPullRequest(ctx context.Context, pr *RunPullRequest) error
	// Insights aggregates q in one read snapshot.
	Insights(ctx context.Context, q InsightsQuery) (*Insights, error)
}

// RunPullRequest is one observation of a run's pull request. MergedAt is
// GitHub's merge time, nil while it is not merged.
type RunPullRequest struct {
	RunID      domain.RunID
	Repository string
	Number     int
	URL        string
	State      string
	MergedAt   *time.Time
	ObservedAt time.Time
}

// Interval is the half-open time range [Start, End).
type Interval struct {
	Start, End time.Time
}

// InsightsQuery selects what Insights aggregates: the range [From, To) and
// the Previous period it is compared with. An empty Member or Workspace
// selects every member or workspace. Buckets are the series intervals and
// Days the heatmap days, all computed by the caller in the member's time
// zone.
type InsightsQuery struct {
	Member    domain.MemberID
	Workspace domain.WorkspaceID
	From, To  time.Time
	Previous  Interval
	Now       time.Time
	Buckets   []Interval
	Days      []Interval
}

// Insights is the aggregate of one InsightsQuery. Series has one entry per
// query bucket and Heatmap one per query day.
type Insights struct {
	Totals            InsightsCounts
	Previous          InsightsCounts
	Series            []InsightsCounts
	Heatmap           []int
	ByWorkspace       []*InsightsWorkspace
	ByAgent           []*InsightsAgent
	Swarms            InsightsSwarms
	Durations         InsightsDurations
	Cost              InsightsCost
	PullRequests      InsightsPullRequests
	Recent            []*InsightsRun
	NeedsYou          int
	StateTimesSince   *time.Time
	PullRequestsSince *time.Time
}

// InsightsCounts counts launches by created_at and outcomes by finished_at.
// Active counts launched runs that are still queued, provisioning, running
// or waiting.
type InsightsCounts struct {
	Launched, Completed, Merged, Failed, Interrupted, Abandoned, Killed, Active int
}

type InsightsTally struct {
	Launched, Finished, Failed int
}

type InsightsWorkspace struct {
	ID   domain.WorkspaceID
	Name string
	InsightsTally
}

type InsightsAgent struct {
	Harness, Model string
	InsightsTally
}

type InsightsSwarms struct {
	Launched, Completed, Cancelled, Agents, TasksAccepted int
}

// InsightsDurations are nearest-rank percentiles, zero when nothing was
// measured.
type InsightsDurations struct {
	RunsMeasured      int
	MedianRun, P90Run time.Duration
	StateRunsMeasured int
	MedianWorking     time.Duration
	MedianWaiting     time.Duration
	MedianResponse    time.Duration
}

type InsightsCost struct {
	MeteredRuns, UnmeteredRuns int
	CostUSD                    float64
	InputTokens, OutputTokens  int64
}

type InsightsPullRequests struct {
	Opened, Merged int
}

type InsightsRun struct {
	ID          domain.RunID
	Title       string
	WorkspaceID domain.WorkspaceID
	Status      domain.RunStatus
	Reason      string
	Harness     string
	Model       string
	CreatedAt   time.Time
	StartedAt   *time.Time
	FinishedAt  time.Time
	PRURL       string
	PRState     string
}

// insightsSwarmRuns names every run a swarm started: workers and every
// integrator generation. Runs outside it are top-level.
const insightsSwarmRuns = `swarm_runs(run_id) AS (
	SELECT run_id FROM mission_attempts WHERE run_id IS NOT NULL AND run_id <> ''
	UNION SELECT current_integrator_run_id FROM missions WHERE current_integrator_run_id IS NOT NULL AND current_integrator_run_id <> ''
	UNION SELECT initial_run_id FROM mission_create_receipts WHERE initial_run_id <> ''
	UNION SELECT run_id FROM mission_integrator_replacements WHERE run_id <> ''
)`

const (
	insightsTerminal  = `('completed', 'merged', 'abandoned', 'failed', 'interrupted')`
	insightsLaunched  = `r.created_at >= :from AND r.created_at < :to`
	insightsFinished  = `r.finished_at >= :from AND r.finished_at < :to`
	insightsInRange   = `((` + insightsLaunched + `) OR (` + insightsFinished + `))`
	insightsTallyCols = `COUNT(*) FILTER (WHERE ` + insightsLaunched + `),
		COUNT(*) FILTER (WHERE ` + insightsFinished + ` AND r.status IN ('completed', 'merged')),
		COUNT(*) FILTER (WHERE ` + insightsFinished + ` AND r.status = 'failed')`
	insightsFinishedIn = insightsFinished + ` AND r.status IN ` + insightsTerminal
)

// ownerScope filters runs r by owner and workspace.
func (q InsightsQuery) ownerScope() string {
	where := "1"
	if q.Member != "" {
		where += " AND r.member_id = :member"
	}
	if q.Workspace != "" {
		where += " AND r.workspace_id = :workspace"
	}
	return where
}

// runScope filters runs r to the top-level runs in scope. A query using it
// declares insightsSwarmRuns.
func (q InsightsQuery) runScope() string {
	return "r.id NOT IN (SELECT run_id FROM swarm_runs) AND " + q.ownerScope()
}

func (d *DB) RecordRunPullRequest(ctx context.Context, pr *RunPullRequest) error {
	at, merged, err := encodePullRequestTimes(pr)
	if err != nil {
		return fmt.Errorf("store: record run pull request: %w", err)
	}
	res, err := d.db.ExecContext(ctx,
		`INSERT INTO run_pull_requests (run_id, workspace_id, member_id, repo, number, url, state, created_at, merged_at, checked_at)
		 SELECT id, workspace_id, member_id, ?, ?, ?, ?, ?, ?, ? FROM runs WHERE id = ?
		 ON CONFLICT (run_id, repo, number) DO UPDATE SET
		     url = excluded.url, state = excluded.state,
		     merged_at = COALESCE(excluded.merged_at, merged_at), checked_at = excluded.checked_at`,
		pr.Repository, pr.Number, pr.URL, pr.State, at, merged, at, pr.RunID)
	if err = notFoundOnZeroRows(res, err); err != nil {
		return fmt.Errorf("store: record run pull request: %w", err)
	}
	return nil
}

func (d *DB) ObserveRunPullRequest(ctx context.Context, pr *RunPullRequest) error {
	at, merged, err := encodePullRequestTimes(pr)
	if err != nil {
		return fmt.Errorf("store: observe run pull request: %w", err)
	}
	if _, err := d.db.ExecContext(ctx,
		`UPDATE run_pull_requests SET url = ?, state = ?, merged_at = COALESCE(?, merged_at), checked_at = ?
		 WHERE run_id = ? AND repo = ? AND number = ?`,
		pr.URL, pr.State, merged, at, pr.RunID, pr.Repository, pr.Number); err != nil {
		return fmt.Errorf("store: observe run pull request: %w", err)
	}
	return nil
}

func encodePullRequestTimes(pr *RunPullRequest) (int64, *int64, error) {
	at, err := encodeTime(pr.ObservedAt)
	if err != nil {
		return 0, nil, err
	}
	merged, err := encodeTimePtr(pr.MergedAt)
	return at, merged, err
}

func (d *DB) Insights(ctx context.Context, q InsightsQuery) (*Insights, error) {
	intervals := append([]Interval{{Start: q.From, End: q.To}, q.Previous}, q.Buckets...)
	args, err := q.args(intervals)
	if err != nil {
		return nil, fmt.Errorf("store: insights: %w", err)
	}
	tx, err := d.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("store: insights: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	out := &Insights{Series: make([]InsightsCounts, len(q.Buckets)), Heatmap: make([]int, len(q.Days))}
	counts, err := insightsCounts(ctx, tx, q, args, len(intervals))
	if err != nil {
		return nil, err
	}
	out.Totals, out.Previous = counts[0], counts[1]
	copy(out.Series, counts[2:])
	steps := []func(context.Context, *sql.Tx, InsightsQuery, []any, *Insights) error{
		insightsHeatmap, insightsByWorkspace, insightsByAgent, insightsFinishedRuns,
		insightsStateTimes, insightsRecent, insightsSwarms, insightsPullRequests, insightsLive,
	}
	for _, step := range steps {
		if err := step(ctx, tx, q, args, out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (q InsightsQuery) args(intervals []Interval) ([]any, error) {
	encode := func(list []Interval) (string, int64, int64, error) {
		pairs := make([][2]int64, len(list))
		var lo, hi int64 = math.MaxInt64, math.MinInt64
		for i, in := range list {
			start, err := encodeTime(in.Start)
			if err != nil {
				return "", 0, 0, err
			}
			end, err := encodeTime(in.End)
			if err != nil {
				return "", 0, 0, err
			}
			pairs[i] = [2]int64{start, end}
			lo, hi = min(lo, start), max(hi, end)
		}
		raw, err := json.Marshal(pairs)
		return string(raw), lo, hi, err
	}
	buckets, lo, hi, err := encode(intervals)
	if err != nil {
		return nil, err
	}
	days, dayLo, dayHi, err := encode(q.Days)
	if err != nil {
		return nil, err
	}
	from, err := encodeTime(q.From)
	if err != nil {
		return nil, err
	}
	to, err := encodeTime(q.To)
	if err != nil {
		return nil, err
	}
	now, err := encodeTime(q.Now)
	if err != nil {
		return nil, err
	}
	return []any{
		sql.Named("member", q.Member), sql.Named("workspace", q.Workspace),
		sql.Named("from", from), sql.Named("to", to), sql.Named("now", now),
		sql.Named("buckets", buckets), sql.Named("lo", lo), sql.Named("hi", hi),
		sql.Named("days", days), sql.Named("day_lo", dayLo), sql.Named("day_hi", dayHi),
	}, nil
}

func insightsCounts(ctx context.Context, tx *sql.Tx, q InsightsQuery, args []any, n int) ([]InsightsCounts, error) {
	in := func(col string) string { return "s." + col + " >= b.lo AND s." + col + " < b.hi" }
	rows, err := tx.QueryContext(ctx, `WITH `+insightsSwarmRuns+`,
		b(i, lo, hi) AS (SELECT key, json_extract(value, '$[0]'), json_extract(value, '$[1]') FROM json_each(:buckets)),
		s AS MATERIALIZED (
			SELECT r.created_at, r.finished_at, r.status, r.reason FROM runs r
			WHERE `+q.runScope()+`
			  AND ((r.created_at >= :lo AND r.created_at < :hi) OR (r.finished_at >= :lo AND r.finished_at < :hi)))
		SELECT b.i,
			COUNT(*) FILTER (WHERE `+in("created_at")+`),
			COUNT(*) FILTER (WHERE `+in("finished_at")+` AND s.status = 'completed'),
			COUNT(*) FILTER (WHERE `+in("finished_at")+` AND s.status = 'merged'),
			COUNT(*) FILTER (WHERE `+in("finished_at")+` AND s.status = 'failed'),
			COUNT(*) FILTER (WHERE `+in("finished_at")+` AND s.status = 'interrupted'),
			COUNT(*) FILTER (WHERE `+in("finished_at")+` AND s.status = 'abandoned'),
			COUNT(*) FILTER (WHERE `+in("finished_at")+` AND s.status = 'abandoned' AND s.reason = 'killed'),
			COUNT(*) FILTER (WHERE `+in("created_at")+` AND s.status IN ('queued', 'provisioning', 'running', 'needs-attention'))
		FROM b JOIN s ON (`+in("created_at")+`) OR (`+in("finished_at")+`)
		GROUP BY b.i`, args...)
	if err != nil {
		return nil, fmt.Errorf("store: insights: counts: %w", err)
	}
	defer rows.Close() //nolint:errcheck // read-only iteration
	out := make([]InsightsCounts, n)
	for rows.Next() {
		var (
			i int
			c InsightsCounts
		)
		if err := rows.Scan(&i, &c.Launched, &c.Completed, &c.Merged, &c.Failed, &c.Interrupted, &c.Abandoned, &c.Killed, &c.Active); err != nil {
			return nil, fmt.Errorf("store: insights: counts: %w", err)
		}
		out[i] = c
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: insights: counts: %w", err)
	}
	return out, nil
}

// insightsHeatmap counts, per day, the runs whose [started or created,
// finished or now] interval overlaps it.
func insightsHeatmap(ctx context.Context, tx *sql.Tx, q InsightsQuery, args []any, out *Insights) error {
	rows, err := tx.QueryContext(ctx, `WITH `+insightsSwarmRuns+`,
		d(i, lo, hi) AS (SELECT key, json_extract(value, '$[0]'), json_extract(value, '$[1]') FROM json_each(:days)),
		a AS MATERIALIZED (
			SELECT COALESCE(r.started_at, r.created_at) AS s, COALESCE(r.finished_at, :now) AS e FROM runs r
			WHERE `+q.runScope()+`
			  AND COALESCE(r.started_at, r.created_at) < :day_hi AND (r.finished_at IS NULL OR r.finished_at >= :day_lo))
		SELECT d.i, COUNT(*) FROM d JOIN a ON a.s < d.hi AND a.e >= d.lo GROUP BY d.i`, args...)
	if err != nil {
		return fmt.Errorf("store: insights: heatmap: %w", err)
	}
	defer rows.Close() //nolint:errcheck // read-only iteration
	for rows.Next() {
		var i, n int
		if err := rows.Scan(&i, &n); err != nil {
			return fmt.Errorf("store: insights: heatmap: %w", err)
		}
		out.Heatmap[i] = n
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("store: insights: heatmap: %w", err)
	}
	return nil
}

func insightsByWorkspace(ctx context.Context, tx *sql.Tx, q InsightsQuery, args []any, out *Insights) error {
	rows, err := tx.QueryContext(ctx, `WITH `+insightsSwarmRuns+`
		SELECT r.workspace_id, w.name, `+insightsTallyCols+`
		FROM runs r JOIN workspaces w ON w.id = r.workspace_id
		WHERE `+q.runScope()+` AND `+insightsInRange+`
		GROUP BY r.workspace_id ORDER BY 3 DESC, w.name, r.workspace_id`, args...)
	if err != nil {
		return fmt.Errorf("store: insights: by workspace: %w", err)
	}
	out.ByWorkspace, err = collect(rows, func(row interface{ Scan(...any) error }) (*InsightsWorkspace, error) {
		var w InsightsWorkspace
		return &w, row.Scan(&w.ID, &w.Name, &w.Launched, &w.Finished, &w.Failed)
	})
	if err != nil {
		return fmt.Errorf("store: insights: by workspace: %w", err)
	}
	return nil
}

func insightsByAgent(ctx context.Context, tx *sql.Tx, q InsightsQuery, args []any, out *Insights) error {
	rows, err := tx.QueryContext(ctx, `WITH `+insightsSwarmRuns+`
		SELECT r.harness, r.model, `+insightsTallyCols+`
		FROM runs r
		WHERE `+q.runScope()+` AND `+insightsInRange+`
		GROUP BY r.harness, r.model ORDER BY 3 DESC, r.harness, r.model`, args...)
	if err != nil {
		return fmt.Errorf("store: insights: by agent: %w", err)
	}
	out.ByAgent, err = collect(rows, func(row interface{ Scan(...any) error }) (*InsightsAgent, error) {
		var a InsightsAgent
		return &a, row.Scan(&a.Harness, &a.Model, &a.Launched, &a.Finished, &a.Failed)
	})
	if err != nil {
		return fmt.Errorf("store: insights: by agent: %w", err)
	}
	return nil
}

// insightsFinishedRuns reads each finished run's duration and metered cost.
// A run is metered when any of its segments is; only metered segments add
// cost and tokens.
func insightsFinishedRuns(ctx context.Context, tx *sql.Tx, q InsightsQuery, args []any, out *Insights) error {
	rows, err := tx.QueryContext(ctx, `WITH `+insightsSwarmRuns+`
		SELECT r.finished_at - COALESCE(r.started_at, r.created_at),
			COALESCE(MAX(c.metered), 0),
			COALESCE(SUM(c.cost_usd) FILTER (WHERE c.metered = 1), 0),
			COALESCE(SUM(c.input_tokens) FILTER (WHERE c.metered = 1), 0),
			COALESCE(SUM(c.output_tokens) FILTER (WHERE c.metered = 1), 0)
		FROM runs r LEFT JOIN run_costs c ON c.run_id = r.id
		WHERE `+q.runScope()+` AND `+insightsFinishedIn+`
		GROUP BY r.id`, args...)
	if err != nil {
		return fmt.Errorf("store: insights: finished runs: %w", err)
	}
	defer rows.Close() //nolint:errcheck // read-only iteration
	var durations []time.Duration
	for rows.Next() {
		var (
			duration       int64
			metered        bool
			cost           float64
			input, outputs int64
		)
		if err := rows.Scan(&duration, &metered, &cost, &input, &outputs); err != nil {
			return fmt.Errorf("store: insights: finished runs: %w", err)
		}
		durations = append(durations, time.Duration(duration))
		if !metered {
			out.Cost.UnmeteredRuns++
			continue
		}
		out.Cost.MeteredRuns++
		out.Cost.CostUSD += cost
		out.Cost.InputTokens += input
		out.Cost.OutputTokens += outputs
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("store: insights: finished runs: %w", err)
	}
	out.Durations.RunsMeasured = len(durations)
	out.Durations.MedianRun = percentile(durations, 0.5)
	out.Durations.P90Run = percentile(durations, 0.9)
	return nil
}

// insightsStateTimes measures the finished runs whose recorded transitions
// start at queued, so the record covers the whole run. Working and waiting
// are per-run totals up to the first terminal status; a response is each
// stretch of needs-attention that ended in running.
func insightsStateTimes(ctx context.Context, tx *sql.Tx, q InsightsQuery, args []any, out *Insights) error {
	rows, err := tx.QueryContext(ctx, `WITH `+insightsSwarmRuns+`
		SELECT t.run_id, t.from_status, t.to_status, t.at
		FROM runs r JOIN run_state_transitions t ON t.run_id = r.id
		WHERE `+q.runScope()+` AND `+insightsFinishedIn+`
		ORDER BY t.run_id, t.at, t.rowid`, args...)
	if err != nil {
		return fmt.Errorf("store: insights: state times: %w", err)
	}
	defer rows.Close() //nolint:errcheck // read-only iteration
	var (
		working, waiting, responses []time.Duration
		run                         domain.RunID
		measured, done              bool
		prevTo                      domain.RunStatus
		prevAt                      int64
		waitingSince                int64
		runWorking, runWaiting      time.Duration
	)
	flush := func() {
		if measured {
			working = append(working, runWorking)
			waiting = append(waiting, runWaiting)
		}
	}
	for rows.Next() {
		var (
			id       domain.RunID
			from, to domain.RunStatus
			at       int64
		)
		if err := rows.Scan(&id, &from, &to, &at); err != nil {
			return fmt.Errorf("store: insights: state times: %w", err)
		}
		if id != run {
			flush()
			run, measured, done = id, from == domain.RunQueued, false
			runWorking, runWaiting, waitingSince = 0, 0, -1
		} else if measured && !done {
			switch prevTo {
			case domain.RunRunning:
				runWorking += time.Duration(at - prevAt)
			case domain.RunNeedsAttention:
				runWaiting += time.Duration(at - prevAt)
			}
		}
		if !measured || done {
			continue
		}
		switch {
		case to == domain.RunNeedsAttention:
			if waitingSince < 0 {
				waitingSince = at
			}
		case to == domain.RunRunning && waitingSince >= 0:
			responses = append(responses, time.Duration(at-waitingSince))
			waitingSince = -1
		default:
			waitingSince = -1
		}
		prevTo, prevAt, done = to, at, to.Terminal()
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("store: insights: state times: %w", err)
	}
	flush()
	out.Durations.StateRunsMeasured = len(working)
	out.Durations.MedianWorking = percentile(working, 0.5)
	out.Durations.MedianWaiting = percentile(waiting, 0.5)
	out.Durations.MedianResponse = percentile(responses, 0.5)
	return nil
}

func insightsRecent(ctx context.Context, tx *sql.Tx, q InsightsQuery, args []any, out *Insights) error {
	rows, err := tx.QueryContext(ctx, `WITH `+insightsSwarmRuns+`
		SELECT r.id, r.title, r.workspace_id, r.status, r.reason, r.harness, r.model,
			r.created_at, r.started_at, r.finished_at, COALESCE(p.url, ''), COALESCE(p.state, '')
		FROM runs r
		LEFT JOIN run_pull_requests p ON p.rowid = (
			SELECT rowid FROM run_pull_requests WHERE run_id = r.id ORDER BY created_at DESC, rowid DESC LIMIT 1)
		WHERE `+q.runScope()+` AND `+insightsFinishedIn+`
		ORDER BY r.finished_at DESC, r.id DESC LIMIT 50`, args...)
	if err != nil {
		return fmt.Errorf("store: insights: recent: %w", err)
	}
	out.Recent, err = collect(rows, func(row interface{ Scan(...any) error }) (*InsightsRun, error) {
		var (
			r                 InsightsRun
			created, finished int64
			started           *int64
		)
		if scanErr := row.Scan(&r.ID, &r.Title, &r.WorkspaceID, &r.Status, &r.Reason, &r.Harness, &r.Model,
			&created, &started, &finished, &r.PRURL, &r.PRState); scanErr != nil {
			return nil, scanErr
		}
		r.CreatedAt, r.FinishedAt = decodeTime(created), decodeTime(finished)
		if started != nil {
			s := decodeTime(*started)
			r.StartedAt = &s
		}
		return &r, nil
	})
	if err != nil {
		return fmt.Errorf("store: insights: recent: %w", err)
	}
	return nil
}

// insightsSwarms reports the swarms created in range, scoped by their
// accountable human. Agents are the distinct runs they started.
func insightsSwarms(ctx context.Context, tx *sql.Tx, q InsightsQuery, args []any, out *Insights) error {
	where := "created_at >= :from AND created_at < :to"
	if q.Member != "" {
		where += " AND accountable_human_id = :member"
	}
	if q.Workspace != "" {
		where += " AND workspace_id = :workspace"
	}
	err := tx.QueryRowContext(ctx, `WITH
		m AS MATERIALIZED (SELECT id, phase FROM missions WHERE `+where+`),
		agents AS (
			SELECT run_id FROM mission_attempts WHERE mission_id IN (SELECT id FROM m) AND run_id IS NOT NULL AND run_id <> ''
			UNION SELECT current_integrator_run_id FROM missions
				WHERE id IN (SELECT id FROM m) AND current_integrator_run_id IS NOT NULL AND current_integrator_run_id <> ''
			UNION SELECT initial_run_id FROM mission_create_receipts WHERE mission_id IN (SELECT id FROM m) AND initial_run_id <> ''
			UNION SELECT run_id FROM mission_integrator_replacements WHERE mission_id IN (SELECT id FROM m) AND run_id <> '')
		SELECT (SELECT COUNT(*) FROM m),
			(SELECT COUNT(*) FROM m WHERE phase = 'completed'),
			(SELECT COUNT(*) FROM m WHERE phase = 'cancelled'),
			(SELECT COUNT(*) FROM agents),
			(SELECT COUNT(*) FROM mission_acceptances WHERE mission_id IN (SELECT id FROM m))`, args...,
	).Scan(&out.Swarms.Launched, &out.Swarms.Completed, &out.Swarms.Cancelled, &out.Swarms.Agents, &out.Swarms.TasksAccepted)
	if err != nil {
		return fmt.Errorf("store: insights: swarms: %w", err)
	}
	return nil
}

// insightsPullRequests counts recorded PRs of every run in scope, swarm
// runs included: a PR is one outcome however many agents worked on it.
func insightsPullRequests(ctx context.Context, tx *sql.Tx, q InsightsQuery, args []any, out *Insights) error {
	err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FILTER (WHERE p.created_at >= :from AND p.created_at < :to),
			COUNT(*) FILTER (WHERE p.state = 'merged' AND p.merged_at >= :from AND p.merged_at < :to)
		FROM run_pull_requests p JOIN runs r ON r.id = p.run_id
		WHERE `+q.ownerScope(), args...,
	).Scan(&out.PullRequests.Opened, &out.PullRequests.Merged)
	if err != nil {
		return fmt.Errorf("store: insights: pull requests: %w", err)
	}
	return nil
}

// insightsLive reads what ignores the range: runs waiting on a member now,
// swarm runs included as on the Board, and when this server started
// recording transitions and PRs.
func insightsLive(ctx context.Context, tx *sql.Tx, q InsightsQuery, args []any, out *Insights) error {
	var transitions, prs sql.NullInt64
	err := tx.QueryRowContext(ctx, `
		SELECT (SELECT COUNT(*) FROM runs r WHERE `+q.ownerScope()+` AND r.status = 'needs-attention'),
			(SELECT at FROM run_state_transitions ORDER BY rowid LIMIT 1),
			(SELECT created_at FROM run_pull_requests ORDER BY rowid LIMIT 1)`, args...,
	).Scan(&out.NeedsYou, &transitions, &prs)
	if err != nil {
		return fmt.Errorf("store: insights: live: %w", err)
	}
	if transitions.Valid {
		at := decodeTime(transitions.Int64)
		out.StateTimesSince = &at
	}
	if prs.Valid {
		at := decodeTime(prs.Int64)
		out.PullRequestsSince = &at
	}
	return nil
}

// percentile is the nearest-rank percentile, zero for no values.
func percentile(values []time.Duration, p float64) time.Duration {
	if len(values) == 0 {
		return 0
	}
	sorted := slices.Clone(values)
	slices.Sort(sorted)
	return sorted[int(math.Ceil(p*float64(len(sorted))))-1]
}
