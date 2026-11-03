package assistant

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/push"
)

// PendingCards is the member's cards that wait for a decision, oldest
// first, as a push may show them: a card that saves or forgets a
// preference, or sends a saved preference's text, reads only what it does,
// since the push leaves the server and shows on a locked screen.
func (s *Service) PendingCards(ctx context.Context, member domain.MemberID) ([]push.Card, error) {
	rows, err := s.cfg.Store.AssistantCards(ctx, member, protocol.AssistantCardPending)
	if err != nil {
		return nil, fmt.Errorf("assistant: list the pending cards: %w", err)
	}
	out := make([]push.Card, 0, len(rows))
	for _, row := range rows {
		c, err := cardOf(row)
		if err != nil {
			return nil, err
		}
		card := push.Card{ID: c.ID, Summary: c.Summary, CreatedAt: c.CreatedAt}
		switch {
		case c.Method == protocol.MethodAssistantRemember:
			card.Summary, card.Private = "Save a preference", true
		case c.Method == protocol.MethodAssistantForget:
			card.Summary, card.Private = "Forget a saved preference", true
		case s.carriesPreference(ctx, member, c.AssistantConfirmation):
			card.Summary, card.Private = "An action that uses a saved preference", true
		}
		out = append(out, card)
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
