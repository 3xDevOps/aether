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

// confirm shows c as a card, filling its id and times, and waits for the
// member's answer.
func (r *confirmations) confirm(ctx context.Context, c *protocol.AssistantConfirmation) error {
	c.ID = rand.Text()
	c.CreatedAt = r.now().UTC()
	c.ExpiresAt = c.CreatedAt.Add(r.ttl)
	p := &pendingConfirmation{AssistantConfirmation: *c, answer: make(chan error, 1)}
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
// runs; one that cannot be recorded does not run. A card that does not pass
// is recorded with its outcome.
func (s *Service) approver(m *member) func(context.Context, protocol.AssistantConfirmation) error {
	return func(ctx context.Context, c protocol.AssistantConfirmation) error {
		passed, err := s.autoApprove(ctx, m, c)
		if err != nil || passed {
			return err
		}
		err = m.confirms.confirm(ctx, &c)
		a := newAction(c)
		a.ConfirmationID, a.DecidedAt = c.ID, time.Now().UTC()
		// A withdrawn card ends with the caller's context done; its reset
		// and record must still be stored.
		if cerr := s.countApproval(context.WithoutCancel(ctx), m, c, err == nil); cerr != nil {
			return errors.Join(err, fmt.Errorf("count the approval: %w", cerr))
		}
		if err != nil {
			a.Outcome, a.FinishedAt = refusal(err), a.DecidedAt
			if a.Outcome == protocol.AssistantActionFailed {
				a.Error = err.Error()
			}
			if nerr := s.noteAction(context.WithoutCancel(ctx), m, a, err.Error()); nerr != nil {
				return errors.Join(err, fmt.Errorf("record the %s card: %w", a.Outcome, nerr))
			}
			return err
		}
		slog.Info("assistant approval", "member", m.id, "method", c.Method, "summary", c.Summary, "auto", false)
		a.Outcome, a.Approval = protocol.AssistantActionApproved, "card"
		if err = s.noteAction(ctx, m, a, c.Method+" passed after you approved its card."); err != nil {
			return fmt.Errorf("record the approval: %w", err)
		}
		m.actions.hold(a, c.Params)
		return nil
	}
}

// refusal is the outcome of a card that did not pass with err.
func refusal(err error) string {
	switch {
	case errors.Is(err, errDeclined):
		return protocol.AssistantActionDeclined
	case errors.Is(err, errExpired):
		return protocol.AssistantActionExpired
	case errors.Is(err, errSessionEnded), errors.Is(err, context.Canceled):
		return protocol.AssistantActionWithdrawn
	}
	return protocol.AssistantActionFailed
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
	a := newAction(c)
	a.Outcome, a.Approval, a.DecidedAt = protocol.AssistantActionAutoApproved, "auto", a.CreatedAt
	if err = s.noteAction(ctx, m, a, c.Method+" passed without a card: auto-approval is on."); err != nil {
		return false, fmt.Errorf("record the approval: %w", err)
	}
	m.actions.hold(a, c.Params)
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
	if err = s.note(ctx, m, acphost.Notice{Severity: severity, Title: title, Description: body}); err != nil && !errors.Is(err, ErrRemoving) {
		return protocol.AssistantSettings{}, fmt.Errorf("assistant: %s, but noting it in the transcript failed: %w", strings.ToLower(title), err)
	}
	return settingsOf(st), nil
}

// reversibleMethods are the calls that run without a card and are recorded
// as actions; every call that passed approver is recorded too.
var reversibleMethods = []string{protocol.MethodRunPause, protocol.MethodRunResume}

func newAction(c protocol.AssistantConfirmation) protocol.AssistantAction {
	a := protocol.AssistantAction{ID: rand.Text(), Method: c.Method, Summary: c.Summary, OtherOwner: c.OtherOwner, CreatedAt: c.CreatedAt.UTC()}
	// The socket validated the params, and every action method names its
	// target with these fields; env.save has none.
	_ = json.Unmarshal(c.Params, &a.Target)
	return a
}

// noteAction records a in the transcript as a notice that clients without
// action records still show.
func (s *Service) noteAction(ctx context.Context, m *member, a protocol.AssistantAction, body string) error {
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
	return s.note(ctx, m, acphost.Notice{Severity: severity, Title: verb + ": " + a.Summary, Description: body, Action: &a})
}

// heldActions are the actions whose call was allowed and has not run yet,
// by method and exact params: the tool socket makes the call with the params
// it asked to confirm.
type heldActions struct {
	mu   sync.Mutex
	held map[string][]protocol.AssistantAction
}

func (h *heldActions) hold(a protocol.AssistantAction, params json.RawMessage) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.held == nil {
		h.held = make(map[string][]protocol.AssistantAction)
	}
	key := a.Method + "\x00" + string(params)
	h.held[key] = append(h.held[key], a)
}

func (h *heldActions) take(method string, params json.RawMessage) (protocol.AssistantAction, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	key := method + "\x00" + string(params)
	queue := h.held[key]
	if len(queue) == 0 {
		return protocol.AssistantAction{}, false
	}
	if len(queue) == 1 {
		delete(h.held, key)
	} else {
		h.held[key] = queue[1:]
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
	a, ok := c.m.actions.take(method, params)
	if !ok && !slices.Contains(reversibleMethods, method) {
		return c.Caller.Call(ctx, method, params)
	}
	if !ok {
		a = newAction(protocol.AssistantConfirmation{Method: method, Params: params, CreatedAt: time.Now()})
		a.Summary = reversibleSummary(method, a.Target.RunID)
	}
	result, rpcErr := c.Caller.Call(ctx, method, params)
	a.FinishedAt = time.Now().UTC()
	body := ""
	if rpcErr != nil {
		a.Outcome, a.Error, body = protocol.AssistantActionFailed, rpcErr.Message, rpcErr.Message
	} else {
		a.Outcome, a.Result = protocol.AssistantActionSucceeded, created(result, a.Target)
		switch {
		case a.Result == nil:
		case a.Result.RunID != "":
			body = "Run " + a.Result.RunID
		default:
			body = "Swarm " + a.Result.MissionID
		}
	}
	// The call already ran, so a record that cannot be written must not
	// turn its result into an error.
	if err := c.s.noteAction(context.WithoutCancel(ctx), c.m, a, body); err != nil {
		slog.Warn("assistant: record the action's outcome", "member", c.m.id, "method", method, "action", a.ID, "error", err)
	}
	return result, rpcErr
}

// created names the run or swarm a call's result holds that its target does
// not: what the call created.
func created(result json.RawMessage, target protocol.AssistantActionTarget) *protocol.AssistantActionTarget {
	var ids struct {
		Run       *protocol.Run `json:"run"`
		RunID     string        `json:"run_id"`
		MissionID string        `json:"mission_id"`
	}
	if json.Unmarshal(result, &ids) != nil {
		return nil
	}
	out := protocol.AssistantActionTarget{RunID: ids.RunID, MissionID: ids.MissionID}
	if ids.Run != nil {
		out.RunID, out.WorkspaceID = ids.Run.ID, ids.Run.WorkspaceID
	}
	if out.RunID == target.RunID {
		out.RunID = ""
	}
	if out.MissionID == target.MissionID {
		out.MissionID = ""
	}
	if out.RunID == "" && out.MissionID == "" {
		return nil
	}
	return &out
}

func reversibleSummary(method, run string) string {
	if method == protocol.MethodRunResume {
		return "Resume run " + run
	}
	return "Pause run " + run
}
