package assistant

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// PendingCards is the member's cards that wait for a decision, oldest
// first.
func (s *Service) PendingCards(ctx context.Context, member domain.MemberID) ([]protocol.AssistantConfirmation, error) {
	rows, err := s.cfg.Store.AssistantCards(ctx, member, protocol.AssistantCardPending)
	if err != nil {
		return nil, fmt.Errorf("assistant: list the pending cards: %w", err)
	}
	out := make([]protocol.AssistantConfirmation, 0, len(rows))
	for _, row := range rows {
		c, err := cardOf(row)
		if err != nil {
			return nil, err
		}
		out = append(out, c.AssistantConfirmation)
	}
	return out, nil
}

// cardsChanged tells the bus member's cards changed, for their devices'
// notifications. It runs under the card's locks, so it publishes apart.
func (s *Service) cardsChanged(member domain.MemberID) {
	if s.cfg.Bus == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := s.cfg.Bus.Publish(ctx, events.Event{
			WorkspaceID: events.ServerScope, ActorID: member, Payload: events.AssistantCardsPayload{MemberID: member},
		}); err != nil {
			slog.Warn("assistant: announce a card change", "member", member, "error", err)
		}
	}()
}
