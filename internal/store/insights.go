package store

import (
	"cmp"
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
	// run.pr.create, with the run's workspace. Recording the same
	// PR again updates its observed state.
	RecordRunPullRequest(ctx context.Context, pr *RunPullRequest) error
	// ObserveRunPullRequest updates the state of a recorded pull request.
	// A PR the run did not open through run.pr.create is not recorded.
	ObserveRunPullRequest(ctx context.Context, pr *RunPullRequest) error
	// RunPullRequests lists the pull requests recorded for run, newest
	// first.
	RunPullRequests(ctx context.Context, run domain.RunID) ([]*RunPullRequest, error)
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
	Success           InsightsSuccess
	Members           []*InsightsMemberOutcomes
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
	// SwarmTokens are the tokens of finished swarm worker and integrator
	// runs, kept out of the fields above.
	SwarmTokens int64
	ByAgent     []*InsightsCostAgent
}

// InsightsCostAgent is InsightsCost for the runs of one harness.
type InsightsCostAgent struct {
	Harness                    string
	MeteredRuns, UnmeteredRuns int
	CostUSD                    float64
	InputTokens, OutputTokens  int64
	SwarmTokens                int64
}

// InsightsOutcome is how runs or swarms ended: Done is completed or merged
// runs and completed swarms, Failed failed runs, and Abandoned abandoned and
// interrupted runs and cancelled swarms.
type InsightsOutcome struct {
	Done, Failed, Abandoned int
}

// InsightsMemberOutcomes is one member's outcomes per query bucket. Members
// are listed whether or not they did anything.
type InsightsMemberOutcomes struct {
	ID          domain.MemberID
	Name, Color string
	Runs        []InsightsOutcome
	Swarms      []InsightsOutcome
}

// InsightsSuccess counts finished top-level runs and finished swarms, and
// those among them with a merged pull request.
type InsightsSuccess struct {
	RunsSucceeded, RunsFinished, SwarmsSucceeded, SwarmsFinished int
}

type InsightsPullRequests struct {
	Opened, Merged int
}

type InsightsRun struct {
	ID          domain.RunID
	Title       string
	Task        string
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

const (
	insightsTerminal  = `('completed', 'merged', 'abandoned', 'failed', 'interrupted')`
	insightsLaunched  = `r.created_at >= :from AND r.created_at < :to`
	insightsFinished  = `r.finished_at >= :from AND r.finished_at < :to`
	insightsInRange   = `((` + insightsLaunched + `) OR (` + insightsFinished + `))`
	insightsTallyCols = `COUNT(*) FILTER (WHERE ` + insightsLaunched + `),
		COUNT(*) FILTER (WHERE ` + insightsFinished + ` AND r.status IN ('completed', 'merged')),
		COUNT(*) FILTER (WHERE ` + insightsFinished + ` AND r.status = 'failed')`
	insightsFinishedIn = insightsFinished + ` AND r.status IN ` + insightsTerminal

	// insightsSwarmRuns pairs each swarm with every run it started.
	insightsSwarmRuns = `SELECT mission_id, run_id FROM mission_attempts WHERE run_id IS NOT NULL AND run_id <> ''
		UNION SELECT id, current_integrator_run_id FROM missions
			WHERE current_integrator_run_id IS NOT NULL AND current_integrator_run_id <> ''
		UNION SELECT mission_id, initial_run_id FROM mission_create_receipts WHERE initial_run_id <> ''
		UNION SELECT mission_id, run_id FROM mission_integrator_replacements WHERE run_id <> ''`
)

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

func (q InsightsQuery) runScope() string {
	return "r.swarm_run = 0 AND " + q.ownerScope()
}

func (d *DB) RecordRunPullRequest(ctx context.Context, pr *RunPullRequest) error {
	at, merged, err := encodePullRequestTimes(pr)
	if err != nil {
		return fmt.Errorf("store: record run pull request: %w", err)
	}
	res, err := d.db.ExecContext(ctx,
		`INSERT INTO run_pull_requests (run_id, workspace_id, repo, number, url, state, created_at, merged_at, checked_at)
		 SELECT id, workspace_id, ?, ?, ?, ?, ?, ?, ? FROM runs WHERE id = ?
		 ON CONFLICT (run_id, repo, number) DO UPDATE SET
		     url = excluded.url, state = CASE WHEN state = 'merged' THEN state ELSE excluded.state END,
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
		`UPDATE run_pull_requests SET url = ?, state = CASE WHEN state = 'merged' THEN state ELSE ? END, merged_at = COALESCE(?, merged_at), checked_at = ?
		 WHERE run_id = ? AND repo = ? AND number = ?`,
		pr.URL, pr.State, merged, at, pr.RunID, pr.Repository, pr.Number); err != nil {
		return fmt.Errorf("store: observe run pull request: %w", err)
	}
	return nil
}

func (d *DB) RunPullRequests(ctx context.Context, run domain.RunID) ([]*RunPullRequest, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT repo, number, url, state, merged_at, checked_at FROM run_pull_requests
		 WHERE run_id = ? ORDER BY created_at DESC, rowid DESC`, run)
	if err != nil {
		return nil, fmt.Errorf("store: run pull requests: %w", err)
	}
	prs, err := collect(rows, func(row interface{ Scan(...any) error }) (*RunPullRequest, error) {
		var (
			pr      = RunPullRequest{RunID: run}
			merged  *int64
			checked int64
		)
		if scanErr := row.Scan(&pr.Repository, &pr.Number, &pr.URL, &pr.State, &merged, &checked); scanErr != nil {
			return nil, scanErr
		}
		if merged != nil {
			at := decodeTime(*merged)
			pr.MergedAt = &at
		}
		pr.ObservedAt = decodeTime(checked)
		return &pr, nil
	})
	if err != nil {
		return nil, fmt.Errorf("store: run pull requests: %w", err)
	}
	return prs, nil
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
		insightsStateTimes, insightsRecent, insightsSwarms, insightsPullRequests, insightsSuccess,
		insightsMemberOutcomes, insightsLive,
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
	rows, err := tx.QueryContext(ctx, `WITH
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
	rows, err := tx.QueryContext(ctx, `WITH
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
	rows, err := tx.QueryContext(ctx, `
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
	rows, err := tx.QueryContext(ctx, `
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

// insightsFinishedRuns reads each finished run's duration and metered cost,
// in total and per harness. A run is metered when any of its segments is;
// only metered segments add cost and tokens.
func insightsFinishedRuns(ctx context.Context, tx *sql.Tx, q InsightsQuery, args []any, out *Insights) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT r.harness, r.finished_at - COALESCE(r.started_at, r.created_at),
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
	var (
		durations []time.Duration
		agents    = map[string]*InsightsCostAgent{}
	)
	for rows.Next() {
		var (
			harness        string
			duration       int64
			metered        bool
			cost           float64
			input, outputs int64
		)
		if err := rows.Scan(&harness, &duration, &metered, &cost, &input, &outputs); err != nil {
			return fmt.Errorf("store: insights: finished runs: %w", err)
		}
		durations = append(durations, time.Duration(duration))
		agent := agents[harness]
		if agent == nil {
			agent = &InsightsCostAgent{Harness: harness}
			agents[harness] = agent
		}
		if !metered {
			out.Cost.UnmeteredRuns++
			agent.UnmeteredRuns++
			continue
		}
		out.Cost.MeteredRuns++
		out.Cost.CostUSD += cost
		out.Cost.InputTokens += input
		out.Cost.OutputTokens += outputs
		agent.MeteredRuns++
		agent.CostUSD += cost
		agent.InputTokens += input
		agent.OutputTokens += outputs
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("store: insights: finished runs: %w", err)
	}
	swarm, swarmErr := tx.QueryContext(ctx, `
		SELECT r.harness, COALESCE(SUM(c.input_tokens + c.output_tokens), 0)
		FROM runs r JOIN run_costs c ON c.run_id = r.id AND c.metered = 1
		WHERE r.swarm_run = 1 AND `+q.ownerScope()+` AND `+insightsFinishedIn+`
		GROUP BY r.harness`, args...)
	if swarmErr != nil {
		return fmt.Errorf("store: insights: swarm tokens: %w", swarmErr)
	}
	defer swarm.Close() //nolint:errcheck // read-only iteration
	for swarm.Next() {
		var (
			harness string
			tokens  int64
		)
		if err := swarm.Scan(&harness, &tokens); err != nil {
			return fmt.Errorf("store: insights: swarm tokens: %w", err)
		}
		agent := agents[harness]
		if agent == nil {
			agent = &InsightsCostAgent{Harness: harness}
			agents[harness] = agent
		}
		agent.SwarmTokens = tokens
		out.Cost.SwarmTokens += tokens
	}
	if err := swarm.Err(); err != nil {
		return fmt.Errorf("store: insights: swarm tokens: %w", err)
	}
	for _, agent := range agents {
		out.Cost.ByAgent = append(out.Cost.ByAgent, agent)
	}
	slices.SortFunc(out.Cost.ByAgent, func(a, b *InsightsCostAgent) int {
		return cmp.Or(cmp.Compare(b.InputTokens+b.OutputTokens+b.SwarmTokens, a.InputTokens+a.OutputTokens+a.SwarmTokens), cmp.Compare(a.Harness, b.Harness))
	})
	out.Durations.RunsMeasured = len(durations)
	out.Durations.MedianRun = percentile(durations, 0.5)
	out.Durations.P90Run = percentile(durations, 0.9)
	return nil
}

// insightsStretch is what the time after a transition counts as.
type insightsStretch int

const (
	stretchNone insightsStretch = iota
	stretchWorking
	stretchWaiting
)

// insightsStateTimes measures each finished run's last session that spent
// time working. A session runs from the run leaving queued, or from a
// relaunch out of a terminal status, to its next terminal status, so a
// relaunch rolled back before it ran leaves the session before it measured.
// A transition out of a status other than the one the session was in, such
// as a rolled-back relaunch's, drops the session. A run whose record starts
// mid-session is not measured. Working and waiting are per-run totals of
// their stretches; a response is each run of waiting stretches that ended
// in a working one.
func insightsStateTimes(ctx context.Context, tx *sql.Tx, q InsightsQuery, args []any, out *Insights) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT t.run_id, t.from_status, t.to_status, t.activity, t.at
		FROM runs r JOIN run_state_transitions t ON t.run_id = r.id
		WHERE `+q.runScope()+` AND `+insightsFinishedIn+`
		ORDER BY t.run_id, t.at, t.rowid`, args...)
	if err != nil {
		return fmt.Errorf("store: insights: state times: %w", err)
	}
	defer rows.Close() //nolint:errcheck // read-only iteration
	type session struct {
		working, waiting time.Duration
		responses        []time.Duration
		ran              bool
	}
	var (
		working, waiting, responses []time.Duration
		run                         domain.RunID
		current, last               *session
		prevTo                      domain.RunStatus
		prevStretch                 insightsStretch
		prevAt, waitingSince        int64
	)
	flush := func() {
		if last != nil {
			working = append(working, last.working)
			waiting = append(waiting, last.waiting)
			responses = append(responses, last.responses...)
		}
	}
	for rows.Next() {
		var (
			id       domain.RunID
			from, to domain.RunStatus
			activity domain.RunActivity
			at       int64
		)
		if err := rows.Scan(&id, &from, &to, &activity, &at); err != nil {
			return fmt.Errorf("store: insights: state times: %w", err)
		}
		if id != run {
			flush()
			run, current, last = id, nil, nil
		} else if current != nil && from != prevTo {
			current = nil
		} else if current != nil {
			switch prevStretch {
			case stretchWorking:
				current.working += time.Duration(at - prevAt)
				current.ran = true
			case stretchWaiting:
				current.waiting += time.Duration(at - prevAt)
			}
		}
		if from == domain.RunQueued || from.Terminal() && !to.Terminal() {
			current, waitingSince = &session{}, -1
		}
		if current == nil {
			continue
		}
		stretch := stretchNone
		switch activity {
		case "":
			switch to {
			case domain.RunRunning:
				stretch = stretchWorking
			case domain.RunNeedsAttention:
				stretch = stretchWaiting
			}
		case domain.ActivityWorking, domain.ActivityWaitingAgents, domain.ActivityWaitingCI, domain.ActivityWaitingTools:
			stretch = stretchWorking
		case domain.ActivityNeedsYou:
			stretch = stretchWaiting
		}
		switch {
		case stretch == stretchWaiting:
			if waitingSince < 0 {
				waitingSince = at
			}
		case stretch == stretchWorking && waitingSince >= 0:
			current.responses = append(current.responses, time.Duration(at-waitingSince))
			waitingSince = -1
		default:
			waitingSince = -1
		}
		prevTo, prevStretch, prevAt = to, stretch, at
		if to.Terminal() {
			if current.ran {
				last = current
			}
			current = nil
		}
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
	rows, err := tx.QueryContext(ctx, `
		SELECT r.id, r.title, r.task, r.workspace_id, r.status, r.reason, r.harness, r.model,
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
		if scanErr := row.Scan(&r.ID, &r.Title, &r.Task, &r.WorkspaceID, &r.Status, &r.Reason, &r.Harness, &r.Model,
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
// accountable human. Agents are the distinct runs they started; a reserved
// run ID whose launch never created the run is not one.
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
		agents AS (SELECT run_id FROM (`+insightsSwarmRuns+`) WHERE mission_id IN (SELECT id FROM m))
		SELECT (SELECT COUNT(*) FROM m),
			(SELECT COUNT(*) FROM m WHERE phase = 'completed'),
			(SELECT COUNT(*) FROM m WHERE phase = 'cancelled'),
			(SELECT COUNT(*) FROM agents WHERE run_id IN (SELECT id FROM runs)),
			(SELECT COUNT(*) FROM mission_acceptances WHERE mission_id IN (SELECT id FROM m))`, args...,
	).Scan(&out.Swarms.Launched, &out.Swarms.Completed, &out.Swarms.Cancelled, &out.Swarms.Agents, &out.Swarms.TasksAccepted)
	if err != nil {
		return fmt.Errorf("store: insights: swarms: %w", err)
	}
	return nil
}

// insightsPullRequests counts recorded PRs of every run in scope, swarm
// runs included: a PR is one outcome however many agents or runs recorded
// it.
func insightsPullRequests(ctx context.Context, tx *sql.Tx, q InsightsQuery, args []any, out *Insights) error {
	err := tx.QueryRowContext(ctx, `
		SELECT COUNT(DISTINCT p.repo || '#' || p.number) FILTER (WHERE p.created_at >= :from AND p.created_at < :to),
			COUNT(DISTINCT p.repo || '#' || p.number) FILTER (WHERE p.state = 'merged' AND p.merged_at >= :from AND p.merged_at < :to)
		FROM run_pull_requests p JOIN runs r ON r.id = p.run_id
		WHERE `+q.ownerScope(), args...,
	).Scan(&out.PullRequests.Opened, &out.PullRequests.Merged)
	if err != nil {
		return fmt.Errorf("store: insights: pull requests: %w", err)
	}
	return nil
}

// insightsSuccess counts the runs and swarms that finished in the range and
// those with a merged pull request. A swarm finishes when it is completed or
// cancelled, at the time it did.
func insightsSuccess(ctx context.Context, tx *sql.Tx, q InsightsQuery, args []any, out *Insights) error {
	err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*), COUNT(*) FILTER (WHERE r.status = 'merged' OR EXISTS (
			SELECT 1 FROM run_pull_requests p WHERE p.run_id = r.id AND p.state = 'merged'))
		FROM runs r WHERE `+q.runScope()+` AND `+insightsFinishedIn, args...,
	).Scan(&out.Success.RunsFinished, &out.Success.RunsSucceeded)
	if err != nil {
		return fmt.Errorf("store: insights: run success: %w", err)
	}
	err = tx.QueryRowContext(ctx, `WITH
		m AS MATERIALIZED (SELECT id FROM missions WHERE `+q.swarmFinished()+`)
		SELECT (SELECT COUNT(*) FROM m),
			(SELECT COUNT(*) FROM m WHERE EXISTS (
				SELECT 1 FROM (`+insightsSwarmRuns+`) sr JOIN run_pull_requests p ON p.run_id = sr.run_id
				WHERE sr.mission_id = m.id AND p.state = 'merged'))`, args...,
	).Scan(&out.Success.SwarmsFinished, &out.Success.SwarmsSucceeded)
	if err != nil {
		return fmt.Errorf("store: insights: swarm success: %w", err)
	}
	return nil
}

// swarmFinished selects swarms that finished in [from, to).
func (q InsightsQuery) swarmFinished() string {
	where := "phase IN ('completed', 'cancelled') AND finished_at >= :from AND finished_at < :to"
	if q.Member != "" {
		where += " AND accountable_human_id = :member"
	}
	if q.Workspace != "" {
		where += " AND workspace_id = :workspace"
	}
	return where
}

// insightsMemberOutcomes buckets how each member's runs and swarms ended. A
// scoped query lists only that member.
func insightsMemberOutcomes(ctx context.Context, tx *sql.Tx, q InsightsQuery, args []any, out *Insights) error {
	members := "SELECT id, display_name, color FROM members"
	if q.Member != "" {
		members += " WHERE id = :member"
	}
	rows, err := tx.QueryContext(ctx, members+" ORDER BY created_at, id", args...)
	if err != nil {
		return fmt.Errorf("store: insights: members: %w", err)
	}
	out.Members, err = collect(rows, func(row interface{ Scan(...any) error }) (*InsightsMemberOutcomes, error) {
		m := &InsightsMemberOutcomes{Runs: make([]InsightsOutcome, len(q.Buckets)), Swarms: make([]InsightsOutcome, len(q.Buckets))}
		return m, row.Scan(&m.ID, &m.Name, &m.Color)
	})
	if err != nil {
		return fmt.Errorf("store: insights: members: %w", err)
	}
	byID := make(map[domain.MemberID]*InsightsMemberOutcomes, len(out.Members))
	for _, m := range out.Members {
		byID[m.ID] = m
	}
	const buckets = `b(i, lo, hi) AS (SELECT key - 2, json_extract(value, '$[0]'), json_extract(value, '$[1]')
		FROM json_each(:buckets) WHERE key >= 2)`
	for _, outcomes := range []struct {
		name, query string
		pick        func(*InsightsMemberOutcomes) []InsightsOutcome
	}{
		{"runs", `WITH ` + buckets + `
			SELECT r.member_id, b.i,
				COUNT(*) FILTER (WHERE r.status IN ('completed', 'merged')),
				COUNT(*) FILTER (WHERE r.status = 'failed'),
				COUNT(*) FILTER (WHERE r.status IN ('abandoned', 'interrupted'))
			FROM runs r JOIN b ON r.finished_at >= b.lo AND r.finished_at < b.hi
			WHERE ` + q.runScope() + ` AND r.status IN ` + insightsTerminal + `
			GROUP BY r.member_id, b.i`,
			func(m *InsightsMemberOutcomes) []InsightsOutcome { return m.Runs }},
		{"swarms", `WITH ` + buckets + `
			SELECT m.accountable_human_id, b.i,
				COUNT(*) FILTER (WHERE m.phase = 'completed'), 0,
				COUNT(*) FILTER (WHERE m.phase = 'cancelled')
			FROM missions m JOIN b ON m.finished_at >= b.lo AND m.finished_at < b.hi
			WHERE ` + q.swarmFinished() + `
			GROUP BY m.accountable_human_id, b.i`,
			func(m *InsightsMemberOutcomes) []InsightsOutcome { return m.Swarms }},
	} {
		if err := scanMemberOutcomes(ctx, tx, outcomes.query, args, byID, outcomes.pick); err != nil {
			return fmt.Errorf("store: insights: member %s: %w", outcomes.name, err)
		}
	}
	return nil
}

func scanMemberOutcomes(ctx context.Context, tx *sql.Tx, query string, args []any, byID map[domain.MemberID]*InsightsMemberOutcomes, pick func(*InsightsMemberOutcomes) []InsightsOutcome) error {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close() //nolint:errcheck // read-only iteration
	for rows.Next() {
		var (
			id domain.MemberID
			i  int
			o  InsightsOutcome
		)
		if err := rows.Scan(&id, &i, &o.Done, &o.Failed, &o.Abandoned); err != nil {
			return err
		}
		pick(byID[id])[i] = o
	}
	return rows.Err()
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
