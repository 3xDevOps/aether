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
func (s *Scheduler) Review(ctx context.Context, run domain.RunID, actor domain.MemberID, action string) (*domain.Run, error) {
	r, err := s.cfg.Store.GetRun(ctx, run)
	if err != nil {
		return nil, err
	}
	switch action {
	case protocol.RunReviewLater:
		if r.Review != domain.ReviewReady && r.Review != domain.ReviewLater {
			return nil, fmt.Errorf("%w: run %s has no reported success to review later", ErrInvalidTransition, run)
		}
		return s.setReview(ctx, run, actor, domain.ReviewLater, domain.ReviewReady)
	case protocol.RunReviewComplete:
	default:
		return nil, fmt.Errorf("%w: review action must be %q or %q, got %q", ErrInvalidTransition, protocol.RunReviewComplete, protocol.RunReviewLater, action)
	}
	switch {
	case r.Review == domain.ReviewAccepted:
		return r, nil
	case r.Status.Live() && r.MissionRole == "worker":
		return nil, fmt.Errorf("%w: swarm worker %s completes through its integrator", ErrInvalidTransition, run)
	case r.Status.Live():
		if err := s.closeRun(ctx, run, humanClose(domain.RunCompleted, actor)); err != nil {
			return nil, err
		}
		if err := s.dismissFinished(ctx, run, actor); err != nil {
			return nil, err
		}
	case r.Status != domain.RunCompleted:
		return nil, fmt.Errorf("%w: a %s run cannot be marked complete", ErrInvalidTransition, r.Status)
	}
	return s.setReview(ctx, run, actor, domain.ReviewAccepted, "", domain.ReviewReady, domain.ReviewLater)
}

// setReview moves the run's review and announces it. s.mu orders the write
// and its event against the run.status events that also carry the review.
func (s *Scheduler) setReview(ctx context.Context, run domain.RunID, actor domain.MemberID, review domain.RunReview, from ...domain.RunReview) (*domain.Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
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
			return nil, fmt.Errorf("%w: run %s review is %q", ErrInvalidTransition, run, fresh.Review)
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
