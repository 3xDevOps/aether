package assistant

import (
	"cmp"
	"context"
	"slices"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

func (m *memStore) AddAssistantCard(_ context.Context, c *store.AssistantCard) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	c.UpdatedAt = time.Now()
	m.cards = append(m.cards, *c)
	return nil
}

func (m *memStore) AssistantCard(_ context.Context, member domain.MemberID, id string) (*store.AssistantCard, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range m.cards {
		if c.MemberID == member && c.ID == id {
			return &c, nil
		}
	}
	return nil, store.ErrNotFound
}

func (m *memStore) AssistantCards(_ context.Context, member domain.MemberID, states ...string) ([]store.AssistantCard, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []store.AssistantCard
	for _, c := range m.cards {
		if (member == "" || c.MemberID == member) && (len(states) == 0 || slices.Contains(states, c.State)) {
			out = append(out, c)
		}
	}
	return out, nil
}

func (m *memStore) MoveAssistantCard(_ context.Context, member domain.MemberID, id string, from []string, move store.AssistantCardMove) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, c := range m.cards {
		if c.MemberID != member || c.ID != id || !slices.Contains(from, c.State) {
			continue
		}
		c.State = move.State
		if move.ActionID != "" {
			c.ActionID = move.ActionID
		}
		if move.Result != "" {
			c.Result = move.Result
		}
		if move.Error != "" {
			c.Error = move.Error
		}
		if move.Decided {
			c.DecidedAt = time.Now()
		}
		m.cards[i] = c
		return true, nil
	}
	return false, nil
}

func (m *memStore) EnqueueAssistantPrompt(_ context.Context, e *store.AssistantQueueEntry, limit int) (store.AssistantQueueEntry, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	queued := 0
	for _, held := range m.queue {
		if held.MemberID != e.MemberID || held.Generation != e.Generation {
			continue
		}
		if held.Key == e.Key && (held.State != protocol.AssistantQueueFailed || held.Sent) {
			return held, false, nil
		}
		if held.State == protocol.AssistantQueueQueued {
			queued++
		}
	}
	if queued >= limit {
		return store.AssistantQueueEntry{}, false, store.ErrAssistantQueueFull
	}
	e.Seq, e.State, e.CreatedAt = int64(len(m.queue)+1), protocol.AssistantQueueQueued, time.Now()
	m.queue = append(m.queue, *e)
	return *e, true, nil
}

func (m *memStore) AssistantQueueEntry(_ context.Context, member domain.MemberID, id string) (*store.AssistantQueueEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, e := range m.queue {
		if e.MemberID == member && e.ID == id {
			return &e, nil
		}
	}
	return nil, store.ErrNotFound
}

func (m *memStore) AssistantQueue(_ context.Context, member domain.MemberID, states ...string) ([]store.AssistantQueueEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []store.AssistantQueueEntry
	for _, e := range m.queue {
		if (member == "" || e.MemberID == member) && (len(states) == 0 || slices.Contains(states, e.State)) {
			out = append(out, e)
		}
	}
	return out, nil
}

func (m *memStore) MoveAssistantQueueEntry(_ context.Context, member domain.MemberID, id string, from []string, move store.AssistantQueueMove) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, e := range m.queue {
		if e.MemberID != member || e.ID != id || !slices.Contains(from, e.State) {
			continue
		}
		e.State = move.State
		e.Error, e.Reply, e.MessageID = cmp.Or(move.Error, e.Error), cmp.Or(move.Reply, e.Reply), cmp.Or(move.MessageID, e.MessageID)
		e.Sent = e.Sent || move.Sent
		m.queue[i] = e
		return true, nil
	}
	return false, nil
}

func (m *memStore) AddAssistantQueueCard(_ context.Context, member domain.MemberID, id, card string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, e := range m.queue {
		if e.MemberID == member && e.ID == id {
			m.queue[i].Cards = append(slices.Clone(e.Cards), card)
		}
	}
	return nil
}

func (m *memStore) DeleteAssistantQueue(_ context.Context, member domain.MemberID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.queue = slices.DeleteFunc(m.queue, func(e store.AssistantQueueEntry) bool { return e.MemberID == member })
	return nil
}

// pendingCards waits until m has a pending card and returns those it has.
func pendingCards(t *testing.T, svc *Service, m *member) []protocol.AssistantConfirmation {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		cards, err := svc.openCards(t.Context(), m.id)
		if err != nil {
			t.Fatal(err)
		}
		cards = slices.DeleteFunc(cards, func(c protocol.AssistantConfirmation) bool { return c.State != protocol.AssistantCardPending })
		if len(cards) > 0 {
			return cards
		}
		if time.Now().After(deadline) {
			t.Fatal("no card was shown")
		}
		time.Sleep(time.Millisecond)
	}
}

// decideFirst waits for m's first pending card and decides it.
func decideFirst(t *testing.T, svc *Service, m *member, approve bool) {
	t.Helper()
	if err := svc.Confirm(t.Context(), m.id, pendingCards(t, svc, m)[0].ID, approve); err != nil {
		t.Fatal(err)
	}
}

func pendingCount(t *testing.T, svc *Service, m *member) int {
	t.Helper()
	rows, err := svc.cfg.Store.AssistantCards(t.Context(), m.id, protocol.AssistantCardPending)
	if err != nil {
		t.Fatal(err)
	}
	return len(rows)
}

func promptErr(_ Entry, err error) error { return err }
