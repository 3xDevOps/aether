package gitengine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strconv"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

// Extra repositories are checkouts keyed by domain.RepoKey; the exported per-run
// operations act on the run's own checkout and then each extra one.

// extraKeys lists the run's extra checkouts. With history it also lists those
// whose checkout is gone but whose snapshot store is retained.
func (e *Engine) extraKeys(run domain.RunID, history bool) ([]domain.RunID, error) {
	entries, err := os.ReadDir(e.cfg.CheckoutsDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("gitengine: list extra repositories of run %s: %w", run, err)
	}
	pattern := regexp.MustCompile("^" + regexp.QuoteMeta(string(run)) + `-r([0-9]+)(` + regexp.QuoteMeta(snapshotStoreSuffix) + `)?$`)
	positions := make(map[int]domain.RunID)
	for _, entry := range entries {
		m := pattern.FindStringSubmatch(entry.Name())
		if m == nil || !entry.IsDir() || m[2] != "" && !history {
			continue
		}
		position, _ := strconv.Atoi(m[1])
		positions[position] = domain.RepoKey(run, position)
	}
	order := make([]int, 0, len(positions))
	for position := range positions {
		order = append(order, position)
	}
	slices.Sort(order)
	keys := make([]domain.RunID, len(order))
	for i, position := range order {
		keys[i] = positions[position]
	}
	return keys, nil
}

func (e *Engine) eachExtra(run domain.RunID, history bool, fn func(domain.RunID) error) error {
	keys, err := e.extraKeys(run, history)
	if err != nil {
		return err
	}
	var errs []error
	for _, key := range keys {
		if err := fn(key); err != nil {
			errs = append(errs, fmt.Errorf("extra repository %s: %w", key, err))
		}
	}
	return errors.Join(errs...)
}

func (e *Engine) StartDiffWatch(ctx context.Context, workspace domain.WorkspaceID, run domain.RunID) error {
	if err := e.startDiffWatch(ctx, workspace, run); err != nil {
		return err
	}
	return e.eachExtra(run, false, func(key domain.RunID) error { return e.startDiffWatch(ctx, workspace, key) })
}

func (e *Engine) LastFileChange(run domain.RunID) (time.Time, bool) {
	newest, found := e.lastFileChange(run)
	e.mu.Lock()
	var extras []domain.RunID
	for key := range e.watches {
		if parent, extra := domain.ParentRun(key); extra && parent == run {
			extras = append(extras, key)
		}
	}
	e.mu.Unlock()
	for _, key := range extras {
		if t, ok := e.lastFileChange(key); ok && t.After(newest) {
			newest, found = t, true
		}
	}
	return newest, found
}

// StopDiffWatch takes one last snapshot per checkout, so the final run.diff and
// published tree include changes still waiting on the quiet-period timer.
func (e *Engine) StopDiffWatch(run domain.RunID) {
	e.stopDiffWatch(run, true)
	e.mu.Lock()
	var extras []domain.RunID
	for key := range e.watches {
		if parent, extra := domain.ParentRun(key); extra && parent == run {
			extras = append(extras, key)
		}
	}
	e.mu.Unlock()
	for _, key := range extras {
		e.stopDiffWatch(key, true)
	}
}

func (e *Engine) CommitAll(ctx context.Context, run domain.RunID, message string, author domain.GitIdentity, signingKey []byte) (string, error) {
	commit, err := e.commitAll(ctx, run, message, author, signingKey)
	if err != nil {
		return "", err
	}
	return commit, e.eachExtra(run, false, func(key domain.RunID) error {
		_, err := e.commitAll(ctx, key, message, author, signingKey)
		return err
	})
}

func (e *Engine) PublishRunBranch(ctx context.Context, run domain.RunID) (string, error) {
	commit, err := e.publishRunBranch(ctx, run)
	if err != nil {
		return "", err
	}
	return commit, e.eachExtra(run, false, func(key domain.RunID) error {
		_, err := e.publishRunBranch(ctx, key)
		return err
	})
}

func (e *Engine) RemoveRunCheckout(ctx context.Context, run domain.RunID) error {
	return errors.Join(e.removeRunCheckout(ctx, run), e.eachExtra(run, false, func(key domain.RunID) error {
		return e.removeRunCheckout(ctx, key)
	}))
}

func (e *Engine) RemoveRunHistory(ctx context.Context, run domain.RunID) error {
	return errors.Join(e.removeRunHistory(ctx, run), e.eachExtra(run, true, func(key domain.RunID) error {
		return e.removeRunHistory(ctx, key)
	}))
}
