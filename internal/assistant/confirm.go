package assistant

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/3xDevOps/Aether/internal/acphost"
	"github.com/3xDevOps/Aether/internal/assistant/tools"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

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
	return protocol.AssistantSettings{AutoApprove: st.AutoApprove, SuggestAutoApprove: !st.AutoApprove && !st.NoSuggest && st.Approvals >= suggestAfter && !time.Now().Before(st.SnoozedUntil), SuggestionOff: st.NoSuggest, ShareView: st.ShareView,
		PreferencesEnabled: st.PreferencesEnabled}
}

// approver passes a confirmed call at once when the member turned
// auto-approval on and the call is one it covers, or else raises a card. The
// setting is read from the store on every call, so a change applies to the
// live session. A card from a turn the member may be watching keeps the call
// waiting until it is decided or expires; one from a connector turn parks,
// and the call returns at once. A call that stops waiting leaves its card
// open, and approving it later runs the call on the server (runParked). A
// passed call is in the transcript before it runs; one that cannot be
// recorded does not run.
func (s *Service) approver(m *member) func(context.Context, protocol.AssistantConfirmation) error {
	return func(ctx context.Context, c protocol.AssistantConfirmation) error {
		conv := m.conversation()
		passed, err := s.autoApprove(ctx, conv, m, c)
		if passed {
			return nil
		}
		var answer chan error
		if err == nil {
			origin := m.turnOrigin()
			if answer, err = s.raise(ctx, m, &c, origin); err == nil && answer == nil {
				return &protocol.Error{Code: protocol.CodeDenied, Message: fmt.Sprintf(parkedText, c.ID)}
			}
		}
		if err != nil {
			a := newAction(c)
			a.DecidedAt = time.Now().UTC()
			a.Outcome, a.FinishedAt, a.Error = protocol.AssistantActionFailed, a.DecidedAt, err.Error()
			if nerr := s.noteOutcome(conv, m, a, err.Error()); nerr != nil {
				err = errors.Join(err, fmt.Errorf("record the failed action: %w", nerr))
			}
			return err
		}
		select {
		case err = <-answer:
		case <-ctx.Done():
			if answered, decision := m.confirms.leave(c.ID, answer); answered && decision == nil {
				s.spawn(func() { s.runParked(m, c.ID, nil) })
			}
			return ctx.Err()
		}
		if err != nil {
			return err
		}
		a := newAction(c)
		a.ConfirmationID, a.DecidedAt = c.ID, time.Now().UTC()
		claimed, err := s.claim(context.WithoutCancel(ctx), m, c, a)
		if err == nil && !claimed {
			err = fmt.Errorf("card %s is no longer approved", c.ID)
		}
		if err == nil {
			slog.Info("assistant approval", "member", m.id, "method", c.Method, "action", a.ID, "auto", false)
			a.Outcome, a.Approval = protocol.AssistantActionApproved, "card"
			if err = s.noteAction(ctx, conv, m, a, c.Method+" passed after you approved its card."); err != nil {
				err = fmt.Errorf("record the approval: %w", err)
				a.Outcome, a.Approval, a.Error, a.FinishedAt = protocol.AssistantActionFailed, "", err.Error(), time.Now().UTC()
				s.finishCard(m, a)
			}
		}
		if err != nil {
			return err
		}
		m.actions.hold(conv, a, c.Params, c.Shown)
		return nil
	}
}

// refusal is the outcome of a call that did not pass with err: the member's
// answer, or a failure to read or count the approval.
func refusal(err error) string {
	switch {
	case errors.Is(err, errDeclined):
		return protocol.AssistantActionDeclined
	case errors.Is(err, errExpired):
		return protocol.AssistantActionExpired
	case errors.Is(err, errSessionEnded):
		return protocol.AssistantActionWithdrawn
	}
	return protocol.AssistantActionFailed
}

// countApproval counts a card auto-approval would have passed toward the
// suggestion to turn it on; a decline, expiry or withdrawal of any card
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

// autoApprove passes c, noted in conv, when the member turned auto-approval
// on and c is one it covers. It holds m.settings throughout, so a change to
// the setting falls wholly before or after it.
func (s *Service) autoApprove(ctx, conv context.Context, m *member, c protocol.AssistantConfirmation) (bool, error) {
	m.settings.Lock()
	defer m.settings.Unlock()
	st, err := s.cfg.Store.AssistantSettings(ctx, m.id)
	if err != nil {
		return false, fmt.Errorf("read the auto-approval setting: %w", err)
	}
	if !st.AutoApprove || !autoApprovable(c) || s.carriesPreference(ctx, m.id, c) {
		return false, nil
	}
	if err = ctx.Err(); err != nil {
		return false, err
	}
	a := newAction(c)
	slog.Info("assistant approval", "member", m.id, "method", c.Method, "action", a.ID, "auto", true)
	a.Outcome, a.Approval, a.DecidedAt = protocol.AssistantActionAutoApproved, "auto", a.CreatedAt
	if err = s.noteAction(ctx, conv, m, a, c.Method+" passed without a card: auto-approval is on."); err != nil {
		return false, fmt.Errorf("record the approval: %w", err)
	}
	m.actions.hold(conv, a, c.Params, c.Shown)
	return true, nil
}

func (s *Service) Settings(ctx context.Context, id domain.MemberID) (protocol.AssistantSettings, error) {
	st, err := s.cfg.Store.AssistantSettings(ctx, id)
	if err != nil {
		return protocol.AssistantSettings{}, fmt.Errorf("assistant: %w", err)
	}
	return s.withTemporary(ctx, id, settingsOf(st))
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
	if p.PreferencesEnabled != nil {
		st.PreferencesEnabled = *p.PreferencesEnabled
	}
	if err = s.setTemporary(ctx, id, p.Temporary); err != nil {
		return protocol.AssistantSettings{}, err
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
		return s.withTemporary(ctx, id, settingsOf(st))
	}
	slog.Info("assistant auto-approval changed", "member", id, "auto", st.AutoApprove)
	severity, title, body := "info", "Auto-approval turned off", "Launching, messaging and stopping sessions and saving the environment wait for your approval again."
	if st.AutoApprove {
		severity, title, body = "warning", "Auto-approval turned on", "The assistant launches, messages and stops your sessions and saves the environment without asking. Messaging or stopping another member's session still asks."
	}
	if err = s.note(ctx, m, acphost.Notice{Severity: severity, Title: title, Description: body}); err != nil && !errors.Is(err, ErrRemoving) {
		return protocol.AssistantSettings{}, fmt.Errorf("assistant: %s, but noting it in the transcript failed: %w", strings.ToLower(title), err)
	}
	return s.withTemporary(ctx, id, settingsOf(st))
}

func newAction(c protocol.AssistantConfirmation) protocol.AssistantAction {
	a := protocol.AssistantAction{ID: rand.Text(), Method: c.Method, Summary: c.Summary, OtherOwner: c.OtherOwner, CreatedAt: c.CreatedAt.UTC()}
	// The socket validated the params, and every action method names its
	// target with these fields; env.save has none, and watch.create nests
	// its target.
	_ = json.Unmarshal(c.Params, &a.Target)
	if c.Method == protocol.MethodWatchCreate {
		var p struct {
			Target *protocol.WatchTarget `json:"target"`
		}
		_ = json.Unmarshal(c.Params, &p)
		a.Target.Watch = p.Target
	}
	var keyed struct {
		IdempotencyKey string `json:"idempotency_key"`
	}
	_ = json.Unmarshal(c.Params, &keyed)
	a.IdempotencyKey = keyed.IdempotencyKey
	return a
}

// noteAction records a in the conversation conv as a notice that clients
// without action records still show. A record of the call's approval also
// needs ctx, the call's, live; an outcome is recorded whatever ctx.
func (s *Service) noteAction(ctx, conv context.Context, m *member, a protocol.AssistantAction, body string) error {
	severity, verb := "info", map[string]string{
		protocol.AssistantActionApproved: "Approved", protocol.AssistantActionAutoApproved: "Auto-approved",
		protocol.AssistantActionDeclined: "Declined", protocol.AssistantActionExpired: "Expired",
		protocol.AssistantActionWithdrawn: "Withdrawn", protocol.AssistantActionFailed: "Failed",
		protocol.AssistantActionSucceeded: "Done",
	}[a.Outcome]
	switch a.Outcome {
	case protocol.AssistantActionFailed:
		severity = "error"
	case protocol.AssistantActionExpired:
		severity = "warning"
	}
	return s.noteIn(ctx, conv, m, acphost.Notice{Severity: severity, Title: verb + ": " + a.Summary, Description: body, Action: &a})
}

var errConversationEnded = errors.New("a reset discarded the conversation")

func (m *member) conversation() context.Context {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.conv
}

// noteIn is note confined to the conversation conv: once a reset discarded
// conv it writes nothing and returns errConversationEnded. The live session
// is read with conv under m.mu, since reset stops it before discarding conv,
// and conv is checked again under m.op, which reset holds.
func (s *Service) noteIn(ctx, conv context.Context, m *member, n acphost.Notice) error {
	m.mu.Lock()
	current, live := m.conv == conv, m.liveLocked()
	m.mu.Unlock()
	if !current {
		return errConversationEnded
	}
	if live != nil {
		if err := live.acp.Record(n); !errors.Is(err, acphost.ErrClosed) {
			return err
		}
	}
	m.op.Lock()
	defer m.op.Unlock()
	if conv.Err() != nil {
		return errConversationEnded
	}
	return s.recordLocked(ctx, m, n)
}

// noteOutcome records a in conv, unless a reset discarded it, and the
// outcome of a card's call on the card.
func (s *Service) noteOutcome(conv context.Context, m *member, a protocol.AssistantAction, body string) error {
	s.finishCard(m, a)
	if err := s.noteAction(conv, conv, m, a, body); !errors.Is(err, errConversationEnded) {
		return err
	}
	return nil
}

type heldAction struct {
	conv   context.Context
	action protocol.AssistantAction
	shown  time.Time
}

// heldActions are the actions whose call was allowed and has not run yet,
// with the conversation that allowed them, by method and exact params: the
// tool socket makes the call with the params it asked to confirm.
type heldActions struct {
	mu   sync.Mutex
	held map[string][]heldAction
}

func (h *heldActions) hold(conv context.Context, a protocol.AssistantAction, params json.RawMessage, shown time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.held == nil {
		h.held = make(map[string][]heldAction)
	}
	key := a.Method + "\x00" + string(params)
	h.held[key] = append(h.held[key], heldAction{conv, a, shown})
}

// take returns the oldest action held for the call, dropping those of a
// discarded conversation: a reset ended their turn.
func (h *heldActions) take(method string, params json.RawMessage) (heldAction, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	key := method + "\x00" + string(params)
	queue := slices.DeleteFunc(h.held[key], func(a heldAction) bool { return a.conv.Err() != nil })
	if len(queue) == 0 {
		delete(h.held, key)
		return heldAction{}, false
	}
	if h.held[key] = queue[1:]; len(queue) == 1 {
		delete(h.held, key)
	}
	return queue[0], true
}

// actionCaller is the tool socket's caller: it records the outcome of each
// action call, after the approval approver recorded for it, or as the only
// record of a reversible call.
type actionCaller struct {
	tools.Caller
	s *Service
	m *member
}

func (c actionCaller) Call(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, *protocol.Error) {
	held, ok := c.m.actions.take(method, params)
	if !ok && !tools.Reversible(method) {
		return c.Caller.Call(ctx, method, params)
	}
	conv, a := held.conv, held.action
	if !ok {
		conv = c.m.conversation()
		a = newAction(protocol.AssistantConfirmation{Method: method, Params: params, CreatedAt: time.Now()})
		var owner string
		var rpcErr *protocol.Error
		if a.Target.RunID != "" {
			owner, rpcErr = tools.OwnerName(ctx, c.Caller, string(c.m.id), a.Target.RunID)
		}
		a.OtherOwner = owner
		a.Summary = reversibleSummary(method, params, a.Target.RunID, owner)
		if rpcErr != nil {
			rpcErr = &protocol.Error{Code: rpcErr.Code, Message: "look up the owner of session " + a.Target.RunID + ": " + rpcErr.Message}
			c.record(conv, method, a, nil, rpcErr)
			return nil, rpcErr
		}
	}
	result, rpcErr := c.Caller.Call(ctx, method, params)
	c.record(conv, method, a, result, rpcErr)
	return result, rpcErr
}

// record notes the outcome of a's call in conv.
func (c actionCaller) record(conv context.Context, method string, a protocol.AssistantAction, result json.RawMessage, rpcErr *protocol.Error) {
	a.FinishedAt = time.Now().UTC()
	body := ""
	if rpcErr != nil {
		a.Outcome, a.Error, body = protocol.AssistantActionFailed, rpcErr.Message, rpcErr.Message
	} else {
		a.Outcome, a.Result = protocol.AssistantActionSucceeded, created(result, a.Target)
		if a.Result != nil {
			body = "Session " + a.Result.RunID
		}
	}
	// A record that cannot be written must not change the call's result.
	if err := c.s.noteOutcome(conv, c.m, a, body); err != nil {
		slog.Warn("assistant: record the action's outcome", "member", c.m.id, "method", method, "action", a.ID, "error", err)
	}
}

// created names the run a call's result holds that its target does not:
// the run it launched.
func created(result json.RawMessage, target protocol.AssistantActionTarget) *protocol.AssistantActionTarget {
	var ids struct {
		Run *protocol.Run `json:"run"`
	}
	if json.Unmarshal(result, &ids) != nil || ids.Run == nil || ids.Run.ID == target.RunID {
		return nil
	}
	return &protocol.AssistantActionTarget{RunID: ids.Run.ID, WorkspaceID: ids.Run.WorkspaceID}
}

const unreadableWatch = "Watch (unreadable parameters)"

func reversibleSummary(method string, params json.RawMessage, run, owner string) string {
	switch method {
	case protocol.MethodWatchCreate:
		p, err := protocol.DecodeParams[protocol.WatchCreateParams](params)
		if err != nil {
			return unreadableWatch
		}
		t := p.Target
		var target string
		switch {
		case t.Kind == protocol.WatchTargetRun && t.RunID != "":
			target = "session " + t.RunID
		case t.Kind == protocol.WatchTargetPR && t.Repo != "":
			target = fmt.Sprintf("pull request %s#%d", t.Repo, t.Number)
		default:
			raw, _ := json.Marshal(t)
			target = "target " + string(raw)
		}
		conditions := "its default conditions"
		if len(p.Conditions) > 0 {
			conditions = strings.Join(p.Conditions, ", ")
		}
		return fmt.Sprintf("Watch %s for %s", target, conditions)
	case protocol.MethodWatchUpdate:
		p, err := protocol.DecodeParams[protocol.WatchUpdateParams](params)
		if err != nil || p.WatchID == "" {
			return unreadableWatch
		}
		switch {
		case p.Paused == nil || len(p.Conditions) > 0 || p.ExpiresAt != "" || p.Label != nil:
			return "Change watch " + p.WatchID
		case *p.Paused:
			return "Pause watch " + p.WatchID
		}
		return "Resume watch " + p.WatchID
	}
	verb := "Pause"
	if method == protocol.MethodRunResume {
		verb = "Resume"
	}
	if owner != "" {
		return fmt.Sprintf("%s session %s, owned by %s", verb, run, owner)
	}
	return verb + " session " + run
}
