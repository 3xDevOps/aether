package collab

import (
	"context"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/store"
)

// ApproveNow releases a queued steer request for immediate delivery. The
// approver is recorded in decision metadata, while injection remains
// attributed to the request author's member identity.
func (s *Service) ApproveNow(ctx context.Context, id string, by domain.MemberID, session string, generation uint64) (Result, error) {
	msg, run, err := s.messageActorRun(ctx, id, by)
	if err != nil {
		return Result{}, err
	}
	if msg.Kind != store.RoomMessageSteerRequest || msg.State != store.RoomMessageQueued {
		return Result{Message: msg, Receipt: receiptForState(msg.State)}, nil
	}
	err = s.authorizeDecision(run.ID, by, session, generation)
	if err != nil {
		return Result{}, err
	}
	actor, err := s.cfg.Runs.GetMember(ctx, msg.ActorID)
	if err != nil {
		return Result{}, err
	}
	claimAdmission := func(fn func() error) error {
		return s.cfg.Control.AdmitMember(string(run.ID), by, session, generation, fn)
	}
	return s.deliverResult(ctx, msg, actor, run, true, by, nil, claimAdmission)
}

// Deny atomically settles a queued steer request as denied. The caller must
// prove the current connected controller lease before the store CAS.
func (s *Service) Deny(ctx context.Context, id string, by domain.MemberID, session string, generation uint64) (*store.RoomMessage, error) {
	msg, run, err := s.messageActorRun(ctx, id, by)
	if err != nil {
		return nil, err
	}
	if msg.Kind != store.RoomMessageSteerRequest {
		return msg, nil
	}
	if msg.State != store.RoomMessageQueued && msg.State != store.RoomMessageDenied {
		return msg, nil
	}
	if err := s.authorizeDecision(run.ID, by, session, generation); err != nil {
		return nil, err
	}
	if msg.State == store.RoomMessageDenied {
		if err := s.publishMessage(ctx, msg); err != nil {
			return msg, err
		}
		return msg, nil
	}
	var won bool
	decide := func() error {
		var err error
		won, err = s.cfg.Store.DecideRoomMessage(ctx, id, store.RoomMessageQueued, store.RoomMessageDenied, string(by), s.now())
		if err != nil || !won {
			return err
		}
		msg, err = s.cfg.Store.GetRoomMessage(ctx, id)
		return err
	}
	if err := s.cfg.Control.AdmitMember(string(run.ID), by, session, generation, decide); err != nil {
		return nil, err
	}
	if won {
		if err := s.publishMessage(ctx, msg); err != nil {
			return msg, err
		}
	}
	return msg, nil
}

func (s *Service) messageActorRun(ctx context.Context, id string, actorID domain.MemberID) (*store.RoomMessage, *domain.Run, error) {
	if id == "" || actorID == "" {
		return nil, nil, ErrInvalidRequest
	}
	msg, err := s.cfg.Store.GetRoomMessage(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	run, _, _, err := s.scope(ctx, msg.WorkspaceID, msg.RunID, actorID)
	return msg, run, err
}
