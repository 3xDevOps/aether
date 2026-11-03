package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"

	"github.com/3xDevOps/Aether/internal/assistant/tools"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/preference"
	"github.com/3xDevOps/Aether/internal/protocol"
)

const (
	// maxConnectorCard is the longest card text a connector is shown to
	// decide; a longer card is decided only in Aether.
	maxConnectorCard = 2000
	// maxConnectorReply caps the reply a connector turn returns, in bytes.
	maxConnectorReply = 4 << 10

	preferenceWithheld = "[preference withheld]"
)

// ConnectorCard is a card as a connector is shown it: Text is its FullText,
// which the connector must echo to decide it. AetherOnly is set when the card is too long or shows a saved
// preference; Text is empty then.
type ConnectorCard struct {
	Card
	Text       string
	AetherOnly bool
}

// GrantCard returns the member's pending card id when a turn that grant's
// connector sent raised it, and ErrNoConfirmation for any other card: one
// of another door, another grant, or one already decided.
func (s *Service) GrantCard(ctx context.Context, member domain.MemberID, grant, id string) (ConnectorCard, error) {
	c, err := s.Card(ctx, member, id)
	if err != nil {
		return ConnectorCard{}, err
	}
	missing := fmt.Errorf("%w: %s", ErrNoConfirmation, id)
	if grant == "" || c.Origin.Door != protocol.AssistantDoorConnector || c.Origin.Grant != grant || c.State != protocol.AssistantCardPending {
		return ConnectorCard{}, missing
	}
	raised, err := s.grantRaised(ctx, member, grant, id)
	if err != nil {
		return ConnectorCard{}, err
	}
	if !raised {
		return ConnectorCard{}, missing
	}
	return s.connectorCard(ctx, member, c)
}

// grantRaised reports whether a queue entry grant's connector sent raised
// card id: a card's origin alone could come from a turn the connector did
// not send.
func (s *Service) grantRaised(ctx context.Context, member domain.MemberID, grant, id string) (bool, error) {
	rows, err := s.cfg.Store.AssistantQueue(ctx, member)
	if err != nil {
		return false, fmt.Errorf("assistant: list the queued prompts: %w", err)
	}
	for _, row := range rows {
		if !slices.Contains(row.Cards, id) {
			continue
		}
		e, err := entryOf(row)
		if err != nil {
			return false, err
		}
		return e.Origin.Door == protocol.AssistantDoorConnector && e.Origin.Grant == grant, nil
	}
	return false, nil
}

func (s *Service) connectorCard(ctx context.Context, member domain.MemberID, c Card) (ConnectorCard, error) {
	text := c.FullText()
	shows, err := s.showsPreference(ctx, member, c.shownValues())
	if err != nil {
		return ConnectorCard{}, err
	}
	// The full text's lines are its fields; a summary that holds a line
	// break could show a field the call does not send.
	hidden := oneLine(c.Summary) != c.Summary ||
		strings.ContainsFunc(text, func(r rune) bool { return r != '\n' && r != ' ' && !unicode.IsPrint(r) })
	if c.Method == protocol.MethodAssistantForget || shows || hidden || utf8.RuneCountInString(text) > maxConnectorCard {
		return ConnectorCard{Card: c, AetherOnly: true}, nil
	}
	return ConnectorCard{Card: c, Text: text}, nil
}

// liveOrigin is o with its connector grant's scopes as they are now. A
// grant that was revoked or no longer acts for the member fails, so a
// waiting prompt does not run, and a call does not pass, on scopes it lost.
func (s *Service) liveOrigin(ctx context.Context, member domain.MemberID, o Origin) (Origin, error) {
	if o.Door != protocol.AssistantDoorConnector || s.cfg.ConnectorGrant == nil {
		return o, nil
	}
	scopes, err := s.cfg.ConnectorGrant(ctx, member, o.Grant)
	if err != nil {
		return o, fmt.Errorf("assistant: %s connector %s: %w", o.Client, o.Grant, err)
	}
	o.Scopes = scopes
	return o, nil
}

// shownValues is everything c's full text shows, as the call sends it
// rather than escaped or quoted: the summary, the stated reason and
// rendered task, and each key, string, number and boolean in the params at
// any depth, so neither an escape nor nesting can split a preference's
// words.
func (c Card) shownValues() string {
	values := []string{c.Summary, c.Reason, c.Task}
	var params any
	dec := json.NewDecoder(bytes.NewReader(c.Params))
	dec.UseNumber()
	_ = dec.Decode(&params)
	var walk func(v any)
	walk = func(v any) {
		switch v := v.(type) {
		case string:
			values = append(values, v)
		case json.Number:
			values = append(values, v.String())
		case bool:
			values = append(values, strconv.FormatBool(v))
		case []any:
			for _, item := range v {
				walk(item)
			}
		case map[string]any:
			for key, item := range v {
				values = append(values, key)
				walk(item)
			}
		}
	}
	walk(params)
	return strings.Join(values, "\n")
}

// showsPreference reports whether text holds one of the member's saved
// preferences word for word.
func (s *Service) showsPreference(ctx context.Context, member domain.MemberID, text string) (bool, error) {
	prefs, err := s.savedPreferences(ctx, member)
	if err != nil {
		return false, err
	}
	words := preference.Words(clean(text))
	for _, p := range prefs {
		if preference.Index(words, p) >= 0 {
			return true, nil
		}
	}
	return false, nil
}

func (s *Service) savedPreferences(ctx context.Context, member domain.MemberID) ([][]preference.Word, error) {
	if s.cfg.Preferences == nil {
		return nil, nil
	}
	// A disabled preference keeps its text, which earlier turns may have read.
	listed, err := s.cfg.Preferences.List(ctx, member, protocol.PreferenceListParams{IncludeDisabled: true})
	if err != nil {
		return nil, fmt.Errorf("assistant: read the saved preferences: %w", err)
	}
	out := make([][]preference.Word, 0, len(listed.Preferences))
	for _, p := range listed.Preferences {
		if w := preference.Words(p.Text); len(w) > 0 {
			out = append(out, w)
		}
	}
	return out, nil
}

// ConnectorReply is a connector turn's reply as the connector gets it: each
// run of words matching a saved preference replaced, then cut to
// maxConnectorReply bytes with a note of where.
func (s *Service) ConnectorReply(ctx context.Context, member domain.MemberID, reply string) (string, error) {
	prefs, err := s.savedPreferences(ctx, member)
	if err != nil {
		return "", err
	}
	reply = clean(reply)
	words := preference.Words(reply)
	// Every match of every preference is withheld, overlapping ones as one
	// span, so no part of a preference shows next to the marker.
	withheld := make([]bool, len(words))
	for i := range words {
		for _, p := range prefs {
			if preference.Index(words[i:min(i+len(p), len(words))], p) == 0 {
				for j := i; j < i+len(p); j++ {
					withheld[j] = true
				}
			}
		}
	}
	var out strings.Builder
	last := 0
	for i := 0; i < len(words); i++ {
		if !withheld[i] {
			continue
		}
		j := i
		for j+1 < len(words) && withheld[j+1] {
			j++
		}
		out.WriteString(reply[last:words[i].Start] + preferenceWithheld)
		last, i = words[j].End, j
	}
	out.WriteString(reply[last:])
	reply = out.String()
	if len(reply) <= maxConnectorReply {
		return reply, nil
	}
	note := fmt.Sprintf("\n[reply cut here, at byte %%d of %d]", len(reply))
	end := maxConnectorReply - len(fmt.Sprintf(note, maxConnectorReply))
	for !utf8.RuneStart(reply[end]) {
		end--
	}
	return reply[:end] + fmt.Sprintf(note, end), nil
}

// clean is text in the form preferences are compared in, NFKC without
// format or control characters, with its whitespace kept.
func clean(text string) string {
	return strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Cf, r) || (unicode.IsControl(r) && !unicode.IsSpace(r)) {
			return -1
		}
		return r
	}, norm.NFKC.String(text))
}

// turnView is the member's view context for the turn running: none in a
// connector turn, whose reply leaves Aether.
func (s *Service) turnView(m *member) protocol.AssistantViewContextResult {
	if origin, _ := m.turn(); origin.Door == protocol.AssistantDoorConnector {
		return protocol.AssistantViewContextResult{}
	}
	return s.View(m.id, time.Now())
}

// GrantTurn returns the member's newest queued or running entry with text
// that grant's connector sent since, so a host's retry of a keyless prompt
// reaches it, even across a restart; ok is false when there is none.
func (s *Service) GrantTurn(ctx context.Context, member domain.MemberID, grant, text string, since time.Time) (e Entry, ok bool, err error) {
	rows, err := s.cfg.Store.AssistantQueue(ctx, member, protocol.AssistantQueueQueued, protocol.AssistantQueueRunning)
	if err != nil {
		return Entry{}, false, fmt.Errorf("assistant: list the queued prompts: %w", err)
	}
	for _, row := range slices.Backward(rows) {
		if row.Text != text || row.CreatedAt.Before(since) {
			continue
		}
		if e, err = entryOf(row); err != nil {
			return Entry{}, false, err
		}
		if e.Origin.Door == protocol.AssistantDoorConnector && e.Origin.Grant == grant {
			return e, true, nil
		}
	}
	return Entry{}, false, nil
}

// grantGate refuses the calls that pass without a card, the reversible
// ones, in a connector turn whose grant was revoked, no longer acts for the
// member, or does not cover the method. A call the approver passed, such as
// a relaying watch whose card was approved, is held and goes through: the
// approver judged it, and the turn running when a parked card runs may be
// another.
type grantGate struct {
	tools.Caller
	s *Service
	m *member
}

func (g grantGate) Call(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, *protocol.Error) {
	if tools.Reversible(method) && !g.m.actions.holds(method, params) {
		origin, _ := g.m.turn()
		live, err := g.s.liveOrigin(ctx, g.m.id, origin)
		if err != nil {
			return nil, &protocol.Error{Code: protocol.CodeDenied, Message: method + " was not run: " + err.Error()}
		}
		if !live.Covers(method, g.s.cfg.ConnectorScopes) {
			return nil, &protocol.Error{Code: protocol.CodeDenied, Message: fmt.Sprintf(
				"%s was not run: %s connector %s may not call it; ask the member to do it in Aether", method, live.Client, live.Grant)}
		}
	}
	return g.Caller.Call(ctx, method, params)
}

// holds reports whether a call with method and params was passed and has
// not run yet.
func (h *heldActions) holds(method string, params json.RawMessage) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.ContainsFunc(h.held[method+"\x00"+string(params)], func(a heldAction) bool { return a.conv.Err() == nil })
}
