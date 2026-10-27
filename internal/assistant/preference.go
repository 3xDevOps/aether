package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	acp "github.com/coder/acp-go-sdk"
	"golang.org/x/text/cases"

	"github.com/3xDevOps/Aether/internal/acphost"
	"github.com/3xDevOps/Aether/internal/assistant/tools"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/preference"
	"github.com/3xDevOps/Aether/internal/protocol"
)

const (
	// maxSavesPerTurn bounds the preferences one message can save.
	maxSavesPerTurn = 3
	// maxShown and maxShownBytes bound what aether_my_preferences returns.
	maxShown      = 40
	maxShownBytes = 4 << 10
	minQuoteWords = 3
	minQuoteChars = 12
)

// preferenceCaller serves the assistant's preference methods on its tool
// socket from the member's own preferences, and passes every other call on.
// It lives as long as one session, so a session restored after a restart
// saves nothing until the member's next message.
type preferenceCaller struct {
	tools.Caller
	s *Service
	m *member
	// prompt is the member's message of the turn running now.
	prompt func() (acphost.UserMessage, bool)

	mu    sync.Mutex
	turn  int64
	saves int
}

func (s *Service) preferenceCaller(m *member, next tools.Caller) *preferenceCaller {
	return &preferenceCaller{Caller: next, s: s, m: m, prompt: func() (acphost.UserMessage, bool) {
		live := m.live()
		if live == nil {
			return acphost.UserMessage{}, false
		}
		return live.acp.CurrentPrompt()
	}}
}

func (c *preferenceCaller) Call(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, *protocol.Error) {
	var out any
	var err error
	switch method {
	case protocol.MethodAssistantPreferences:
		out, err = c.list(ctx, params)
	case protocol.MethodAssistantRemember:
		out, err = c.remember(ctx, params)
	case protocol.MethodAssistantForget:
		out, err = c.forget(ctx, params)
	default:
		return c.Caller.Call(ctx, method, params)
	}
	if err != nil {
		var perr *protocol.Error
		if errors.As(err, &perr) {
			return nil, perr
		}
		return nil, &protocol.Error{Code: protocol.CodeInternal, Message: method + ": " + err.Error()}
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return nil, &protocol.Error{Code: protocol.CodeInternal, Message: method + ": " + err.Error()}
	}
	return raw, nil
}

// memoryOff refuses every preference method while the member turned
// preferences off or the conversation is temporary.
func (c *preferenceCaller) memoryOff(ctx context.Context) error {
	if c.s.cfg.Preferences == nil {
		return &protocol.Error{Code: protocol.CodeUnavailable, Message: "preferences are not configured on this server"}
	}
	enabled, err := c.s.cfg.Preferences.Enabled(ctx, c.m.id)
	if err != nil {
		return err
	}
	if !enabled {
		return &protocol.Error{Code: protocol.CodeDenied, Message: "memory is off: the member turned preferences off in Settings"}
	}
	row, err := c.s.stored(ctx, c.m.id)
	if err != nil {
		return err
	}
	if row.Temporary {
		return &protocol.Error{Code: protocol.CodeDenied, Message: "memory is off: this is a temporary conversation"}
	}
	return nil
}

// workspace is the workspace the member's held view is in, empty for none.
// fromPrompt keeps only the view their latest message carried, not one a
// later navigation replaced it with.
func (c *preferenceCaller) workspace(fromPrompt bool) string {
	held := c.s.View(c.m.id, time.Now())
	if held.ViewContext == nil || (fromPrompt && held.Source != viewFromPrompt) {
		return ""
	}
	return held.ViewContext.Route.WorkspaceID
}

func (c *preferenceCaller) list(ctx context.Context, raw json.RawMessage) (any, error) {
	p, err := protocol.DecodeParams[protocol.AssistantPreferencesParams](raw)
	if err != nil {
		return nil, &protocol.Error{Code: protocol.CodeInvalidParams, Message: err.Error()}
	}
	if err = c.memoryOff(ctx); err != nil {
		return nil, err
	}
	out := protocol.AssistantPreferencesResult{Preferences: []protocol.AssistantPreference{}}
	if p.ID != "" {
		got, gerr := c.s.cfg.Preferences.Get(ctx, c.m.id, p.ID)
		if gerr != nil {
			return nil, gerr
		}
		out.Preferences = append(out.Preferences, shown(got))
		return out, nil
	}
	ws := c.workspace(false)
	listed, err := c.s.cfg.Preferences.List(ctx, c.m.id, protocol.PreferenceListParams{WorkspaceID: ws, Query: p.Query})
	if err != nil {
		return nil, err
	}
	// With no workspace in view only the preferences for all of them apply.
	listed.Preferences = slices.DeleteFunc(listed.Preferences, func(p protocol.Preference) bool { return ws == "" && p.WorkspaceID != "" })
	var ids []string
	size := 0
	for _, pref := range listed.Preferences {
		if size += len(pref.Text) + len(pref.ID); len(out.Preferences) == maxShown || size > maxShownBytes {
			break
		}
		out.Preferences = append(out.Preferences, shown(pref))
		ids = append(ids, pref.ID)
	}
	if err = c.s.cfg.Preferences.Touch(ctx, c.m.id, ids); err != nil {
		return nil, err
	}
	return out, nil
}

func shown(p protocol.Preference) protocol.AssistantPreference {
	scope := "all"
	if p.WorkspaceID != "" {
		scope = "workspace"
	}
	return protocol.AssistantPreference{ID: p.ID, Text: p.Text, Scope: scope, Source: p.Source, UpdatedAt: p.UpdatedAt}
}

// remember saves a preference the member asked for in the message of the
// turn running now, quoting it.
func (c *preferenceCaller) remember(ctx context.Context, raw json.RawMessage) (any, error) {
	p, err := protocol.DecodeParams[protocol.AssistantRememberParams](raw)
	if err != nil {
		return nil, &protocol.Error{Code: protocol.CodeInvalidParams, Message: err.Error()}
	}
	if err = c.memoryOff(ctx); err != nil {
		return nil, err
	}
	msg, ok := c.prompt()
	if !ok {
		return nil, &protocol.Error{Code: protocol.CodeDenied, Message: "a preference is saved only while answering the member's own message " +
			"that asked for it; ask them to say it again"}
	}
	if err = quoted(msg.Text, p.Text); err != nil {
		return nil, err
	}
	var ws string
	if p.WorkspaceScoped {
		if ws = c.workspace(true); ws == "" {
			return nil, &protocol.Error{Code: protocol.CodeInvalidParams, Message: "workspace_scoped needs the workspace the member's message was sent from, " +
				"and it carried none or they have navigated since; save it for all workspaces or ask which workspace"}
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.turn != msg.Turn {
		c.turn, c.saves = msg.Turn, 0
	}
	if c.saves >= maxSavesPerTurn {
		return nil, &protocol.Error{Code: protocol.CodeDenied, Message: fmt.Sprintf(
			"one message saves at most %d preferences; ask the member which to keep", maxSavesPerTurn)}
	}
	var res protocol.PreferenceResult
	if p.Replaces != "" {
		res, err = c.s.cfg.Preferences.Update(ctx, c.m.id, protocol.PreferenceUpdateParams{
			ID: p.Replaces, Text: &p.Text, WorkspaceID: ws, ClearWorkspace: ws == "",
		}, protocol.PreferenceSourceAssistant, msg.MessageID)
	} else {
		res, err = c.s.cfg.Preferences.Add(ctx, c.m.id, p.Text, ws, protocol.PreferenceSourceAssistant, msg.MessageID)
	}
	if err != nil {
		return nil, err
	}
	c.saves++
	summary := fmt.Sprintf("Saved your preference: %q", res.Preference.Text)
	if res.Previous != nil {
		summary = fmt.Sprintf("Updated your preference: %q, was %q", res.Preference.Text, res.Previous.Text)
	}
	c.note(protocol.MethodAssistantRemember, summary, res.Preference.ID, nil, nil)
	return res, nil
}

func (c *preferenceCaller) forget(ctx context.Context, raw json.RawMessage) (any, error) {
	held, ok := c.m.actions.take(protocol.MethodAssistantForget, raw)
	p, err := protocol.DecodeParams[protocol.PreferenceIDParams](raw)
	if err == nil {
		err = c.memoryOff(ctx)
	}
	var res protocol.PreferenceDeleteResult
	if err == nil {
		res, err = c.s.cfg.Preferences.Delete(ctx, c.m.id, p.ID)
	}
	if ok {
		c.note(protocol.MethodAssistantForget, held.action.Summary, p.ID, &held, err)
	}
	return res, err
}

// note records a preference call's outcome in the conversation, after the
// approval approver recorded for a held call.
func (c *preferenceCaller) note(method, summary, id string, held *heldAction, err error) {
	conv := c.m.conversation()
	a := newAction(protocol.AssistantConfirmation{Method: method, Summary: summary, CreatedAt: time.Now()})
	if held != nil {
		conv, a = held.conv, held.action
	}
	a.Target.PreferenceID, a.FinishedAt = id, time.Now().UTC()
	a.Outcome = protocol.AssistantActionSucceeded
	body := ""
	if err != nil {
		a.Outcome, a.Error, body = protocol.AssistantActionFailed, err.Error(), err.Error()
	}
	if nerr := c.s.noteOutcome(conv, c.m, a, body); nerr != nil {
		slog.Warn("assistant: record the preference action", "member", c.m.id, "method", method, "action", a.ID, "error", nerr)
	}
}

var fold = cases.Fold()

// quoted refuses text unless it is a contiguous span of whole words of
// message, outside quoted lines and fenced code blocks, ignoring case and
// the punctuation around it, long enough to be a preference.
func quoted(message, text string) error {
	want := strings.TrimFunc(fold.String(preference.Normalize(text)), func(r rune) bool { return unicode.IsPunct(r) || unicode.IsSpace(r) })
	if len(strings.Fields(want)) < minQuoteWords || utf8.RuneCountInString(want) < minQuoteChars {
		return &protocol.Error{Code: protocol.CodeInvalidParams, Message: fmt.Sprintf(
			"a preference quotes at least %d words and %d characters of the member's message; ask them what to remember", minQuoteWords, minQuoteChars)}
	}
	for _, seg := range typedSegments(message) {
		seg = fold.String(preference.Normalize(seg))
		for i := 0; i <= len(seg); {
			j := strings.Index(seg[i:], want)
			if j < 0 {
				break
			}
			start, end := i+j, i+j+len(want)
			before, _ := utf8.DecodeLastRuneInString(seg[:start])
			after, _ := utf8.DecodeRuneInString(seg[end:])
			if !wordRune(before) && !wordRune(after) {
				return nil
			}
			_, size := utf8.DecodeRuneInString(seg[start:])
			i = start + size
		}
	}
	return &protocol.Error{Code: protocol.CodeInvalidParams, Message: "text must quote the member's own words from their latest message, " +
		"outside quoted lines and code blocks; quote them exactly or ask what to remember"}
}

func wordRune(r rune) bool {
	return r != utf8.RuneError && (unicode.IsLetter(r) || unicode.IsDigit(r))
}

// typedSegments splits message into runs of lines the member wrote
// themselves: lines starting with '>' and fenced code blocks, which hold
// text pasted from elsewhere, split it and are dropped.
func typedSegments(message string) []string {
	var out, cur []string
	fence := ""
	flush := func() {
		if len(cur) > 0 {
			out = append(out, strings.Join(cur, "\n"))
			cur = nil
		}
	}
	for _, line := range strings.Split(message, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case fence != "":
			if strings.HasPrefix(trimmed, fence) {
				fence = ""
			}
		case strings.HasPrefix(trimmed, "```"), strings.HasPrefix(trimmed, "~~~"):
			flush()
			fence = trimmed[:3]
		case strings.HasPrefix(trimmed, ">"):
			flush()
		default:
			cur = append(cur, line)
		}
	}
	flush()
	return out
}

// carriesPreference reports whether c, a launch or message auto-approval
// could pass, sends the text of one of the member's enabled preferences, so
// that a preference never launches or steers work without a card. A failed
// read counts as carrying one.
func (s *Service) carriesPreference(ctx context.Context, member domain.MemberID, c protocol.AssistantConfirmation) bool {
	if s.cfg.Preferences == nil {
		return false
	}
	var sent struct {
		Task    string `json:"task"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(c.Params, &sent)
	text := fold.String(preference.Normalize(sent.Task + "\n" + sent.Message))
	listed, err := s.cfg.Preferences.List(ctx, member, protocol.PreferenceListParams{})
	if err != nil {
		return true
	}
	for _, p := range listed.Preferences {
		if strings.Contains(text, fold.String(p.Text)) {
			return true
		}
	}
	return false
}

// preferenceNotes tells the agent, for its eyes only, which preferences
// changed since its last prompt, so it stops using their old text. restore
// puts the notes back for a prompt that was not sent.
func (s *Service) preferenceNotes(member domain.MemberID) (blocks []acp.ContentBlock, restore func()) {
	if s.cfg.Preferences == nil {
		return nil, func() {}
	}
	notes := s.cfg.Preferences.TakeChanges(member)
	if len(notes) == 0 {
		return nil, func() {}
	}
	block := acp.TextBlock("\n\n<aether-preference-changes>\n" + strings.Join(notes, "\n") + "\n</aether-preference-changes>")
	block.Text.Annotations = &acp.Annotations{Audience: []acp.Role{acp.RoleAssistant}}
	return []acp.ContentBlock{block}, func() { s.cfg.Preferences.RestoreChanges(member, notes) }
}

// withTemporary adds whether the member's conversation is temporary to out.
func (s *Service) withTemporary(ctx context.Context, id domain.MemberID, out protocol.AssistantSettings) (protocol.AssistantSettings, error) {
	row, err := s.stored(ctx, id)
	if err != nil {
		return protocol.AssistantSettings{}, err
	}
	out.Temporary = row.Temporary
	return out, nil
}

func (s *Service) setTemporary(ctx context.Context, id domain.MemberID, temporary *bool) error {
	if temporary == nil {
		return nil
	}
	if err := s.cfg.Store.SetAssistantTemporary(ctx, id, *temporary); err != nil {
		return fmt.Errorf("assistant: %w", err)
	}
	return nil
}
