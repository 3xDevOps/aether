package children

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/fallback"
	"github.com/3xDevOps/Aether/internal/store"
)

// dispatch starts queued children while their tree has free slots, the
// parent with the fewest busy children first, then the oldest request.
func (s *Service) dispatch(ctx context.Context) error {
	queued, err := s.cfg.Store.ChildrenInState(ctx, domain.ChildQueued)
	if err != nil {
		return err
	}
	trees := map[string][]*domain.ChildReservation{}
	var order []string
	for _, c := range queued {
		if trees[c.TreeKey] == nil {
			order = append(order, c.TreeKey)
		}
		trees[c.TreeKey] = append(trees[c.TreeKey], c)
	}
	for _, key := range order {
		list := trees[key]
		limits, err := s.cfg.Store.WorkspaceChildLimits(ctx, list[0].WorkspaceID)
		if err != nil {
			return err
		}
		counts, err := s.cfg.Store.ChildCounts(ctx, key)
		if err != nil {
			return err
		}
		busy := map[domain.RunID]int{}
		for _, c := range list {
			if _, ok := busy[c.ParentRunID]; !ok {
				if busy[c.ParentRunID], err = s.cfg.Store.BusyChildren(ctx, c.ParentRunID); err != nil {
					return err
				}
			}
		}
		for free := limits.MaxLive - counts.Busy; free > 0 && len(list) > 0; {
			sort.SliceStable(list, func(i, j int) bool { return busy[list[i].ParentRunID] < busy[list[j].ParentRunID] })
			c := list[0]
			list = list[1:]
			if err := s.cfg.Store.SetChildState(ctx, c.RunID, domain.ChildLaunching, "", domain.ChildQueued); err != nil {
				if errors.Is(err, store.ErrConflict) {
					continue
				}
				return err
			}
			busy[c.ParentRunID]++
			free--
			go s.launch(ctx, c)
		}
	}
	return nil
}

// launch re-admits a reserved child exactly as a run launch is admitted,
// then hands it to the scheduler under its reserved ID.
func (s *Service) launch(ctx context.Context, c *domain.ChildReservation) {
	refuse := func(reason string) {
		if err := s.cfg.Store.SetChildState(ctx, c.RunID, domain.ChildRefused, reason, domain.ChildLaunching); err != nil {
			slog.Warn("children: refuse child", "child", c.RunID, "error", err)
			return
		}
		s.notify(ctx, c.RunID, c.ParentRunID,
			fmt.Sprintf("Child run %s (%s) did not start: %s", c.RunID, c.Purpose, reason), "", "child-refused:"+string(c.RunID))
	}
	p, err := s.resolve(ctx, c.ParentRunID)
	switch {
	case errors.Is(err, store.ErrNotFound):
		_ = s.cfg.Store.SetChildState(ctx, c.RunID, domain.ChildCancelled, "the parent run was deleted", domain.ChildLaunching)
		return
	case err != nil:
		refuse(err.Error())
		return
	case p.parent.Status.Terminal() && !c.Detached:
		_ = s.cfg.Store.SetChildState(ctx, c.RunID, domain.ChildCancelled, "the parent run ended before it started", domain.ChildLaunching)
		return
	case p.refusal != "":
		refuse(p.refusal)
		return
	case p.owner != c.OwnerID:
		refuse(fmt.Sprintf("the parent run now belongs to %s, not %s, who asked for it", p.owner, c.OwnerID))
		return
	}
	if s.cfg.Cost != nil {
		if cost := s.cfg.Cost(); cost != nil {
			if err := cost.Admit(ctx, c.WorkspaceID, c.OwnerID); err != nil {
				refuse(err.Error())
				return
			}
		}
	}
	measured, err := fallback.Measure(ctx, s.cfg.Store, s.cfg.Usage, c.OwnerID, string(c.AccountID), c.Harness, c.Fallback)
	if err != nil {
		refuse(err.Error())
		return
	}
	s.cfg.AuthorizationMu.Lock()
	unlock := sync.OnceFunc(s.cfg.AuthorizationMu.Unlock)
	defer unlock()
	requested, err := s.cfg.Authorize(ctx, s.cfg.Store, c.OwnerID, string(c.AccountID))
	if err != nil {
		unlock()
		refuse(err.Error())
		return
	}
	account, excluded := fallback.Select(ctx, s.cfg.Store, measured, c.OwnerID, requested, c.Fallback, func(ctx context.Context, account domain.MemberID) (bool, error) {
		return s.cfg.Runs.FallbackLaunchable(ctx, c.OwnerID, requested, account, c.Harness, c.Mode)
	})
	if excluded != nil {
		slog.Warn("children: fallback accounts left out of a child launch", "child", c.RunID, "error", excluded)
	}
	created := false
	_, err = s.cfg.Runs.LaunchWithOptions(ctx, c.WorkspaceID, c.OwnerID, account, c.Task, c.Harness, c.Mode, domain.LaunchOptions{
		Model: c.Model, RequestedAccount: requested, Fallback: c.Fallback, ReservedRunID: c.RunID,
		FromCommit: c.BaseCommit, FromBranch: c.BaseBranch, Admitted: unlock,
		Created: func(domain.RunID) {
			created = true
			if err := s.cfg.Store.SetChildState(ctx, c.RunID, domain.ChildStarted, "", domain.ChildLaunching); err != nil {
				slog.Warn("children: mark child started", "child", c.RunID, "error", err)
			}
		},
	})
	if err != nil && !created {
		unlock()
		refuse(err.Error())
		return
	}
	s.Kick()
}

// settle stops the attached children of ended parents and tells a parent
// once when a child ended without a report.
func (s *Service) settle(ctx context.Context) error {
	list, err := s.cfg.Store.ChildrenToSettle(ctx)
	if err != nil {
		return err
	}
	for _, c := range list {
		parent, err := s.cfg.Store.GetRun(ctx, c.ParentRunID)
		gone := errors.Is(err, store.ErrNotFound)
		if err != nil && !gone {
			return err
		}
		if !c.Detached && (gone || parent.Status.Terminal()) {
			if err := s.stop(ctx, c, ""); err != nil {
				slog.Warn("children: stop the child of an ended run", "child", c.RunID, "parent", parent.ID, "error", err)
			}
			continue
		}
		child, err := s.cfg.Store.GetRun(ctx, c.RunID)
		if err != nil {
			return err
		}
		first, err := s.cfg.Store.MarkChildEndNotified(ctx, c.RunID)
		if err != nil || !first || c.Detached || gone {
			continue
		}
		if _, err := s.cfg.Store.LatestCoordReport(ctx, c.RunID); err == nil {
			continue
		} else if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		reason := ""
		if child.Reason != "" {
			reason = ": " + child.Reason
		}
		s.notify(ctx, c.RunID, c.ParentRunID,
			fmt.Sprintf("Child run %s (%s) ended %s without a report%s", c.RunID, c.Purpose, child.Status, reason),
			"", "child-ended:"+string(c.RunID))
	}
	return nil
}
