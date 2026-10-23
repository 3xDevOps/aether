package scheduler

import (
	"context"
	"fmt"
	"path"
	"slices"
	"sync"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/mirror"
)

type extraCapture struct {
	repos []domain.RunRepo
	err   error
}

// resolveExtras names the extra workspaces of a launch and assigns each its
// directory under /repos. An unknown workspace fails the launch.
func (s *Scheduler) resolveExtras(ctx context.Context, ids []domain.WorkspaceID) ([]domain.RunRepo, error) {
	taken := make(map[string]bool, len(ids))
	repos := make([]domain.RunRepo, len(ids))
	for i, id := range ids {
		ws, err := s.cfg.Store.GetWorkspace(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("repo workspace %s: %w", id, err)
		}
		dir := domain.RepoDirName(ws.Name)
		for n := 2; taken[dir]; n++ {
			dir = fmt.Sprintf("%s-%d", domain.RepoDirName(ws.Name), n)
		}
		taken[dir] = true
		repos[i] = domain.RunRepo{WorkspaceID: id, Name: ws.Name, Path: path.Join(domain.ExtraReposRoot, dir)}
	}
	return repos, nil
}

// captureExtras captures the base of every extra repository in parallel, so
// extra repositories add no launch latency of their own. The first failure
// names the repository.
func (s *Scheduler) captureExtras(ctx context.Context, bases BaseCapture, repos []domain.RunRepo) ([]domain.RunRepo, error) {
	captured := slices.Clone(repos)
	errs := make([]error, len(captured))
	var wg sync.WaitGroup
	for i := range captured {
		wg.Go(func() {
			var base mirror.CaptureResult
			base, errs[i] = bases.Capture(ctx, captured[i].WorkspaceID, "")
			captured[i].BaseBranch, captured[i].BaseCommit = base.Branch, base.Commit
		})
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			return nil, fmt.Errorf("repo %q (workspace %s): %v", captured[i].Name, captured[i].WorkspaceID, err)
		}
	}
	return captured, nil
}

// createCheckouts creates the run's checkouts, the primary and every extra
// repository's, in parallel. On any failure the checkouts that were made are
// removed, so a refused launch leaves none behind.
func (s *Scheduler) createCheckouts(ctx context.Context, run *domain.Run, ws *domain.Workspace) (checkout, branch string, err error) {
	errs := make([]error, 1+len(run.Extras))
	var wg sync.WaitGroup
	wg.Go(func() {
		checkout, branch, errs[0] = s.cfg.Git.CreateRunCheckoutAt(ctx, ws.ID, run.ID, run.BaseCommit, run.BaseBranch, run.Task, ws.Origin)
	})
	for i := range run.Extras {
		extra := &run.Extras[i]
		wg.Go(func() {
			extraWS, extraErr := s.cfg.Store.GetWorkspace(ctx, extra.WorkspaceID)
			if extraErr == nil {
				extra.Worktree, extra.Branch, extraErr = s.cfg.Git.CreateRunCheckoutAt(ctx, extra.WorkspaceID, extra.Key, extra.BaseCommit, extra.BaseBranch, run.Task, extraWS.Origin)
			}
			if extraErr != nil {
				extraErr = fmt.Errorf("repo %q (workspace %s): %w", extra.Name, extra.WorkspaceID, extraErr)
			}
			errs[i+1] = extraErr
		})
	}
	wg.Wait()
	for _, e := range errs {
		if e != nil {
			err = e
			break
		}
	}
	if err != nil {
		if rmErr := s.cfg.Git.RemoveRunCheckout(context.WithoutCancel(ctx), run.ID); rmErr != nil {
			err = fmt.Errorf("%w (and removing the checkouts failed: %v)", err, rmErr)
		}
		return "", "", err
	}
	return checkout, branch, nil
}
