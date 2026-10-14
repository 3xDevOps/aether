package assistant

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/3xDevOps/Aether/internal/protocol"
)

// confirmTTL is how long a confirmation waits for the member.
const confirmTTL = 10 * time.Minute

var (
	// ErrNoConfirmation means no pending confirmation has the id: it was
	// answered, expired or discarded with its session.
	ErrNoConfirmation = errors.New("assistant: no such pending confirmation")

	errDeclined     = errors.New("the member declined this action")
	errExpired      = fmt.Errorf("the member did not answer within %s", confirmTTL)
	errSessionEnded = errors.New("the assistant session ended before the member answered")
)

type pendingConfirmation struct {
	protocol.AssistantConfirmation
	answer chan error
}

// confirmations holds one member's pending confirmations. changed runs after
// the set changes and must not block.
type confirmations struct {
	now     func() time.Time
	ttl     time.Duration
	changed func()

	mu      sync.Mutex
	pending []*pendingConfirmation
}

func newConfirmations(changed func()) *confirmations {
	return &confirmations{now: time.Now, ttl: confirmTTL, changed: changed}
}

// confirm publishes c and blocks until the member decides, the confirmation
// expires, ctx ends or cancelAll discards it. A nil return is approval.
func (r *confirmations) confirm(ctx context.Context, c protocol.AssistantConfirmation) error {
	c.ID = rand.Text()
	c.CreatedAt = r.now().UTC()
	c.ExpiresAt = c.CreatedAt.Add(r.ttl)
	p := &pendingConfirmation{AssistantConfirmation: c, answer: make(chan error, 1)}
	r.mu.Lock()
	r.pending = append(r.pending, p)
	r.mu.Unlock()
	r.changed()
	defer r.remove(p)

	timer := time.NewTimer(r.ttl)
	defer timer.Stop()
	select {
	case err := <-p.answer:
		return err
	case <-timer.C:
		return errExpired
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *confirmations) remove(p *pendingConfirmation) {
	r.mu.Lock()
	i := slices.Index(r.pending, p)
	if i >= 0 {
		r.pending = slices.Delete(r.pending, i, i+1)
	}
	r.mu.Unlock()
	if i >= 0 {
		r.changed()
	}
}

// decide answers the pending confirmation id.
func (r *confirmations) decide(id string, approve bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, p := range r.pending {
		if p.ID != id {
			continue
		}
		var answer error
		if !approve {
			answer = errDeclined
		}
		select {
		case p.answer <- answer:
			return nil
		default:
		}
	}
	return fmt.Errorf("%w: %s", ErrNoConfirmation, id)
}

// cancelAll fails every pending confirmation with errSessionEnded.
func (r *confirmations) cancelAll() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, p := range r.pending {
		select {
		case p.answer <- errSessionEnded:
		default:
		}
	}
}

func (r *confirmations) snapshot() []protocol.AssistantConfirmation {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]protocol.AssistantConfirmation, len(r.pending))
	for i, p := range r.pending {
		out[i] = p.AssistantConfirmation
	}
	return out
}
