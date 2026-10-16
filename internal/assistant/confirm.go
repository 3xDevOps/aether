package assistant

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

const confirmTTL = 10 * time.Minute

var (
	// ErrNoConfirmation means no pending confirmation has the id: it was
	// answered, expired or discarded with its session.
	ErrNoConfirmation = errors.New("assistant: no such pending confirmation")

	errDeclined     = errors.New("the member declined this action")
	errExpired      = fmt.Errorf("the member did not answer within %s", confirmTTL)
	errSessionEnded = errors.New("the turn or session ended before the member answered")
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

// approver passes a confirmed call at once when the member turned
// auto-approval on, or else when they approve its card. The setting is read
// from the store on every call, so a change applies to the live session. A
// passed call is in the transcript before it runs; one that cannot be
// recorded does not run.
func (s *Service) approver(m *member) func(context.Context, protocol.AssistantConfirmation) error {
	return func(ctx context.Context, c protocol.AssistantConfirmation) error {
		passed, err := s.autoApprove(ctx, m, c)
		if err != nil || passed {
			return err
		}
		if err = m.confirms.confirm(ctx, c); err != nil {
			return err
		}
		slog.Info("assistant approval", "member", m.id, "method", c.Method, "summary", c.Summary, "auto", false)
		if err = s.note(ctx, m, "info", "Approved: "+c.Summary, c.Method+" passed after you approved its card."); err != nil {
			return fmt.Errorf("record the approval: %w", err)
		}
		return nil
	}
}

// autoApprove passes c, noted in the transcript, when the member turned
// auto-approval on. It holds m.settings throughout, so a change to the
// setting falls wholly before or after it.
func (s *Service) autoApprove(ctx context.Context, m *member, c protocol.AssistantConfirmation) (bool, error) {
	m.settings.Lock()
	defer m.settings.Unlock()
	auto, err := s.cfg.Store.AssistantAutoApprove(ctx, m.id)
	if err != nil {
		return false, fmt.Errorf("read the auto-approval setting: %w", err)
	}
	if !auto {
		return false, nil
	}
	if err = ctx.Err(); err != nil {
		return false, err
	}
	slog.Info("assistant approval", "member", m.id, "method", c.Method, "summary", c.Summary, "auto", true)
	if err = s.note(ctx, m, "info", "Auto-approved: "+c.Summary, c.Method+" passed without a card: auto-approval is on."); err != nil {
		return false, fmt.Errorf("record the approval: %w", err)
	}
	return true, nil
}

func (s *Service) Settings(ctx context.Context, id domain.MemberID) (protocol.AssistantSettings, error) {
	auto, err := s.cfg.Store.AssistantAutoApprove(ctx, id)
	if err != nil {
		return protocol.AssistantSettings{}, fmt.Errorf("assistant: %w", err)
	}
	share, err := s.cfg.Store.AssistantShareView(ctx, id)
	if err != nil {
		return protocol.AssistantSettings{}, fmt.Errorf("assistant: %w", err)
	}
	return protocol.AssistantSettings{AutoApprove: auto, ShareView: share}, nil
}

// SetAutoApprove stores the member's choice and notes a change in their
// transcript, under m.settings so it orders with the calls autoApprove
// passes. Cards already open stay open.
func (s *Service) SetAutoApprove(ctx context.Context, id domain.MemberID, on bool) (protocol.AssistantSettings, error) {
	m := s.member(id)
	defer m.notify()
	m.settings.Lock()
	defer m.settings.Unlock()
	was, err := s.cfg.Store.AssistantAutoApprove(ctx, id)
	if err != nil {
		return protocol.AssistantSettings{}, fmt.Errorf("assistant: %w", err)
	}
	if err = s.cfg.Store.SetAssistantAutoApprove(ctx, id, on); err != nil {
		return protocol.AssistantSettings{}, fmt.Errorf("assistant: %w", err)
	}
	if was == on {
		return protocol.AssistantSettings{AutoApprove: on}, nil
	}
	slog.Info("assistant auto-approval changed", "member", id, "auto", on)
	severity, title, body := "info", "Auto-approval turned off", "Launching, messaging and stopping runs and saving the environment wait for your approval again."
	if on {
		severity, title, body = "warning", "Auto-approval turned on", "The assistant launches, messages and stops runs and saves the environment without asking."
	}
	if err = s.note(ctx, m, severity, title, body); err != nil && !errors.Is(err, ErrRemoving) {
		return protocol.AssistantSettings{}, fmt.Errorf("assistant: %s, but noting it in the transcript failed: %w", strings.ToLower(title), err)
	}
	return protocol.AssistantSettings{AutoApprove: on}, nil
}
