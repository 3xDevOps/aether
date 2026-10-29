package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	acp "github.com/coder/acp-go-sdk"

	"github.com/3xDevOps/Aether/internal/acphost"
	"github.com/3xDevOps/Aether/internal/assistant/tools"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/preference"
	"github.com/3xDevOps/Aether/internal/protocol"
)

const (
	// maxSavesPerTurn bounds the preferences one message can save.
	maxSavesPerTurn = 3
	// maxShown and maxShownBytes bound what aether_my_preferences returns,
	// the latter as encoded.
	maxShown      = 40
	maxShownBytes = 4 << 10
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
	case protocol.MethodAssistantPreferenceShow:
		out, err = c.show(ctx, params)
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

func (c *preferenceCaller) configured() error {
	if c.s.cfg.Preferences == nil {
		return &protocol.Error{Code: protocol.CodeUnavailable, Message: "preferences are not configured on this server"}
	}
	return nil
}

// memoryOff refuses reading and saving preferences while the member turned
// them off or the conversation is temporary. Forgetting one is always
// allowed. The caller holds m.settings, so a switch falls wholly before or
// after the call it decides.
func (c *preferenceCaller) memoryOff(ctx context.Context) error {
	if err := c.configured(); err != nil {
		return err
	}
	why, err := c.s.memoryOff(ctx, c.m.id)
	if err != nil {
		return err
	}
	if why != "" {
		return &protocol.Error{Code: protocol.CodeDenied, Message: "memory is off: " + why}
	}
	return nil
}

// memoryOff says why the member's assistant may not use preferences now,
// or is empty when it may.
func (s *Service) memoryOff(ctx context.Context, id domain.MemberID) (string, error) {
	enabled, err := s.cfg.Preferences.Enabled(ctx, id)
	if err != nil {
		return "", err
	}
	if !enabled {
		return "the member turned preferences off in Settings", nil
	}
	row, err := s.stored(ctx, id)
	if err != nil {
		return "", err
	}
	if row.Temporary {
		return "this is a temporary conversation", nil
	}
	return "", nil
}

// workspace is the workspace the member's held view is in, empty for none.
func (c *preferenceCaller) workspace() string {
	held := c.s.View(c.m.id, time.Now())
	if held.ViewContext == nil {
		return ""
	}
	return held.ViewContext.Route.WorkspaceID
}

func (c *preferenceCaller) list(ctx context.Context, raw json.RawMessage) (any, error) {
	p, err := protocol.DecodeParams[protocol.AssistantPreferencesParams](raw)
	if err != nil {
		return nil, &protocol.Error{Code: protocol.CodeInvalidParams, Message: err.Error()}
	}
	c.m.settings.Lock()
	defer c.m.settings.Unlock()
	if err = c.memoryOff(ctx); err != nil {
		return nil, err
	}
	out := protocol.AssistantPreferencesResult{Preferences: []protocol.AssistantPreference{}}
	ws := c.workspace()
	listed, err := c.s.cfg.Preferences.List(ctx, c.m.id, protocol.PreferenceListParams{WorkspaceID: ws, Query: p.Query})
	if err != nil {
		return nil, err
	}
	// With no workspace in view only the preferences for all of them apply.
	listed.Preferences = slices.DeleteFunc(listed.Preferences, func(p protocol.Preference) bool { return ws == "" && p.WorkspaceID != "" })
	var ids []string
	size := len(`{"preferences":[]}`)
	for _, pref := range listed.Preferences {
		item := shown(pref)
		b, merr := json.Marshal(item)
		if merr != nil {
			return nil, merr
		}
		if size += len(b) + len(","); len(out.Preferences) == maxShown || size > maxShownBytes {
			break
		}
		out.Preferences = append(out.Preferences, item)
		ids = append(ids, pref.ID)
	}
	if err = c.s.cfg.Preferences.Touch(ctx, c.m.id, ids); err != nil {
		return nil, err
	}
	return out, nil
}

// show reads one preference by id for the card of aether_forget, which
// works with memory off; the tool socket refuses the method itself.
func (c *preferenceCaller) show(ctx context.Context, raw json.RawMessage) (any, error) {
	p, err := protocol.DecodeParams[protocol.PreferenceIDParams](raw)
	if err != nil {
		return nil, &protocol.Error{Code: protocol.CodeInvalidParams, Message: err.Error()}
	}
	if err = c.configured(); err != nil {
		return nil, err
	}
	got, err := c.s.cfg.Preferences.Get(ctx, c.m.id, p.ID)
	if err != nil {
		return nil, err
	}
	return shown(got), nil
}

func shown(p protocol.Preference) protocol.AssistantPreference {
	scope := "all"
	if p.WorkspaceID != "" {
		scope = "workspace"
	}
	return protocol.AssistantPreference{ID: p.ID, Text: p.Text, Scope: scope, Source: p.Source, UpdatedAt: p.UpdatedAt}
}

// remember saves a preference the member asked for in the message of the
// turn running now, quoting it, and notes it in the conversation the call
// came in, which a reset may end before the save returns.
func (c *preferenceCaller) remember(ctx context.Context, raw json.RawMessage) (any, error) {
	conv := c.m.conversation()
	p, err := protocol.DecodeParams[protocol.AssistantRememberParams](raw)
	if err != nil {
		return nil, &protocol.Error{Code: protocol.CodeInvalidParams, Message: err.Error()}
	}
	c.m.settings.Lock()
	defer c.m.settings.Unlock()
	if err = c.memoryOff(ctx); err != nil {
		return nil, err
	}
	msg, ok := c.prompt()
	if !ok {
		return nil, &protocol.Error{Code: protocol.CodeDenied, Message: "a preference is saved only while answering the member's own message " +
			"that asked for it; ask them to say it again"}
	}
	if p.Text, err = quoted(msg.Text, p.Text); err != nil {
		return nil, err
	}
	var ws string
	if p.WorkspaceScoped {
		if v := promptView(msg.Context); v != nil {
			ws = v.Route.WorkspaceID
		}
		if ws == "" {
			return nil, &protocol.Error{Code: protocol.CodeInvalidParams, Message: "workspace_scoped needs the workspace the member's message was sent from, " +
				"and it carried none; save it for all workspaces or ask which workspace"}
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
	if err = c.note(conv, newAction(protocol.AssistantConfirmation{Method: protocol.MethodAssistantRemember, Summary: summary, CreatedAt: time.Now()}),
		res.Preference.ID, nil); err != nil {
		return nil, fmt.Errorf("preference %s was saved, but recording that in the transcript failed: %w", res.Preference.ID, err)
	}
	return res, nil
}

func (c *preferenceCaller) forget(ctx context.Context, raw json.RawMessage) (any, error) {
	held, ok := c.m.actions.take(protocol.MethodAssistantForget, raw)
	p, err := protocol.DecodeParams[protocol.PreferenceIDParams](raw)
	if err == nil {
		err = c.configured()
	}
	var res protocol.PreferenceDeleteResult
	if err == nil {
		res, err = c.s.cfg.Preferences.Delete(ctx, c.m.id, p.ID, held.shown)
	}
	if !ok {
		return res, err
	}
	if nerr := c.note(held.conv, held.action, p.ID, err); nerr != nil {
		if err == nil {
			return nil, fmt.Errorf("preference %s was forgotten, but recording that in the transcript failed: %w", p.ID, nerr)
		}
		return nil, fmt.Errorf("%v; recording that in the transcript failed too: %w", err, nerr)
	}
	return res, err
}

// note records the outcome of action a on preference id in conv, the
// conversation the call came in.
func (c *preferenceCaller) note(conv context.Context, a protocol.AssistantAction, id string, err error) error {
	a.Target.PreferenceID, a.FinishedAt = id, time.Now().UTC()
	a.Outcome = protocol.AssistantActionSucceeded
	body := ""
	if err != nil {
		a.Outcome, a.Error, body = protocol.AssistantActionFailed, err.Error(), err.Error()
	}
	return c.s.noteOutcome(conv, c.m, a, body)
}

// quoted returns the span of message that text quotes: a contiguous run of
// whole words of it, outside quoted lines and code blocks, ignoring case and
// punctuation, long enough to be a preference. The span is the member's own
// text, without the punctuation around it. Words ignore symbols, so of the
// spans matching text, one text holds with its symbols wins: "use C++"
// over an earlier "use C#".
func quoted(message, text string) (string, error) {
	text = preference.Normalize(text)
	want := preference.Words(text)
	tooShort := &protocol.Error{Code: protocol.CodeInvalidParams, Message: fmt.Sprintf(
		"a preference quotes at least %d words and %d characters of the member's message; ask them what to remember",
		preference.MinWords, preference.MinChars)}
	if len(want) < preference.MinWords {
		return "", tooShort
	}
	text = strings.ToLower(text)
	found := ""
search:
	for _, seg := range typedSegments(message) {
		seg = preference.Normalize(seg)
		words := preference.Words(seg)
		for from := 0; ; {
			i := preference.Index(words[from:], want)
			if i < 0 {
				break
			}
			i += from
			span := attached(seg, words[i].Start, words[i+len(want)-1].End)
			exact := strings.Contains(text, strings.ToLower(span))
			if found == "" || exact {
				found = span
			}
			if exact {
				break search
			}
			from = i + 1
		}
	}
	switch {
	case found == "":
		return "", &protocol.Error{Code: protocol.CodeInvalidParams, Message: "text must quote the member's own words from their latest message, " +
			"outside quoted lines and code blocks; quote them exactly or ask what to remember"}
	case utf8.RuneCountInString(found) < preference.MinChars:
		return "", tooShort
	}
	return found, nil
}

// attached widens seg[start:end] to the symbols joined to its first and
// last words, such as the "++" of "C++" or the "#" of "#ops", without the
// quotes, brackets and sentence punctuation around them. A closing bracket
// the span opened stays, as in "fmt.Println()".
func attached(seg string, start, end int) string {
	symbol := func(r rune) bool { return !unicode.IsSpace(r) && !unicode.IsLetter(r) && !unicode.IsDigit(r) }
	for start > 0 {
		r, size := utf8.DecodeLastRuneInString(seg[:start])
		if !symbol(r) || strings.ContainsRune(`"'([{«“‘`, r) {
			break
		}
		start -= size
	}
	for end < len(seg) {
		r, size := utf8.DecodeRuneInString(seg[end:])
		if !symbol(r) {
			break
		}
		end += size
	}
	span := seg[start:end]
	for span != "" {
		r, size := utf8.DecodeLastRuneInString(span)
		if !strings.ContainsRune(`.,;:!?…"')]}»”’`, r) {
			break
		}
		if open, ok := openers[r]; ok && strings.Count(span, string(open)) >= strings.Count(span, string(r)) {
			break
		}
		span = span[:len(span)-size]
	}
	return span
}

var openers = map[rune]rune{')': '(', ']': '[', '}': '{', '»': '«', '”': '“', '’': '‘'}

// typedSegments splits message into runs of lines the member wrote
// themselves. Text pasted from elsewhere splits it and is dropped: quoted
// lines starting with '>' and the lines continuing them up to a blank line,
// fenced code blocks, and lines indented as code by four columns, counting
// a tab to the next multiple of four. An indented fence is code, not a
// fence.
func typedSegments(message string) []string {
	var out, cur []string
	fence, quoting := "", false
	flush := func() {
		if len(cur) > 0 {
			out = append(out, strings.Join(cur, "\n"))
			cur = nil
		}
	}
	for _, line := range strings.Split(message, "\n") {
		trimmed := strings.TrimSpace(line)
		indented := indent(line) >= 4
		run := fenceRun(trimmed)
		switch {
		case fence != "":
			if !indented && run != "" && run[0] == fence[0] && len(run) >= len(fence) && strings.TrimSpace(trimmed[len(run):]) == "" {
				fence = ""
			}
		case run != "" && !indented:
			flush()
			fence, quoting = run, false
		case strings.HasPrefix(trimmed, ">"):
			flush()
			quoting = true
		case trimmed == "":
			quoting = false
			cur = append(cur, line)
		case quoting:
		case indented:
			flush()
		default:
			cur = append(cur, line)
		}
	}
	flush()
	return out
}

// indent is the width of line's leading spaces and tabs, a tab reaching the
// next multiple of four columns.
func indent(line string) int {
	n := 0
	for _, r := range line {
		switch r {
		case ' ':
			n++
		case '\t':
			n += 4 - n%4
		default:
			return n
		}
	}
	return n
}

// fenceRun is the run of backticks or tildes that opens or closes a code
// fence at the start of line, or empty.
func fenceRun(line string) string {
	if line == "" || (line[0] != '`' && line[0] != '~') {
		return ""
	}
	n := len(line) - len(strings.TrimLeft(line, line[:1]))
	if n < 3 {
		return ""
	}
	return line[:n]
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
	words := preference.Words(preference.Normalize(sent.Task + "\n" + sent.Message))
	listed, err := s.cfg.Preferences.List(ctx, member, protocol.PreferenceListParams{})
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

// preferenceNotes tells the agent, for its eyes only, which preferences
// changed since its last prompt, so it stops using their old text, and,
// while memory is off, not to use the preferences it read earlier in the
// conversation. restore puts the changes back for a prompt that was not
// sent.
func (s *Service) preferenceNotes(ctx context.Context, member domain.MemberID) (blocks []acp.ContentBlock, restore func(), err error) {
	if s.cfg.Preferences == nil {
		return nil, func() {}, nil
	}
	why, err := s.memoryOff(ctx, member)
	if err != nil {
		return nil, nil, fmt.Errorf("assistant: read whether memory is on: %w", err)
	}
	changes := s.cfg.Preferences.TakeChanges(ctx, member)
	notes := changes
	if why != "" {
		notes = append(slices.Clip(notes), "memory is off for this message: "+why+"; do not use or mention any preference you read earlier in this conversation")
	}
	if len(notes) == 0 {
		return nil, func() {}, nil
	}
	block := acp.TextBlock("\n\n<aether-preference-changes>\n" + strings.Join(notes, "\n") + "\n</aether-preference-changes>")
	block.Text.Annotations = &acp.Annotations{Audience: []acp.Role{acp.RoleAssistant}}
	return []acp.ContentBlock{block}, func() { s.cfg.Preferences.RestoreChanges(member, changes) }, nil
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
