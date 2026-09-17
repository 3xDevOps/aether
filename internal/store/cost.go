package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

// RunCost is one run's recorded token usage, attributed to the run's
// workspace and owning member.
//
// Metered is the honesty flag: false means nobody measured this run's
// usage (its harness has no adapter), so the token counts and CostUSD are
// zero because they are unknown, not because the run was free. Rollups
// over unmetered runs are floors.
type RunCost struct {
	RunID        domain.RunID
	WorkspaceID  domain.WorkspaceID
	MemberID     domain.MemberID
	InputTokens  int64
	OutputTokens int64
	CostUSD      float64
	Metered      bool
	RecordedAt   time.Time
}

// WorkspaceBudget is a workspace's spend cap. LimitUSD is the hard cap new
// runs are refused at; WarnUSD is the soft threshold (0 = none); Override
// is an admin's standing permission to start runs past the cap.
type WorkspaceBudget struct {
	WorkspaceID domain.WorkspaceID
	LimitUSD    float64
	WarnUSD     float64
	Override    bool
	UpdatedBy   domain.MemberID
	UpdatedAt   time.Time
}

// RunCostSummary is the workspace-wide usage aggregate used by budget
// admission and status checks. Unmetered rows contribute to the run counts
// only; their token and cost columns are intentionally ignored.
type RunCostSummary struct {
	Runs         int
	Metered      int
	Unmetered    int
	InputTokens  int64
	OutputTokens int64
	CostUSD      float64
}

// MemberCostSummary is one member's folded totals from runs deleted out of
// a workspace (see DeleteRun): a per-(workspace, member) row of
// run_cost_deletions, in the same shape SummarizeRunCosts returns so a
// caller can add it straight into a live rollup.
type MemberCostSummary struct {
	MemberID domain.MemberID
	RunCostSummary
}

// CostStore is the cost-attribution and budget persistence surface.
type CostStore interface {
	// PutRunCost records a run's usage, keyed by run. A metered record
	// replaces whatever is stored; an unmetered record never overwrites a
	// metered one, so a late adapter result cannot be downgraded and an
	// unmetered marker cannot erase real numbers.
	PutRunCost(ctx context.Context, c *RunCost) error
	GetRunCost(ctx context.Context, run domain.RunID) (*RunCost, error)
	// ListRunCosts returns a workspace's records, oldest first.
	ListRunCosts(ctx context.Context, workspace domain.WorkspaceID) ([]*RunCost, error)
	// SummarizeRunCosts returns the same workspace history as a scalar
	// aggregate using standard SQL SUM semantics, while avoiding allocation
	// of every record. It includes runs deleted from the workspace (see
	// DeleteRun), so a deletion cannot lower counted spend or reopen a
	// budget cap.
	SummarizeRunCosts(ctx context.Context, workspace domain.WorkspaceID) (RunCostSummary, error)
	// ListDeletedRunCosts returns, ordered by member ID, each member's
	// folded totals from runs deleted out of workspace. A cost report adds
	// these into its live per-member and workspace rollups so a deletion
	// cannot lower a member's counted spend either.
	ListDeletedRunCosts(ctx context.Context, workspace domain.WorkspaceID) ([]*MemberCostSummary, error)
	// SetWorkspaceBudget creates or replaces a workspace's budget.
	SetWorkspaceBudget(ctx context.Context, b *WorkspaceBudget) error
	// GetWorkspaceBudget returns ErrNotFound when the workspace has no budget.
	GetWorkspaceBudget(ctx context.Context, workspace domain.WorkspaceID) (*WorkspaceBudget, error)
	// DeleteWorkspaceBudget removes a workspace's budget; removing a budget
	// that does not exist is not an error.
	DeleteWorkspaceBudget(ctx context.Context, workspace domain.WorkspaceID) error
}

const runCostCols = `run_id, workspace_id, member_id, input_tokens, output_tokens, cost_usd, metered, recorded_at`

func (d *DB) PutRunCost(ctx context.Context, c *RunCost) error {
	if c.RunID == "" || c.WorkspaceID == "" || c.MemberID == "" {
		return errors.New("store: put run cost: run_id, workspace_id, and member_id are required")
	}
	at := c.RecordedAt
	if at.IsZero() {
		at = time.Now().UTC()
	}
	recordedAt, err := encodeTime(at)
	if err != nil {
		return fmt.Errorf("store: put run cost: %w", err)
	}
	if _, err := d.db.ExecContext(ctx,
		`INSERT INTO run_costs (`+runCostCols+`)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT (run_id) DO UPDATE SET
			workspace_id    = excluded.workspace_id,
			member_id     = excluded.member_id,
			input_tokens  = excluded.input_tokens,
			output_tokens = excluded.output_tokens,
			cost_usd      = excluded.cost_usd,
			metered       = excluded.metered,
			recorded_at   = excluded.recorded_at
		 WHERE excluded.metered = 1 OR run_costs.metered = 0`,
		c.RunID, c.WorkspaceID, c.MemberID, c.InputTokens, c.OutputTokens, c.CostUSD, c.Metered, recordedAt,
	); err != nil {
		return fmt.Errorf("store: put run cost %s: %w", c.RunID, mapConstraint(err, ErrNotFound))
	}
	c.RecordedAt = at
	return nil
}

func (d *DB) GetRunCost(ctx context.Context, run domain.RunID) (*RunCost, error) {
	c, err := scanRunCost(d.db.QueryRowContext(ctx,
		`SELECT `+runCostCols+` FROM run_costs WHERE run_id = ?`, run))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: get run cost %s: %w", run, err)
	}
	return c, nil
}

func (d *DB) ListRunCosts(ctx context.Context, workspace domain.WorkspaceID) ([]*RunCost, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT `+runCostCols+` FROM run_costs WHERE workspace_id = ? ORDER BY recorded_at, run_id`, workspace)
	if err != nil {
		return nil, fmt.Errorf("store: list run costs: %w", err)
	}
	return collect(rows, scanRunCost)
}

// SummarizeRunCosts returns the aggregate used by budget status and
// admission. Standard SQL SUM semantics determine the numeric totals while
// the query avoids allocating one object per historical row. The union
// with run_cost_deletions folds back in what deleted runs (see DeleteRun)
// contributed before their row was removed.
func (d *DB) SummarizeRunCosts(ctx context.Context, workspace domain.WorkspaceID) (RunCostSummary, error) {
	var summary RunCostSummary
	err := d.db.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(runs), 0), COALESCE(SUM(metered), 0), COALESCE(SUM(unmetered), 0),
		       COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0), COALESCE(SUM(cost_usd), 0)
		FROM (
			SELECT COUNT(*) AS runs,
			       COALESCE(SUM(CASE WHEN metered <> 0 THEN 1 ELSE 0 END), 0) AS metered,
			       COALESCE(SUM(CASE WHEN metered = 0 THEN 1 ELSE 0 END), 0) AS unmetered,
			       COALESCE(SUM(CASE WHEN metered <> 0 THEN input_tokens END), 0) AS input_tokens,
			       COALESCE(SUM(CASE WHEN metered <> 0 THEN output_tokens END), 0) AS output_tokens,
			       COALESCE(SUM(CASE WHEN metered <> 0 THEN cost_usd END), 0) AS cost_usd
			FROM run_costs WHERE workspace_id = ?
			UNION ALL
			SELECT runs, metered, unmetered, input_tokens, output_tokens, cost_usd
			FROM run_cost_deletions WHERE workspace_id = ?
		)`, workspace, workspace,
	).Scan(&summary.Runs, &summary.Metered, &summary.Unmetered,
		&summary.InputTokens, &summary.OutputTokens, &summary.CostUSD)
	if err != nil {
		return RunCostSummary{}, fmt.Errorf("store: summarize run costs: %w", err)
	}
	return summary, nil
}

// ListDeletedRunCosts returns run_cost_deletions' rows for workspace: one
// per member, already folded to the same numeric semantics SummarizeRunCosts
// uses (an unmetered run contributes to the run counts only).
func (d *DB) ListDeletedRunCosts(ctx context.Context, workspace domain.WorkspaceID) ([]*MemberCostSummary, error) {
	rows, err := d.db.QueryContext(ctx, `
		SELECT member_id, runs, metered, unmetered, input_tokens, output_tokens, cost_usd
		FROM run_cost_deletions WHERE workspace_id = ? ORDER BY member_id`, workspace)
	if err != nil {
		return nil, fmt.Errorf("store: list deleted run costs: %w", err)
	}
	return collect(rows, scanMemberCostSummary)
}

// foldDeletedRunCostSQL rolls a run's row into its workspace and member's
// accumulator before DeleteRun removes it, so a workspace's and a member's
// counted spend cannot drop, and a budget cap cannot reopen, just because
// the run itself is gone. A no-op when the run never had a cost row. The
// CASE guards mirror SummarizeRunCosts: an unmetered row's numeric columns
// are ignored, only its run and unmetered counts carry over.
const foldDeletedRunCostSQL = `
INSERT INTO run_cost_deletions (workspace_id, member_id, runs, metered, unmetered, input_tokens, output_tokens, cost_usd)
SELECT workspace_id, member_id, 1,
       CASE WHEN metered <> 0 THEN 1 ELSE 0 END,
       CASE WHEN metered = 0 THEN 1 ELSE 0 END,
       CASE WHEN metered <> 0 THEN input_tokens ELSE 0 END,
       CASE WHEN metered <> 0 THEN output_tokens ELSE 0 END,
       CASE WHEN metered <> 0 THEN cost_usd ELSE 0 END
FROM run_costs WHERE run_id = ?
ON CONFLICT (workspace_id, member_id) DO UPDATE SET
	runs          = run_cost_deletions.runs + excluded.runs,
	metered       = run_cost_deletions.metered + excluded.metered,
	unmetered     = run_cost_deletions.unmetered + excluded.unmetered,
	input_tokens  = run_cost_deletions.input_tokens + excluded.input_tokens,
	output_tokens = run_cost_deletions.output_tokens + excluded.output_tokens,
	cost_usd      = run_cost_deletions.cost_usd + excluded.cost_usd
`

func (d *DB) SetWorkspaceBudget(ctx context.Context, b *WorkspaceBudget) error {
	if b.WorkspaceID == "" {
		return errors.New("store: set workspace budget: workspace_id is required")
	}
	if b.LimitUSD <= 0 {
		return fmt.Errorf("store: set workspace budget: limit must be positive, got %v", b.LimitUSD)
	}
	if b.WarnUSD < 0 || b.WarnUSD > b.LimitUSD {
		return fmt.Errorf("store: set workspace budget: warning threshold %v must be between 0 and the limit %v", b.WarnUSD, b.LimitUSD)
	}
	at := b.UpdatedAt
	if at.IsZero() {
		at = time.Now().UTC()
	}
	updatedAt, err := encodeTime(at)
	if err != nil {
		return fmt.Errorf("store: set workspace budget: %w", err)
	}
	if _, err := d.db.ExecContext(ctx,
		`INSERT INTO workspace_budgets (workspace_id, limit_usd, warn_usd, override, updated_by, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT (workspace_id) DO UPDATE SET
			limit_usd  = excluded.limit_usd,
			warn_usd   = excluded.warn_usd,
			override   = excluded.override,
			updated_by = excluded.updated_by,
			updated_at = excluded.updated_at`,
		b.WorkspaceID, b.LimitUSD, b.WarnUSD, b.Override, b.UpdatedBy, updatedAt,
	); err != nil {
		return fmt.Errorf("store: set workspace budget %s: %w", b.WorkspaceID, mapConstraint(err, ErrNotFound))
	}
	b.UpdatedAt = at
	return nil
}

func (d *DB) GetWorkspaceBudget(ctx context.Context, workspace domain.WorkspaceID) (*WorkspaceBudget, error) {
	var (
		b         WorkspaceBudget
		updatedAt int64
	)
	err := d.db.QueryRowContext(ctx,
		`SELECT workspace_id, limit_usd, warn_usd, override, updated_by, updated_at
		 FROM workspace_budgets WHERE workspace_id = ?`, workspace,
	).Scan(&b.WorkspaceID, &b.LimitUSD, &b.WarnUSD, &b.Override, &b.UpdatedBy, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: get workspace budget %s: %w", workspace, err)
	}
	b.UpdatedAt = decodeTime(updatedAt)
	return &b, nil
}

func (d *DB) DeleteWorkspaceBudget(ctx context.Context, workspace domain.WorkspaceID) error {
	if _, err := d.db.ExecContext(ctx,
		`DELETE FROM workspace_budgets WHERE workspace_id = ?`, workspace); err != nil {
		return fmt.Errorf("store: delete workspace budget %s: %w", workspace, err)
	}
	return nil
}

func scanMemberCostSummary(row interface{ Scan(...any) error }) (*MemberCostSummary, error) {
	var m MemberCostSummary
	if err := row.Scan(&m.MemberID, &m.Runs, &m.Metered, &m.Unmetered,
		&m.InputTokens, &m.OutputTokens, &m.CostUSD); err != nil {
		return nil, err
	}
	return &m, nil
}

func scanRunCost(row interface{ Scan(...any) error }) (*RunCost, error) {
	var (
		c          RunCost
		recordedAt int64
	)
	if err := row.Scan(&c.RunID, &c.WorkspaceID, &c.MemberID, &c.InputTokens,
		&c.OutputTokens, &c.CostUSD, &c.Metered, &recordedAt); err != nil {
		return nil, err
	}
	c.RecordedAt = decodeTime(recordedAt)
	return &c, nil
}
