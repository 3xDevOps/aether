package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

// ApprovalRequested is the decision value of a request nobody has decided
// yet. It mirrors events.ApprovalRequested; the store keeps the decision
// an opaque string rather than importing the event vocabulary.
const ApprovalRequested = "requested"

// Approval is one entry in a session's approval inbox: a permission
// request raised by a run, plus the decision a member made on it.
type Approval struct {
	ID        string
	SessionID domain.SessionID
	RunID     domain.RunID
	// SourceID is the raising request's own identity within its run - the
	// agent's tool-use id for the pause. It is unique per run, so the same
	// pause delivered twice cannot become two requests. Empty means the
	// raiser has no stable identity and every write is a new request.
	SourceID string
	// Action names what is being asked for, e.g. the tool the agent
	// paused on.
	Action string
	// Detail is the human-readable body of the request (plan text, command).
	Detail    string
	Decision  string
	DecidedBy domain.MemberID
	CreatedAt time.Time
	DecidedAt *time.Time
}

// ApprovalStore is the approval-inbox persistence surface.
type ApprovalStore interface {
	// CreateApproval assigns the request's ID and CreatedAt and stores it
	// undecided; any Decision on the passed struct is ignored. A non-empty
	// SourceID makes the write idempotent within the run: the stored
	// request is loaded back into a instead of a second row being inserted.
	CreateApproval(ctx context.Context, a *Approval) error
	GetApproval(ctx context.Context, id string) (*Approval, error)
	// ListApprovals returns a session's requests oldest first; decision
	// narrows to one decision value, empty returns all.
	ListApprovals(ctx context.Context, session domain.SessionID, decision string) ([]*Approval, error)
	// DecideApproval records a decision on a still-undecided request.
	// A request that was already decided returns ErrConflict.
	DecideApproval(ctx context.Context, id, decision string, by domain.MemberID, at time.Time) error
}

const approvalCols = `id, session_id, run_id, source_id, action, detail, decision, decided_by, created_at, decided_at`

func (d *DB) CreateApproval(ctx context.Context, a *Approval) error {
	if a.SessionID == "" || a.RunID == "" || a.Action == "" {
		return errors.New("store: create approval: session_id, run_id, and action are required")
	}
	id, ts, err := prepareCreate(a.CreatedAt)
	if err != nil {
		return err
	}
	createdAt, err := encodeTime(ts)
	if err != nil {
		return fmt.Errorf("store: create approval: %w", err)
	}
	res, err := d.db.ExecContext(ctx,
		`INSERT INTO approvals (`+approvalCols+`)
		 VALUES (?, ?, ?, ?, ?, ?, ?, '', ?, NULL)
		 ON CONFLICT (run_id, source_id) WHERE source_id <> '' DO NOTHING`,
		id, a.SessionID, a.RunID, a.SourceID, a.Action, a.Detail, ApprovalRequested, createdAt,
	)
	if err != nil {
		return fmt.Errorf("store: create approval: %w", mapConstraint(err, ErrNotFound))
	}
	inserted, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: create approval: %w", err)
	}
	if inserted == 0 {
		// The run already has this request; return the stored one so a
		// replayed event decides nothing new.
		stored, gerr := scanApproval(d.db.QueryRowContext(ctx,
			`SELECT `+approvalCols+` FROM approvals WHERE run_id = ? AND source_id = ?`,
			a.RunID, a.SourceID))
		if gerr != nil {
			return fmt.Errorf("store: create approval %s/%s: %w", a.RunID, a.SourceID, gerr)
		}
		*a = *stored
		return nil
	}
	a.ID, a.CreatedAt, a.Decision, a.DecidedBy, a.DecidedAt = id, ts, ApprovalRequested, "", nil
	return nil
}

func (d *DB) GetApproval(ctx context.Context, id string) (*Approval, error) {
	a, err := scanApproval(d.db.QueryRowContext(ctx,
		`SELECT `+approvalCols+` FROM approvals WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: get approval: %w", err)
	}
	return a, nil
}

func (d *DB) ListApprovals(ctx context.Context, session domain.SessionID, decision string) ([]*Approval, error) {
	query := `SELECT ` + approvalCols + ` FROM approvals WHERE session_id = ?`
	args := []any{session}
	if decision != "" {
		query += ` AND decision = ?`
		args = append(args, decision)
	}
	rows, err := d.db.QueryContext(ctx, query+` ORDER BY id`, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list approvals: %w", err)
	}
	return collect(rows, scanApproval)
}

func (d *DB) DecideApproval(ctx context.Context, id, decision string, by domain.MemberID, at time.Time) error {
	if decision == "" || decision == ApprovalRequested {
		return fmt.Errorf("store: decide approval: invalid decision %q", decision)
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	decidedAt, err := encodeTime(at)
	if err != nil {
		return fmt.Errorf("store: decide approval: %w", err)
	}
	err = notFoundOnZeroRows(d.db.ExecContext(ctx,
		`UPDATE approvals SET decision = ?, decided_by = ?, decided_at = ?
		 WHERE id = ? AND decision = ?`,
		decision, by, decidedAt, id, ApprovalRequested))
	if errors.Is(err, ErrNotFound) {
		// Either the request does not exist or somebody already decided
		// it; only the second is a conflict.
		if _, gerr := d.GetApproval(ctx, id); gerr == nil {
			return fmt.Errorf("store: decide approval %s: %w: already decided", id, ErrConflict)
		}
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("store: decide approval: %w", mapConstraint(err, ErrNotFound))
	}
	return nil
}

func scanApproval(row interface{ Scan(...any) error }) (*Approval, error) {
	var (
		a         Approval
		createdAt int64
		decidedAt *int64
	)
	if err := row.Scan(&a.ID, &a.SessionID, &a.RunID, &a.SourceID, &a.Action, &a.Detail,
		&a.Decision, &a.DecidedBy, &createdAt, &decidedAt); err != nil {
		return nil, err
	}
	a.CreatedAt = decodeTime(createdAt)
	a.DecidedAt = decodeTimePtr(decidedAt)
	return &a, nil
}
