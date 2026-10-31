package scheduler

import (
	"context"
	"fmt"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// Review records what actor did with a run's outcome. complete accepts it:
// a live run closes as completed the way CloseRun closes one, and a
// completed run only records the acceptance. later puts a reported success
// off and leaves the run where it is; a new success report asks again.
// reportedOnly completes a live run only when its reported success awaits
// review, judged here and again by closeRun under the run's lifecycle lock,
// which Relaunch also holds, so a run completed and reopened in between is
// not closed; a completed run's acceptance requires it still completed
// under s.mu.
func (s *Scheduler) Review(ctx context.Context, run domain.RunID, actor domain.MemberID, action string, reportedOnly bool) (*domain.Run, error) {
	r, err := s.cfg.Store.GetRun(ctx, run)
	if err != nil {
		return nil, err
	}
	switch action {
	case protocol.RunReviewLater:
		if r.Review != domain.ReviewReady && r.Review != domain.ReviewLater {
			return nil, fmt.Errorf("%w: session %s has no reported success to review later", ErrInvalidTransition, run)
		}
		return s.setReview(ctx, run, actor, domain.ReviewLater, domain.ReviewReady)
	case protocol.RunReviewComplete:
	default:
		return nil, fmt.Errorf("%w: review action must be %q or %q, got %q", ErrInvalidTransition, protocol.RunReviewComplete, protocol.RunReviewLater, action)
	}
	switch {
	case r.Review == domain.ReviewAccepted:
		return r, nil
	case r.Status.Live() && reportedOnly && r.Review != domain.ReviewReady:
		return nil, fmt.Errorf("%w: session %s", ErrReviewNotReported, run)
	case r.Status.Live():
		spec := humanClose(domain.RunCompleted, actor)
		spec.reportedOnly = reportedOnly
		if err := s.closeRun(ctx, run, spec); err != nil {
			return nil, err
		}
		if err := s.dismissFinished(ctx, run, actor); err != nil {
			return nil, err
		}
	case r.Status != domain.RunCompleted:
		return nil, fmt.Errorf("%w: a %s session cannot be marked complete", ErrInvalidTransition, r.Status)
	}
	return s.setReview(ctx, run, actor, domain.ReviewAccepted, "", domain.ReviewReady, domain.ReviewLater)
}

// setReview moves the run's review and announces it. s.mu orders the write
// and its event against the run.status events that also carry the review,
// and against a Relaunch that reopens the run between its close and here.
func (s *Scheduler) setReview(ctx context.Context, run domain.RunID, actor domain.MemberID, review domain.RunReview, from ...domain.RunReview) (*domain.Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if review == domain.ReviewAccepted {
		current, err := s.cfg.Store.GetRun(ctx, run)
		if err != nil {
			return nil, err
		}
		if current.Status != domain.RunCompleted {
			return nil, fmt.Errorf("%w: session %s is %s, not completed", ErrInvalidTransition, run, current.Status)
		}
	}
	stamp, changed, err := s.cfg.Store.SetRunReview(ctx, run, review, from...)
	if err != nil {
		return nil, err
	}
	fresh, err := s.cfg.Store.GetRun(ctx, run)
	if err != nil {
		return nil, err
	}
	if !changed {
		if fresh.Review != review {
			return nil, fmt.Errorf("%w: session %s review is %q", ErrInvalidTransition, run, fresh.Review)
		}
		return fresh, nil
	}
	s.publish(ctx, events.Event{
		WorkspaceID: fresh.WorkspaceID,
		RunID:       run,
		ActorID:     actor,
		Payload:     events.RunReviewPayload{Review: stamp.Review, ReviewChangedAt: stamp.ReviewChangedAt, StatusSeq: stamp.StatusSeq},
	})
	return fresh, nil
}
