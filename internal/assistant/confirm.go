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
	"github.com/3xDevOps/Aether/internal/store"
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

// autoApproved are the methods auto-approval may pass. A confirmed method
// not listed here shows a card whatever the setting.
var autoApproved = []string{protocol.MethodRunLaunch, protocol.MethodRunInject, protocol.MethodRunKill, protocol.MethodEnvSave}

const (
	// suggestAfter is how many cards approved by hand in a row make the
	// dashboard suggest auto-approval.
	suggestAfter = 3
	firstSnooze  = 7 * 24 * time.Hour
	secondSnooze = 30 * 24 * time.Hour
)

// autoApprovable reports whether auto-approval may pass c: a listed method
// that does not act on another member's run.
func autoApprovable(c protocol.AssistantConfirmation) bool {
	return !c.OtherMember && slices.Contains(autoApproved, c.Method)
}

func settingsOf(st store.AssistantSettings) protocol.AssistantSettings {
	return protocol.AssistantSettings{AutoApprove: st.AutoApprove, SuggestAutoApprove: !st.AutoApprove && !st.NoSuggest && st.Approvals >= suggestAfter && !time.Now().Before(st.SnoozedUntil), SuggestionOff: st.NoSuggest, ShareView: st.ShareView}
}

// approver passes a confirmed call at once when the member turned
// auto-approval on and the call is one it covers, or else when they approve
// its card. The setting is read from the store on every call, so a change
// applies to the live session. A passed call is in the transcript before it
// runs; one that cannot be recorded does not run.
func (s *Service) approver(m *member) func(context.Context, protocol.AssistantConfirmation) error {
	return func(ctx context.Context, c protocol.AssistantConfirmation) error {
		passed, err := s.autoApprove(ctx, m, c)
		if err != nil || passed {
			return err
		}
		err = m.confirms.confirm(ctx, c)
		// A withdrawn card ends with the caller's context done; its reset
		// must still be stored.
		if cerr := s.countApproval(context.WithoutCancel(ctx), m, c, err == nil); cerr != nil {
			return errors.Join(err, fmt.Errorf("count the approval: %w", cerr))
		}
		if err != nil {
			return err
		}
		slog.Info("assistant approval", "member", m.id, "method", c.Method, "summary", c.Summary, "auto", false)
		if err = s.note(ctx, m, "info", "Approved: "+c.Summary, c.Method+" passed after you approved its card."); err != nil {
			return fmt.Errorf("record the approval: %w", err)
		}
		return nil
	}
}

// countApproval counts a card auto-approval would have passed toward the
// suggestion to turn it on; a decline, timeout or withdrawal of any card
// starts the count over.
func (s *Service) countApproval(ctx context.Context, m *member, c protocol.AssistantConfirmation, approved bool) error {
	if approved && !autoApprovable(c) {
		return nil
	}
	m.settings.Lock()
	defer m.settings.Unlock()
	st, err := s.cfg.Store.AssistantSettings(ctx, m.id)
	if err != nil {
		return err
	}
	switch {
	case approved:
		st.Approvals++
	case st.Approvals == 0:
		return nil
	default:
		st.Approvals = 0
	}
	if err = s.cfg.Store.PutAssistantSettings(ctx, m.id, st); err != nil {
		return err
	}
	m.notify()
	return nil
}

// autoApprove passes c, noted in the transcript, when the member turned
// auto-approval on and c is one it covers. It holds m.settings throughout,
// so a change to the setting falls wholly before or after it.
func (s *Service) autoApprove(ctx context.Context, m *member, c protocol.AssistantConfirmation) (bool, error) {
	m.settings.Lock()
	defer m.settings.Unlock()
	st, err := s.cfg.Store.AssistantSettings(ctx, m.id)
	if err != nil {
		return false, fmt.Errorf("read the auto-approval setting: %w", err)
	}
	if !st.AutoApprove || !autoApprovable(c) {
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
	st, err := s.cfg.Store.AssistantSettings(ctx, id)
	if err != nil {
		return protocol.AssistantSettings{}, fmt.Errorf("assistant: %w", err)
	}
	return settingsOf(st), nil
}

// SetSettings applies p and notes a change of auto-approval in the member's
// transcript, under m.settings so it orders with the calls autoApprove
// passes. Cards already open stay open. Turning auto-approval on ends the
// suggestion; turning it off after it was on stops the suggestion for good,
// since the member has tried it.
func (s *Service) SetSettings(ctx context.Context, id domain.MemberID, p protocol.AssistantSettingsParams) (protocol.AssistantSettings, error) {
	m := s.member(id)
	defer m.notify()
	m.settings.Lock()
	defer m.settings.Unlock()
	st, err := s.cfg.Store.AssistantSettings(ctx, id)
	if err != nil {
		return protocol.AssistantSettings{}, fmt.Errorf("assistant: %w", err)
	}
	was := st.AutoApprove
	switch p.Suggestion {
	case protocol.AssistantSuggestionLater:
		st.Approvals = 0
		st.Snoozes++
		switch st.Snoozes {
		case 1:
			st.SnoozedUntil = time.Now().Add(firstSnooze).UTC()
		case 2:
			st.SnoozedUntil = time.Now().Add(secondSnooze).UTC()
		default:
			st.NoSuggest = true
		}
	case protocol.AssistantSuggestionNever:
		st.Approvals = 0
		st.NoSuggest = true
	}
	if p.AutoApprove != nil {
		st.AutoApprove = *p.AutoApprove
		if st.AutoApprove {
			st.Approvals = 0
		} else if was {
			st.NoSuggest = true
		}
	}
	if err = s.cfg.Store.PutAssistantSettings(ctx, id, st); err != nil {
		return protocol.AssistantSettings{}, fmt.Errorf("assistant: %w", err)
	}
	if was == st.AutoApprove {
		return settingsOf(st), nil
	}
	slog.Info("assistant auto-approval changed", "member", id, "auto", st.AutoApprove)
	severity, title, body := "info", "Auto-approval turned off", "Launching, messaging and stopping runs and saving the environment wait for your approval again."
	if st.AutoApprove {
		severity, title, body = "warning", "Auto-approval turned on", "The assistant launches, messages and stops your runs and saves the environment without asking. Messaging or stopping another member's run still asks."
	}
	if err = s.note(ctx, m, severity, title, body); err != nil && !errors.Is(err, ErrRemoving) {
		return protocol.AssistantSettings{}, fmt.Errorf("assistant: %s, but noting it in the transcript failed: %w", strings.ToLower(title), err)
	}
	return settingsOf(st), nil
}
