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
// session and owning member.
//
// Metered is the honesty flag: false means nobody measured this run's
// usage (its harness has no adapter), so the token counts and CostUSD are
// zero because they are unknown, not because the run was free. Rollups
// over unmetered runs are floors.
type RunCost struct {
	RunID        domain.RunID
	SessionID    domain.SessionID
	MemberID     domain.MemberID
	InputTokens  int64
	OutputTokens int64
	CostUSD      float64
	Metered      bool
	RecordedAt   time.Time
}

// SessionBudget is a session's spend cap. LimitUSD is the hard cap new
// runs are refused at; WarnUSD is the soft threshold (0 = none); Override
// is an admin's standing permission to start runs past the cap.
type SessionBudget struct {
	SessionID domain.SessionID
	LimitUSD  float64
	WarnUSD   float64
	Override  bool
	UpdatedBy domain.MemberID
	UpdatedAt time.Time
}

// CostStore is the cost-attribution and budget persistence surface.
type CostStore interface {
	// PutRunCost records a run's usage, keyed by run. A metered record
	// replaces whatever is stored; an unmetered record never overwrites a
	// metered one, so a late adapter result cannot be downgraded and an
	// unmetered marker cannot erase real numbers.
	PutRunCost(ctx context.Context, c *RunCost) error
	GetRunCost(ctx context.Context, run domain.RunID) (*RunCost, error)
	// ListRunCosts returns a session's records, oldest first.
	ListRunCosts(ctx context.Context, session domain.SessionID) ([]*RunCost, error)
	// SetSessionBudget creates or replaces a session's budget.
	SetSessionBudget(ctx context.Context, b *SessionBudget) error
	// GetSessionBudget returns ErrNotFound when the session has no budget.
	GetSessionBudget(ctx context.Context, session domain.SessionID) (*SessionBudget, error)
	// DeleteSessionBudget removes a session's budget; removing a budget
	// that does not exist is not an error.
	DeleteSessionBudget(ctx context.Context, session domain.SessionID) error
}

const runCostCols = `run_id, session_id, member_id, input_tokens, output_tokens, cost_usd, metered, recorded_at`

func (d *DB) PutRunCost(ctx context.Context, c *RunCost) error {
	if c.RunID == "" || c.SessionID == "" || c.MemberID == "" {
		return errors.New("store: put run cost: run_id, session_id, and member_id are required")
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
			session_id    = excluded.session_id,
			member_id     = excluded.member_id,
			input_tokens  = excluded.input_tokens,
			output_tokens = excluded.output_tokens,
			cost_usd      = excluded.cost_usd,
			metered       = excluded.metered,
			recorded_at   = excluded.recorded_at
		 WHERE excluded.metered = 1 OR run_costs.metered = 0`,
		c.RunID, c.SessionID, c.MemberID, c.InputTokens, c.OutputTokens, c.CostUSD, c.Metered, recordedAt,
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

func (d *DB) ListRunCosts(ctx context.Context, session domain.SessionID) ([]*RunCost, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT `+runCostCols+` FROM run_costs WHERE session_id = ? ORDER BY recorded_at, run_id`, session)
	if err != nil {
		return nil, fmt.Errorf("store: list run costs: %w", err)
	}
	return collect(rows, scanRunCost)
}

func (d *DB) SetSessionBudget(ctx context.Context, b *SessionBudget) error {
	if b.SessionID == "" {
		return errors.New("store: set session budget: session_id is required")
	}
	if b.LimitUSD <= 0 {
		return fmt.Errorf("store: set session budget: limit must be positive, got %v", b.LimitUSD)
	}
	if b.WarnUSD < 0 || b.WarnUSD > b.LimitUSD {
		return fmt.Errorf("store: set session budget: warning threshold %v must be between 0 and the limit %v", b.WarnUSD, b.LimitUSD)
	}
	at := b.UpdatedAt
	if at.IsZero() {
		at = time.Now().UTC()
	}
	updatedAt, err := encodeTime(at)
	if err != nil {
		return fmt.Errorf("store: set session budget: %w", err)
	}
	if _, err := d.db.ExecContext(ctx,
		`INSERT INTO session_budgets (session_id, limit_usd, warn_usd, override, updated_by, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT (session_id) DO UPDATE SET
			limit_usd  = excluded.limit_usd,
			warn_usd   = excluded.warn_usd,
			override   = excluded.override,
			updated_by = excluded.updated_by,
			updated_at = excluded.updated_at`,
		b.SessionID, b.LimitUSD, b.WarnUSD, b.Override, b.UpdatedBy, updatedAt,
	); err != nil {
		return fmt.Errorf("store: set session budget %s: %w", b.SessionID, mapConstraint(err, ErrNotFound))
	}
	b.UpdatedAt = at
	return nil
}

func (d *DB) GetSessionBudget(ctx context.Context, session domain.SessionID) (*SessionBudget, error) {
	var (
		b         SessionBudget
		updatedAt int64
	)
	err := d.db.QueryRowContext(ctx,
		`SELECT session_id, limit_usd, warn_usd, override, updated_by, updated_at
		 FROM session_budgets WHERE session_id = ?`, session,
	).Scan(&b.SessionID, &b.LimitUSD, &b.WarnUSD, &b.Override, &b.UpdatedBy, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: get session budget %s: %w", session, err)
	}
	b.UpdatedAt = decodeTime(updatedAt)
	return &b, nil
}

func (d *DB) DeleteSessionBudget(ctx context.Context, session domain.SessionID) error {
	if _, err := d.db.ExecContext(ctx,
		`DELETE FROM session_budgets WHERE session_id = ?`, session); err != nil {
		return fmt.Errorf("store: delete session budget %s: %w", session, err)
	}
	return nil
}

func scanRunCost(row interface{ Scan(...any) error }) (*RunCost, error) {
	var (
		c          RunCost
		recordedAt int64
	)
	if err := row.Scan(&c.RunID, &c.SessionID, &c.MemberID, &c.InputTokens,
		&c.OutputTokens, &c.CostUSD, &c.Metered, &recordedAt); err != nil {
		return nil, err
	}
	c.RecordedAt = decodeTime(recordedAt)
	return &c, nil
}
