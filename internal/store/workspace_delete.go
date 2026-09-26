package store

import (
	"context"
	"fmt"

	"github.com/3xDevOps/Aether/internal/domain"
)

// CheckWorkspaceDeletion checks durable work that can launch without a run row.
// The server holds mission admission and the scheduler's workspace lifecycle gate.
func (d *DB) CheckWorkspaceDeletion(ctx context.Context, id domain.WorkspaceID) error {
	if _, err := d.GetWorkspace(ctx, id); err != nil {
		return err
	}
	checks := []struct {
		query   string
		message string
	}{
		{`SELECT EXISTS (SELECT 1 FROM schedules s JOIN templates t ON t.id = s.template_id WHERE t.workspace_id = ?)`, "remove workspace schedules before deleting the workspace"},
		{`SELECT EXISTS (SELECT 1 FROM missions WHERE workspace_id = ? AND phase != 'rejected' AND integrator_run_launched = 0)`, "workspace has a pending mission integrator launch"},
		{`SELECT EXISTS (SELECT 1 FROM mission_attempts a JOIN missions m ON m.id = a.mission_id WHERE m.workspace_id = ? AND a.state IN ('reserved', 'launching', 'running', 'unknown', 'submitted'))`, "workspace has active or queued mission attempts"},
	}
	for _, check := range checks {
		var blocked bool
		if err := d.db.QueryRowContext(ctx, check.query, id).Scan(&blocked); err != nil {
			return fmt.Errorf("store: check workspace deletion: %w", err)
		}
		if blocked {
			return fmt.Errorf("%w: %s", ErrInUse, check.message)
		}
	}
	return nil
}

// DeleteWorkspaceMissions releases submission references before finished runs
// are purged. It is called only after every runtime admission check succeeds.
func (d *DB) DeleteWorkspaceMissions(ctx context.Context, id domain.WorkspaceID) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `PRAGMA defer_foreign_keys = ON`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM missions WHERE workspace_id = ?`, id); err != nil {
		return fmt.Errorf("store: delete workspace missions: %w", err)
	}
	return tx.Commit()
}

func (d *DB) DeleteWorkspace(ctx context.Context, id domain.WorkspaceID) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, query := range []string{
		`DELETE FROM workspace_budgets WHERE workspace_id = ?`,
		`DELETE FROM templates WHERE workspace_id = ?`,
		`DELETE FROM coord_audit_publications WHERE workspace_id = ?`,
		`DELETE FROM events WHERE workspace_id = ?`,
	} {
		if _, err = tx.ExecContext(ctx, query, id); err != nil {
			return fmt.Errorf("store: delete workspace data: %w", mapConstraint(err, ErrInUse))
		}
	}
	var candidates bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM integration_candidates WHERE workspace_id = ?)`, id).Scan(&candidates); err != nil {
		return err
	}
	if candidates {
		return fmt.Errorf("%w: workspace integration candidates have not been cleaned up", ErrInUse)
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM workspaces WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: delete workspace: %w", mapConstraint(err, ErrInUse))
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}
