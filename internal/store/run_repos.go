package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/3xDevOps/Aether/internal/domain"
)

// SetRunRepos records the extra repositories of a run, numbering them from 1
// in order. Their position names their checkout in the git engine, so a run's
// extras are written once, whole.
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

const runRepoCols = `run_id, position, workspace_id, name, mount_path, branch, base_branch, base_commit, worktree`

// attachExtras fills Run.Extras. Only multi-repo runs have rows, so a list
// reads the whole table rather than a clause per run.
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
