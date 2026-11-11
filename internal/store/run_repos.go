package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/3xDevOps/Aether/internal/domain"
)

// SetRunRepos writes a run's extras whole: their position names their git
// engine checkout. CreateRun already wrote them, so this fills in what
// checkout creation learned.
func (d *DB) SetRunRepos(ctx context.Context, id domain.RunID, repos []domain.RunRepo) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: set session repos: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := upsertRunRepos(ctx, tx, id, repos); err != nil {
		return fmt.Errorf("store: set session repos: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: set session repos: commit: %w", err)
	}
	return nil
}

func upsertRunRepos(ctx context.Context, tx *sql.Tx, id domain.RunID, repos []domain.RunRepo) error {
	for i, repo := range repos {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO run_repos (run_id, position, workspace_id, name, mount_path, branch, base_branch, base_commit, worktree)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT (run_id, position) DO UPDATE SET
			   workspace_id = excluded.workspace_id, name = excluded.name, mount_path = excluded.mount_path,
			   branch = excluded.branch, base_branch = excluded.base_branch, base_commit = excluded.base_commit,
			   worktree = excluded.worktree`,
			id, i+1, repo.WorkspaceID, repo.Name, repo.Path, repo.Branch, repo.BaseBranch, repo.BaseCommit, repo.Worktree,
		); err != nil {
			return fmt.Errorf("repo %d: %w", i+1, mapConstraint(err, ErrNotFound))
		}
	}
	return nil
}

func (d *DB) RunsWithExtraRepo(ctx context.Context, workspace domain.WorkspaceID) ([]domain.RunID, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT DISTINCT run_id FROM run_repos WHERE workspace_id = ? ORDER BY run_id`, workspace)
	if err != nil {
		return nil, fmt.Errorf("store: sessions with extra repo: %w", err)
	}
	defer rows.Close() //nolint:errcheck // read-only iteration
	var ids []domain.RunID
	for rows.Next() {
		var id domain.RunID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("store: scan session with extra repo: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: sessions with extra repo: %w", err)
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
		return fmt.Errorf("store: read session repos: %w", err)
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
			return fmt.Errorf("store: scan session repo: %w", err)
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
