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

// A multi-repo run's extra repositories are checkouts of their own, keyed by
// domain.RepoKey. The exported per-run operations below act on the run's own
// checkout and then on each extra one, so a caller holding only the run ID
// covers every repository; the first failure of an extra names its key.

// extraKeys lists the checkout keys of run's extra repositories, found on
// disk like every other per-run fact, in position order.
func (e *Engine) extraKeys(run domain.RunID) []domain.RunID {
	entries, err := os.ReadDir(e.cfg.CheckoutsDir)
	if err != nil {
		return nil
	}
	pattern, err := regexp.Compile("^" + regexp.QuoteMeta(string(run)) + `-r([0-9]+)$`)
	if err != nil {
		return nil
	}
	type extra struct {
		position int
		key      domain.RunID
	}
	var found []extra
	for _, entry := range entries {
		if m := pattern.FindStringSubmatch(entry.Name()); m != nil && entry.IsDir() {
			position, _ := strconv.Atoi(m[1])
			found = append(found, extra{position, domain.RunID(entry.Name())})
		}
	}
	slices.SortFunc(found, func(a, b extra) int { return a.position - b.position })
	keys := make([]domain.RunID, len(found))
	for i, f := range found {
		keys[i] = f.key
	}
	return keys
}

// eachExtra runs fn on every extra repository of run and joins the failures.
func (e *Engine) eachExtra(run domain.RunID, fn func(domain.RunID) error) error {
	var errs []error
	for _, key := range e.extraKeys(run) {
		if err := fn(key); err != nil {
			errs = append(errs, fmt.Errorf("extra repository %s: %w", key, err))
		}
	}
	return errors.Join(errs...)
}

// StartDiffWatch watches the run's checkout and each extra one, all under
// the run's workspace scope.
func (e *Engine) StartDiffWatch(ctx context.Context, workspace domain.WorkspaceID, run domain.RunID) error {
	if err := e.startDiffWatch(ctx, workspace, run); err != nil {
		return err
	}
	return e.eachExtra(run, func(key domain.RunID) error { return e.startDiffWatch(ctx, workspace, key) })
}

// LastFileChange is the newest file change across the run's watched
// checkouts.
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

// StopDiffWatch stops the watches of the run's checkout and of each extra
// one.
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

// CommitAll commits the run's checkout and each extra one with the same
// message, returning the run's own commit.
func (e *Engine) CommitAll(ctx context.Context, run domain.RunID, message string, author domain.GitIdentity, signingKey []byte) (string, error) {
	commit, err := e.commitAll(ctx, run, message, author, signingKey)
	if err != nil {
		return "", err
	}
	return commit, e.eachExtra(run, func(key domain.RunID) error {
		_, err := e.commitAll(ctx, key, message, author, signingKey)
		return err
	})
}

// PublishRunBranch publishes the run's branch and each extra repository's,
// returning the run's own tip.
func (e *Engine) PublishRunBranch(ctx context.Context, run domain.RunID) (string, error) {
	commit, err := e.publishRunBranch(ctx, run)
	if err != nil {
		return "", err
	}
	return commit, e.eachExtra(run, func(key domain.RunID) error {
		_, err := e.publishRunBranch(ctx, key)
		return err
	})
}

// RemoveRunCheckout removes the run's checkout and every extra one.
func (e *Engine) RemoveRunCheckout(ctx context.Context, run domain.RunID) error {
	return errors.Join(e.removeRunCheckout(ctx, run), e.eachExtra(run, func(key domain.RunID) error {
		return e.removeRunCheckout(ctx, key)
	}))
}

// RemoveRunHistory removes the run's checkout and diff history and those of
// every extra repository.
func (e *Engine) RemoveRunHistory(ctx context.Context, run domain.RunID) error {
	return errors.Join(e.removeRunHistory(ctx, run), e.eachExtra(run, func(key domain.RunID) error {
		return e.removeRunHistory(ctx, key)
	}))
}
