package children

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/fallback"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

// dispatch starts queued children while their parent has free slots, the
// oldest request first.
func (s *Service) dispatch(ctx context.Context) error {
	queued, err := s.cfg.Store.ChildrenInState(ctx, domain.ChildQueued)
	if err != nil {
		return err
	}
	parents := map[domain.RunID][]*domain.ChildReservation{}
	var order []domain.RunID
	for _, c := range queued {
		if parents[c.ParentRunID] == nil {
			order = append(order, c.ParentRunID)
		}
		parents[c.ParentRunID] = append(parents[c.ParentRunID], c)
	}
	for _, parent := range order {
		list := parents[parent]
		limits, err := s.cfg.Store.WorkspaceChildLimits(ctx, list[0].WorkspaceID)
		if err != nil {
			return err
		}
		counts, err := s.cfg.Store.ChildCounts(ctx, parent)
		if err != nil {
			return err
		}
		for free := limits.MaxLive - counts.Busy; free > 0 && len(list) > 0; free-- {
			c := list[0]
			list = list[1:]
			if err := s.cfg.Store.SetChildState(ctx, c.RunID, domain.ChildLaunching, "", domain.ChildQueued); err != nil {
				if errors.Is(err, store.ErrConflict) {
					free++
					continue
				}
				return err
			}
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
		s.publish(ctx, c.ParentRunID, "")
		// A child that never started has no run to send from.
		_ = s.notify(ctx, c.ParentRunID, c.ParentRunID,
			fmt.Sprintf("Child run %s (%s) did not start: %s", c.RunID, c.Purpose, reason), "child-refused:"+string(c.RunID))
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
			if admitErr := cost.Admit(ctx, c.WorkspaceID, c.OwnerID); admitErr != nil {
				refuse(admitErr.Error())
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
	// A handoff takes this lock, so the parent read here is the one the
	// launch runs under.
	parent, getErr := s.cfg.Store.GetRun(ctx, c.ParentRunID)
	if getErr != nil || parent.MemberID != c.OwnerID || parent.Status.Terminal() {
		unlock()
		refuse("the parent run ended or changed owner before this child started")
		return
	}
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
	extras := make([]domain.WorkspaceID, len(p.parent.Extras))
	for i, repo := range p.parent.Extras {
		extras[i] = repo.WorkspaceID
	}
	_, err = s.cfg.Runs.LaunchWithOptions(ctx, c.WorkspaceID, c.OwnerID, account, c.Task, c.Harness, c.Mode, domain.LaunchOptions{
		Model: c.Model, Permission: parent.Permission, RequestedAccount: requested, Fallback: c.Fallback, ReservedRunID: c.RunID,
		FromCommit: c.BaseCommit, FromBranch: c.BaseBranch, ExtraWorkspaces: extras, Admitted: unlock,
		Created: func(domain.RunID) {
			created = true
			if stateErr := s.cfg.Store.SetChildState(ctx, c.RunID, domain.ChildStarted, "", domain.ChildLaunching); stateErr != nil {
				slog.Warn("children: mark child started", "child", c.RunID, "error", stateErr)
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

// maxNoticeSummary bounds the report summary in a parent's notice, which a
// message cuts at 4 KiB, so the child's branch after it survives.
const maxNoticeSummary = 3000

// settle ends the attached children of ended parents and tells a parent
// once when a child ended without a final report. A parent a person closed
// takes its children with it: finished ones are closed as it was, live ones
// are stopped and closed. A parent that ended otherwise detaches children
// that already reported and stops the rest.
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
		child, childErr := s.cfg.Store.GetRun(ctx, c.RunID)
		childLive := childErr == nil && !child.Status.Terminal()
		parentEnded := gone || parent.Status.Terminal()
		// A person closed the parent when they chose its final disposition
		// or accepted its completion; a kill also ends a run abandoned but
		// leaves its finished children for review.
		closed := !gone && (parent.Status == domain.RunMerged ||
			(parent.Status == domain.RunAbandoned && parent.Reason != "killed") ||
			(parent.Status == domain.RunCompleted && parent.Review == domain.ReviewAccepted))
		if !c.Detached && parentEnded && closed && childErr == nil {
			// A child still provisioning cannot be closed; stop it first.
			if child.Status == domain.RunQueued || child.Status == domain.RunProvisioning {
				if stopErr := s.stop(ctx, c); stopErr != nil {
					slog.Warn("children: stop the child of a closed run", "child", c.RunID, "parent", c.ParentRunID, "error", stopErr)
				}
				continue
			}
			// A child already in a final state has nothing to close.
			if child.Status.Final() {
				if _, err := s.cfg.Store.MarkChildEndNotified(ctx, c.RunID); err != nil {
					return err
				}
				continue
			}
			// The child's result was delivered to the parent and is part of
			// its work: close it as the parent was.
			var closeErr error
			if parent.Status == domain.RunCompleted {
				_, closeErr = s.cfg.Runs.Review(ctx, c.RunID, c.OwnerID, protocol.RunReviewComplete, false)
			} else {
				closeErr = s.cfg.Runs.CloseRun(ctx, c.RunID, c.OwnerID, parent.Status)
			}
			if closeErr != nil {
				slog.Warn("children: close the child of a closed run", "child", c.RunID, "parent", c.ParentRunID, "error", closeErr)
				continue
			}
			if _, err := s.cfg.Store.MarkChildEndNotified(ctx, c.RunID); err != nil {
				return err
			}
			s.publish(ctx, c.RunID, "")
			continue
		}
		if !c.Detached && parentEnded && (c.State == domain.ChildQueued || childLive) {
			// A child that already delivered its result keeps it for review
			// instead of being stopped with its parent.
			reported, err := s.cfg.Store.ChildReported(ctx, c.RunID)
			if err != nil {
				return err
			}
			if childLive && (reported || finished(child)) {
				if err := s.cfg.Store.DetachChild(ctx, c.RunID); err != nil {
					return err
				}
				s.publish(ctx, c.RunID, "")
				continue
			}
			if stopErr := s.stop(ctx, c); stopErr != nil {
				slog.Warn("children: stop the child of an ended run", "child", c.RunID, "parent", c.ParentRunID, "error", stopErr)
			}
			continue
		}
		if childErr != nil {
			return childErr
		}
		if c.Detached || gone || c.EndNotified {
			if _, err := s.cfg.Store.MarkChildEndNotified(ctx, c.RunID); err != nil {
				return err
			}
			continue
		}
		if _, err := s.cfg.Store.LatestCoordReport(ctx, c.RunID); err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		} else if err != nil {
			// The notice's key makes a resend after a failed mark a no-op.
			msg := fmt.Sprintf("Child run %s (%s) ended %s without a final report", c.RunID, c.Purpose, child.Status)
			if child.Reason != "" {
				msg += ": " + child.Reason
			}
			if latest, err := s.cfg.Store.LatestReportOfAnyOutcome(ctx, c.RunID); err == nil {
				msg += fmt.Sprintf("\nIts latest report, %s at %s: %s", latest.Outcome, latest.CreatedAt.UTC().Format(time.RFC3339), truncateSummary(latest.Summary))
			}
			if child.Branch != "" {
				msg += "\nIts branch: " + child.Branch
			}
			if err := s.notify(ctx, c.RunID, c.ParentRunID, msg, "child-ended:"+string(c.RunID)); err != nil {
				continue
			}
		}
		if _, err := s.cfg.Store.MarkChildEndNotified(ctx, c.RunID); err != nil {
			return err
		}
	}
	return nil
}

func finished(r *domain.Run) bool {
	switch r.Activity {
	case domain.ActivityFinished, domain.ActivityAnswered, domain.ActivityFailed:
		return true
	}
	return false
}

// idleNoticeAfter is how long a child may sit quiet before its parent is
// told.
const idleNoticeAfter = time.Hour

// noticeIdle tells each parent once per quiet spell about an attached child
// that has waited, answered or asked its owner for over idleNoticeAfter. The
// notice's key holds the spell's start, so new work starts a new spell. A run
// a person paused is left alone.
func (s *Service) noticeIdle(ctx context.Context) error {
	now := s.cfg.Now()
	idle, err := s.cfg.Store.IdleChildren(ctx, now.Add(-idleNoticeAfter))
	if err != nil {
		return err
	}
	for _, c := range idle {
		if s.cfg.Runs.Paused(c.Child.RunID) {
			continue
		}
		// The body names a growing duration, so a resend under the same key
		// would be refused as a different message.
		key := "child-idle:" + string(c.Child.RunID) + ":" + c.Since.UTC().Format(time.RFC3339Nano)
		if _, err := s.cfg.Store.GetRunMessageByIdempotency(ctx, c.Child.RunID, key); err == nil {
			continue
		} else if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		msg := fmt.Sprintf("Child run %s (%s) has been %s for %s", c.Child.RunID, c.Child.Purpose, c.Run.Activity, now.Sub(c.Since).Round(time.Minute))
		if c.Run.Reason != "" {
			msg += ": " + c.Run.Reason
		}
		if latest, err := s.cfg.Store.LatestReportOfAnyOutcome(ctx, c.Child.RunID); err == nil {
			msg += fmt.Sprintf("\nIts latest report, %s at %s: %s", latest.Outcome, latest.CreatedAt.UTC().Format(time.RFC3339), truncateSummary(latest.Summary))
		} else if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		msg += "\nCheck what it is doing, then wake it with an instruction: aether-internal send --to " + string(c.Child.RunID) +
			" --body <text>, or stop it: aether-internal child cancel --run-id " + string(c.Child.RunID)
		_ = s.notify(ctx, c.Child.RunID, c.Child.ParentRunID, msg, key)
	}
	return nil
}

func truncateSummary(summary string) string {
	if len(summary) > maxNoticeSummary {
		return strings.ToValidUTF8(summary[:maxNoticeSummary], "") + "…"
	}
	return summary
}
