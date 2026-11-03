package assistant

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/harness"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

// ErrNoRule means a card offers no rule to save.
var ErrNoRule = errors.New("assistant: this card offers no rule; approve or decline it")

// ErrNoSuchRule means the member has no rule with the id.
var ErrNoSuchRule = errors.New("assistant: no such approval rule")

// approveByRule passes c, noted in conv, when one of the member's active
// rules has c's key; otherwise it sets c.Rule to the rule c offers, if any.
// Like autoApprove it holds m.settings throughout, so a revoke falls wholly
// before or after it.
func (s *Service) approveByRule(ctx, conv context.Context, m *member, c *protocol.AssistantConfirmation) (bool, error) {
	key, ok := s.ruleKey(ctx, m, *c)
	if !ok {
		return false, nil
	}
	m.settings.Lock()
	defer m.settings.Unlock()
	rules, err := s.cfg.Store.AssistantRules(ctx, m.id)
	if err != nil {
		return false, fmt.Errorf("read the approval rules: %w", err)
	}
	if i := slices.IndexFunc(rules, func(r store.AssistantRule) bool { return r.Key == store.AssistantRuleKey(key) }); i >= 0 {
		r := rules[i]
		inactive, ierr := s.inactive(ctx, m, r)
		if ierr != nil {
			slog.Warn("assistant: check the approval rule; showing the card", "member", m.id, "rule", r.ID, "error", ierr)
		}
		if ierr == nil && inactive == "" {
			return true, s.passByRule(ctx, conv, m, *c, r)
		}
	}
	st, err := s.cfg.Store.AssistantSettings(ctx, m.id)
	if err != nil {
		return false, fmt.Errorf("read the auto-approval setting: %w", err)
	}
	wording, err := s.ruleWording(ctx, m, key)
	if err != nil {
		slog.Warn("assistant: word the card's approval rule", "member", m.id, "method", c.Method, "error", err)
		return false, nil
	}
	offer := &protocol.AssistantRuleOffer{Key: key, Wording: wording}
	offer.Suggested = st.RuleStreak >= suggestAfter && st.RuleStreakKey == ruleStreakKey(offer)
	c.Rule = offer
	return false, nil
}

func (s *Service) passByRule(ctx, conv context.Context, m *member, c protocol.AssistantConfirmation, r store.AssistantRule) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.cfg.Store.UseAssistantRule(ctx, m.id, r.ID); err != nil {
		return fmt.Errorf("count the use of approval rule %s: %w", r.ID, err)
	}
	a := newAction(c)
	slog.Info("assistant approval", "member", m.id, "method", c.Method, "action", a.ID, "rule", r.ID)
	a.Outcome, a.Approval, a.RuleID, a.DecidedAt = protocol.AssistantActionAutoApproved, "rule", r.ID, a.CreatedAt
	if err := s.noteAction(ctx, conv, m, a, fmt.Sprintf("%s passed without a card: your rule %q covers it.", c.Method, r.Wording)); err != nil {
		return fmt.Errorf("record the approval: %w", err)
	}
	m.actions.hold(conv, a, c.Params, c.Shown)
	return nil
}

// ruleStreakKey is the nudge's name for the kind of card offer offers, or
// "" for a card that offers no rule.
func ruleStreakKey(offer *protocol.AssistantRuleOffer) string {
	if offer == nil {
		return ""
	}
	b, _ := json.Marshal(offer.Key)
	return string(b)
}

// ruleKey is the key of the rule c may become, built only from c's typed
// params as the tool socket resolved them and from the server's own records:
// never from the task, message, reason or other text the assistant wrote.
// A card on another member's session, one with automatic fallback while an
// account is shared with the member (both OtherMember), one carrying a saved
// preference, and every method auto-approval does not cover is never
// covered by a rule.
func (s *Service) ruleKey(ctx context.Context, m *member, c protocol.AssistantConfirmation) (protocol.AssistantRuleKey, bool) {
	if !autoApprovable(c) {
		return protocol.AssistantRuleKey{}, false
	}
	key := protocol.AssistantRuleKey{Method: c.Method}
	switch c.Method {
	case protocol.MethodEnvSave:
	case protocol.MethodRunLaunch:
		var p protocol.RunLaunchParams
		dec := json.NewDecoder(bytes.NewReader(c.Params))
		dec.DisallowUnknownFields()
		if dec.Decode(&p) != nil || p.WorkspaceID == "" || harness.ValidateName(p.Harness) != nil {
			return key, false
		}
		// The wording quotes these, so only values run.launch accepts reach it.
		if _, err := domain.ParsePermission(p.Permission); err != nil || (p.Mode != "" && !domain.LaunchMode(p.Mode).Valid()) ||
			domain.ValidModel(p.Model) != nil || domain.ValidEffort(p.Effort) != nil {
			return key, false
		}
		// The card shows everything a rule allows: these widen a launch
		// beyond one agent on one account in one workspace.
		if len(p.ExtraWorkspaceIDs) > 0 || len(p.FallbackAccountMemberIDs) > 0 || p.Children != "" || p.Limits != nil || p.CachedBase != "" {
			return key, false
		}
		key.WorkspaceID, key.Agent, key.Mode, key.Model, key.Effort = p.WorkspaceID, p.Harness, p.Mode, p.Model, p.Effort
		key.Permission, key.NoFallback = cmp.Or(p.Permission, "default"), p.NoFallback
		if p.AccountMemberID != string(m.id) {
			key.AccountMemberID = p.AccountMemberID
		}
	case protocol.MethodRunInject, protocol.MethodRunKill:
		id, err := protocol.DecodeParams[protocol.RunIDParams](c.Params)
		if err != nil || id.RunID == "" {
			return key, false
		}
		run, err := callAs(ctx, s, m, protocol.MethodRunGet, id, new(protocol.RunResult))
		if err != nil {
			slog.Warn("assistant: read the card's session for its approval rule", "member", m.id, "run", id.RunID, "error", err)
			return key, false
		}
		if run.Run.MemberID != string(m.id) {
			return key, false
		}
		key.WorkspaceID = run.Run.WorkspaceID
	default:
		return key, false
	}
	return key, !s.carriesPreference(ctx, m.id, c)
}

// callAs makes a call as the member, decoding its result into out.
func callAs[T any](ctx context.Context, s *Service, m *member, method string, params any, out *T) (*T, error) {
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	result, rpcErr := s.cfg.Caller(m.id).Call(ctx, method, raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	if err = json.Unmarshal(result, out); err != nil {
		return nil, fmt.Errorf("decode %s result: %w", method, err)
	}
	return out, nil
}

// ruleWording is the server's sentence for what a rule with key covers,
// which the card's button and the rules list show.
func (s *Service) ruleWording(ctx context.Context, m *member, key protocol.AssistantRuleKey) (string, error) {
	if key.Method == protocol.MethodEnvSave {
		return "Always approve saving your environment as a new image", nil
	}
	ws, err := callAs(ctx, s, m, protocol.MethodWorkspaceGet, protocol.WorkspaceGetParams{WorkspaceID: key.WorkspaceID}, new(protocol.WorkspaceGetResult))
	if err != nil {
		return "", err
	}
	switch key.Method {
	case protocol.MethodRunInject:
		return "Always approve messages to your own sessions in " + ws.Workspace.Name, nil
	case protocol.MethodRunKill:
		return "Always approve stopping your own sessions in " + ws.Workspace.Name, nil
	}
	agent := key.Agent
	if p, ok := harness.Lookup(key.Agent); ok && p.DisplayName != "" {
		agent = p.DisplayName
	}
	how := []string{cmp.Or(key.Mode, "default mode")}
	if key.Model != "" {
		how = append(how, fmt.Sprintf("model %q", key.Model))
	}
	if key.Effort != "" {
		how = append(how, fmt.Sprintf("effort %q", key.Effort))
	}
	account := "your account"
	if key.AccountMemberID != "" {
		owner, err := s.sharedAccount(ctx, m, key.AccountMemberID)
		if err != nil {
			return "", err
		}
		if owner == "" {
			return "", fmt.Errorf("account %s is not shared with you", key.AccountMemberID)
		}
		account = owner + "'s account"
	}
	wording := fmt.Sprintf("Always approve launches of %s (%s) in %s on %s, permission %s", agent, strings.Join(how, ", "), ws.Workspace.Name, account, key.Permission)
	if key.NoFallback {
		wording += ", no fallback"
	}
	return wording, nil
}

// sharedAccount names the owner of account when the member may launch on
// it, or returns "" when they may not.
func (s *Service) sharedAccount(ctx context.Context, m *member, account string) (string, error) {
	accounts, err := callAs(ctx, s, m, protocol.MethodAccountList, struct{}{}, new(protocol.AccountListResult))
	if err != nil {
		return "", err
	}
	for _, a := range accounts.Accounts {
		if a.ID == account {
			return cmp.Or(a.DisplayName, a.ID), nil
		}
	}
	return "", nil
}

// inactive says why r no longer passes cards, or "" while it does: its
// workspace is gone, or its account is no longer shared with the member.
func (s *Service) inactive(ctx context.Context, m *member, r store.AssistantRule) (string, error) {
	if r.Key.WorkspaceID != "" {
		_, err := callAs(ctx, s, m, protocol.MethodWorkspaceGet, protocol.WorkspaceGetParams{WorkspaceID: r.Key.WorkspaceID}, new(protocol.WorkspaceGetResult))
		var rpcErr *protocol.Error
		if errors.As(err, &rpcErr) && (rpcErr.Code == protocol.CodeNotFound || rpcErr.Code == protocol.CodeDenied) {
			return fmt.Sprintf("workspace %s no longer exists or you lost access to it", r.Key.WorkspaceID), nil
		}
		if err != nil {
			return "", err
		}
	}
	if r.Key.AccountMemberID != "" {
		owner, err := s.sharedAccount(ctx, m, r.Key.AccountMemberID)
		if err != nil {
			return "", err
		}
		if owner == "" {
			return fmt.Sprintf("account %s is no longer shared with you", r.Key.AccountMemberID), nil
		}
	}
	return "", nil
}

// ApproveAlways approves the member's card id and saves the rule it
// offers, as the server built it when it showed the card. The rule's key
// and wording come from the stored card; the caller sends only the id.
func (s *Service) ApproveAlways(ctx context.Context, id domain.MemberID, confirmationID string) (protocol.AssistantRule, error) {
	m := s.member(id)
	cards := m.confirms.snapshot()
	i := slices.IndexFunc(cards, func(c protocol.AssistantConfirmation) bool { return c.ID == confirmationID })
	if i < 0 {
		return protocol.AssistantRule{}, fmt.Errorf("%w: %s", ErrNoConfirmation, confirmationID)
	}
	card := cards[i]
	if card.Rule == nil {
		return protocol.AssistantRule{}, ErrNoRule
	}
	if err := m.confirms.decide(confirmationID, true); err != nil {
		return protocol.AssistantRule{}, err
	}
	m.settings.Lock()
	defer m.settings.Unlock()
	r, err := s.cfg.Store.AddAssistantRule(ctx, id, store.AssistantRuleKey(card.Rule.Key), card.Rule.Wording, card.Summary)
	if err != nil {
		return protocol.AssistantRule{}, fmt.Errorf("assistant: approved the card, but saving the rule failed: %w", err)
	}
	// The member answered the nudge: both counts start over.
	st, err := s.cfg.Store.AssistantSettings(ctx, id)
	if err == nil {
		st.Approvals, st.RuleStreakKey, st.RuleStreak = 0, "", 0
		err = s.cfg.Store.PutAssistantSettings(ctx, id, st)
	}
	if err != nil {
		return protocol.AssistantRule{}, fmt.Errorf("assistant: saved rule %s, but resetting the approval counts failed: %w", r.ID, err)
	}
	m.notify()
	slog.Info("assistant approval rule saved", "member", id, "rule", r.ID, "method", r.Key.Method)
	return ruleOf(r, ""), nil
}

// Rules lists the member's approval rules, each with why it no longer
// passes cards when it does not.
func (s *Service) Rules(ctx context.Context, id domain.MemberID) ([]protocol.AssistantRule, error) {
	m := s.member(id)
	rules, err := s.cfg.Store.AssistantRules(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("assistant: %w", err)
	}
	out := make([]protocol.AssistantRule, len(rules))
	for i, r := range rules {
		inactive, err := s.inactive(ctx, m, r)
		if err != nil {
			return nil, fmt.Errorf("assistant: check approval rule %s: %w", r.ID, err)
		}
		out[i] = ruleOf(r, inactive)
	}
	return out, nil
}

// RevokeRule deletes the member's rule ruleID; the next card it would have
// passed asks again.
func (s *Service) RevokeRule(ctx context.Context, id domain.MemberID, ruleID string) error {
	m := s.member(id)
	m.settings.Lock()
	defer m.settings.Unlock()
	if err := s.cfg.Store.DeleteAssistantRule(ctx, id, ruleID); errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("%w: %s", ErrNoSuchRule, ruleID)
	} else if err != nil {
		return fmt.Errorf("assistant: %w", err)
	}
	slog.Info("assistant approval rule revoked", "member", id, "rule", ruleID)
	return nil
}

func ruleOf(r store.AssistantRule, inactive string) protocol.AssistantRule {
	return protocol.AssistantRule{ID: r.ID, Method: r.Key.Method, Wording: r.Wording, Summary: r.Summary, CreatedAt: r.CreatedAt,
		LastUsedAt: r.LastUsedAt, Uses: r.Uses, Inactive: inactive}
}
