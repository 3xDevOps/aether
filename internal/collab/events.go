package collab

import (
	"context"
	"fmt"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/store"
)

func (s *Service) publishProtected(ctx context.Context, run *domain.Run, actor domain.MemberID) error {
	if s.cfg.Bus == nil {
		return nil
	}
	_, err := s.cfg.Bus.Publish(ctx, events.Event{
		WorkspaceID: run.WorkspaceID,
		RunID:       run.ID,
		ActorID:     actor,
		Payload:     events.RunProtectedPayload{Protected: true},
	})
	if err != nil {
		return fmt.Errorf("collab: publish protection event for run %q: %w", run.ID, err)
	}
	return nil
}

func (s *Service) publishMessage(ctx context.Context, msg *store.RoomMessage) error {
	if s.cfg.Bus == nil {
		return nil
	}
	var deliverAfter, decidedAt *string
	if msg.DeliverAfter != nil {
		v := msg.DeliverAfter.UTC().Format(time.RFC3339Nano)
		deliverAfter = &v
	}
	if msg.DecidedAt != nil {
		v := msg.DecidedAt.UTC().Format(time.RFC3339Nano)
		decidedAt = &v
	}
	_, err := s.cfg.Bus.Publish(ctx, events.Event{WorkspaceID: msg.WorkspaceID, RunID: msg.RunID, ActorID: msg.ActorID, Payload: events.RoomMessagePayload{MessageID: msg.ID, WorkspaceID: msg.WorkspaceID, RunID: msg.RunID, ActorID: msg.ActorID, Kind: string(msg.Kind), State: string(msg.State), DeliverAfter: deliverAfter, DecidedBy: msg.DecidedBy, DecidedAt: decidedAt, CorrelationID: msg.CorrelationID, AttachmentCount: len(msg.Attachments), HasAnchor: msg.Anchor != nil}})
	if err != nil {
		return fmt.Errorf("collab: publish room message %q in state %q: %w", msg.ID, msg.State, err)
	}
	return nil
}
