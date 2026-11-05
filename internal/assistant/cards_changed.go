package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/preference"
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

// CardState is the state of the member's card id and its summary as a
// push may show it.
func (s *Service) CardState(ctx context.Context, member domain.MemberID, id string) (string, string, error) {
	c, err := s.Card(ctx, member, id)
	if err != nil {
		return "", "", err
	}
	if s.privateCard(ctx, member, c.AssistantConfirmation) {
		return c.State, privateSummary(c.Method), nil
	}
	return c.State, c.Summary, nil
}

// privateCard reports whether c saves, forgets or sends a saved preference,
// in its parameters or in the text the card shows, which a template
// launch renders from parameters nested in its own. Disabled preferences
// count, and a card found private stays so: disabling a preference while
// its card waits must not let the card's text reach a locked screen.
func (s *Service) privateCard(ctx context.Context, member domain.MemberID, c protocol.AssistantConfirmation) bool {
	if _, ok := privateSummaries[c.Method]; ok {
		return true
	}
	if _, ok := s.privateCards.Load(c.ID); ok {
		return true
	}
	if !s.sendsPreference(ctx, member, c) {
		return false
	}
	s.privateCards.Store(c.ID, true)
	return true
}

// sendsPreference reports whether c's parameters or shown text hold the
// text of any of the member's preferences, enabled or not. A failed read
// counts as holding one.
func (s *Service) sendsPreference(ctx context.Context, member domain.MemberID, c protocol.AssistantConfirmation) bool {
	if s.cfg.Preferences == nil {
		return false
	}
	var sent struct {
		Task    string `json:"task"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(c.Params, &sent)
	words := preference.Words(preference.Normalize(strings.Join([]string{sent.Task, sent.Message, c.Summary, c.Task, c.Reason}, "\n")))
	listed, err := s.cfg.Preferences.List(ctx, member, protocol.PreferenceListParams{IncludeDisabled: true})
	if err != nil {
		return true
	}
	for _, p := range listed.Preferences {
		if preference.Index(words, preference.Words(p.Text)) >= 0 {
			return true
		}
	}
	return false
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
