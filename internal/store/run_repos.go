package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/3xDevOps/Aether/internal/domain"
)

// SetRunRepos writes a run's extras once, whole: their position names their git engine checkout.
func (d *DB) SetRunRepos(ctx context.Context, id domain.RunID, repos []domain.RunRepo) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: set run repos: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for i, repo := range repos {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO run_repos (run_id, position, workspace_id, name, mount_path, branch, base_branch, base_commit, worktree)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, i+1, repo.WorkspaceID, repo.Name, repo.Path, repo.Branch, repo.BaseBranch, repo.BaseCommit, repo.Worktree,
		); err != nil {
			return fmt.Errorf("store: set run repos: %w", mapConstraint(err, ErrNotFound))
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: set run repos: commit: %w", err)
	}
	return nil
}

func (d *DB) RunsWithExtraRepo(ctx context.Context, workspace domain.WorkspaceID) ([]domain.RunID, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT DISTINCT run_id FROM run_repos WHERE workspace_id = ? ORDER BY run_id`, workspace)
	if err != nil {
		return nil, fmt.Errorf("store: runs with extra repo: %w", err)
	}
	defer rows.Close() //nolint:errcheck // read-only iteration
	var ids []domain.RunID
	for rows.Next() {
		var id domain.RunID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("store: scan run with extra repo: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: runs with extra repo: %w", err)
	}
	return ids, nil
}

const runRepoCols = `run_id, position, workspace_id, name, mount_path, branch, base_branch, base_commit, worktree`

func (d *DB) attachExtras(ctx context.Context, runs ...*domain.Run) error {
	if len(runs) == 0 {
		return nil
	}
	var (
		rows *sql.Rows
		err  error
	)
	if len(runs) == 1 {
		rows, err = d.db.QueryContext(ctx, `SELECT `+runRepoCols+` FROM run_repos WHERE run_id = ? ORDER BY position`, runs[0].ID)
	} else {
		rows, err = d.db.QueryContext(ctx, `SELECT `+runRepoCols+` FROM run_repos ORDER BY run_id, position`)
	}
	if err != nil {
		return fmt.Errorf("store: read run repos: %w", err)
	}
	defer rows.Close() //nolint:errcheck // read-only iteration
	byRun := make(map[domain.RunID]*domain.Run, len(runs))
	for _, r := range runs {
		byRun[r.ID] = r
	}
	for rows.Next() {
		var (
			id       domain.RunID
			position int
			repo     domain.RunRepo
		)
		if err := rows.Scan(&id, &position, &repo.WorkspaceID, &repo.Name, &repo.Path, &repo.Branch, &repo.BaseBranch, &repo.BaseCommit, &repo.Worktree); err != nil {
			return fmt.Errorf("store: scan run repo: %w", err)
		}
		if r := byRun[id]; r != nil {
			repo.Key = domain.RepoKey(id, position)
			r.Extras = append(r.Extras, repo)
		}
	}
	return rows.Err()
}

func (d *DB) listRuns(ctx context.Context, what, query string, args ...any) ([]*domain.Run, error) {
	rows, err := d.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: %s: %w", what, err)
	}
	runs, err := collect(rows, scanRun)
	if err != nil {
		return nil, err
	}
	if err := d.attachExtras(ctx, runs...); err != nil {
		return nil, err
	}
	return runs, nil
}
