package store

import (
	"context"
	"fmt"

	"github.com/3xDevOps/Aether/internal/domain"
)

// CheckWorkspaceDeletion checks durable work that can launch without a run row.
// The server holds the scheduler's workspace lifecycle gate.
func (d *DB) CheckWorkspaceDeletion(ctx context.Context, id domain.WorkspaceID) error {
	if _, err := d.GetWorkspace(ctx, id); err != nil {
		return err
	}
	placeholders, liveArgs := liveStatusArgs()
	checks := []struct {
		query   string
		message string
		extra   []any
	}{
		{`SELECT EXISTS (SELECT 1 FROM schedules s JOIN templates t ON t.id = s.template_id WHERE t.workspace_id = ?)`, "remove workspace schedules before deleting the workspace", nil},
		{`SELECT EXISTS (SELECT 1 FROM run_repos rr JOIN runs r ON r.id = rr.run_id WHERE rr.workspace_id = ? AND r.status IN (` + placeholders + `))`, "workspace is an extra repository of an unfinished run", liveArgs},
	}
	for _, check := range checks {
		var blocked bool
		if err := d.db.QueryRowContext(ctx, check.query, append([]any{id}, check.extra...)...).Scan(&blocked); err != nil {
			return fmt.Errorf("store: check workspace deletion: %w", err)
		}
		if blocked {
			return fmt.Errorf("%w: %s", ErrInUse, check.message)
		}
	}
	return nil
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
		`DELETE FROM run_repos WHERE workspace_id = ?`,
	} {
		if _, err = tx.ExecContext(ctx, query, id); err != nil {
			return fmt.Errorf("store: delete workspace data: %w", mapConstraint(err, ErrInUse))
		}
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
