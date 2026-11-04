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
		if s.privateCard(ctx, member, c.AssistantConfirmation) {
			card.Summary, card.Private = privateSummary(c.Method), true
		}
		out = append(out, card)
	}
	return out, nil
}

// privateSummaries is what a notification shows for a card that saves or
// forgets a preference; the desktop app keeps the same words.
var privateSummaries = map[string]string{
	protocol.MethodAssistantRemember: "Save a preference",
	protocol.MethodAssistantForget:   "Forget a saved preference",
}

func privateSummary(method string) string {
	if summary, ok := privateSummaries[method]; ok {
		return summary
	}
	return "An action that uses a saved preference"
}

// CardOutcome is what became of the member's card id, with its summary as
// a push may show it. Which door decided it is not recorded.
func (s *Service) CardOutcome(ctx context.Context, member domain.MemberID, id string) (string, error) {
	c, err := s.Card(ctx, member, id)
	if err != nil {
		return "", err
	}
	summary := c.Summary
	if s.privateCard(ctx, member, c.AssistantConfirmation) {
		summary = privateSummary(c.Method)
	}
	switch c.State {
	case protocol.AssistantCardApproved, protocol.AssistantCardRunning, protocol.AssistantCardSucceeded, protocol.AssistantCardFailed:
		return "Approved: " + summary, nil
	case protocol.AssistantCardDeclined:
		return "Declined: " + summary, nil
	case protocol.AssistantCardExpired:
		return "Expired: " + summary, nil
	}
	return "Withdrawn: " + summary, nil
}

// privateCard reports whether c saves, forgets or sends a saved preference.
func (s *Service) privateCard(ctx context.Context, member domain.MemberID, c protocol.AssistantConfirmation) bool {
	_, ok := privateSummaries[c.Method]
	return ok || s.carriesPreference(ctx, member, c)
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
