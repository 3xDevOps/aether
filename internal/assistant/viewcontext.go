package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	acp "github.com/coder/acp-go-sdk"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

const (
	// ViewContextURI names the view context block in a prompt.
	ViewContextURI = "aether://view-context"

	viewContextVersion  = 1
	maxViewContextBytes = 4 << 10
	maxViewRecent       = 8
	maxViewPath         = 512
	maxViewAttachments  = 6
	maxViewCheck        = 128
)

var (
	viewID         = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)
	viewWord       = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,39}$`)
	viewRepository = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,100}/[A-Za-z0-9_.-]{1,100}$`)
	attachmentKind = map[string]bool{"workspace": true, "run": true, "pr": true, "check": true, "failure": true}
)

// CleanViewContext checks a view context from the dashboard: an id, word or
// path that is malformed is dropped, recent and attachments are cut to their
// caps, and the oldest recent pages, then the open file, then the last
// attachments go until it fits in 4 KiB. An unknown version is dropped whole.
func CleanViewContext(v *protocol.AssistantViewContext) (*protocol.AssistantViewContext, error) {
	if v == nil || v.Version != viewContextVersion {
		return nil, nil
	}
	out := &protocol.AssistantViewContext{Version: v.Version, CapturedAt: cleanTime(v.CapturedAt), Route: cleanEntry(v.Route)}
	out.Route.At = time.Time{}
	if f := v.OpenFile; f != nil && (f.Kind == "workspace" || f.Kind == "home") && cleanPath(f.Path) != "" {
		out.OpenFile = &protocol.AssistantViewFile{Kind: f.Kind, Path: cleanPath(f.Path), RunID: cleanID(f.RunID), WorkspaceID: cleanID(f.WorkspaceID)}
	}
	for _, e := range v.Recent {
		if e = cleanEntry(e); e.Name != "" && len(out.Recent) < maxViewRecent {
			out.Recent = append(out.Recent, e)
		}
	}
	for _, a := range v.Attachments {
		if clean, ok := cleanAttachment(a); ok && len(out.Attachments) < maxViewAttachments {
			out.Attachments = append(out.Attachments, clean)
		}
	}
	for {
		b, err := json.Marshal(out)
		if err != nil {
			return nil, fmt.Errorf("encode view_context: %w", err)
		}
		switch {
		case len(b) <= maxViewContextBytes:
			return out, nil
		case len(out.Recent) > 0:
			out.Recent = out.Recent[:len(out.Recent)-1]
		case out.OpenFile != nil:
			out.OpenFile = nil
		case len(out.Attachments) > 0:
			out.Attachments = out.Attachments[:len(out.Attachments)-1]
		default:
			return nil, fmt.Errorf("view_context is %d bytes with only its route; the limit is %d", len(b), maxViewContextBytes)
		}
	}
}

func cleanEntry(e protocol.AssistantViewEntry) protocol.AssistantViewEntry {
	return protocol.AssistantViewEntry{
		Name: cleanWord(e.Name), WorkspaceID: cleanID(e.WorkspaceID), RunID: cleanID(e.RunID),
		Subagent: cleanID(e.Subagent), View: cleanWord(e.View), At: cleanTime(e.At),
	}
}

// cleanAttachment drops malformed fields, and the attachment when its kind
// is unknown or it names nothing.
func cleanAttachment(a protocol.AssistantViewAttachment) (protocol.AssistantViewAttachment, bool) {
	out := protocol.AssistantViewAttachment{
		Kind: a.Kind, WorkspaceID: cleanID(a.WorkspaceID), RunID: cleanID(a.RunID), Failure: cleanWord(a.Failure),
	}
	if a.PR > 0 {
		out.PR = a.PR
	}
	if viewRepository.MatchString(a.Repository) {
		out.Repository = a.Repository
	}
	if len(a.Check) <= maxViewCheck && utf8.ValidString(a.Check) && !strings.ContainsFunc(a.Check, unprintable) {
		out.Check = strings.TrimSpace(a.Check)
	}
	if a.Seq > 0 {
		out.Seq = a.Seq
	}
	named := out.WorkspaceID != "" || out.RunID != "" || out.PR != 0
	return out, attachmentKind[a.Kind] && named
}

// unprintable is a control or format character, such as a bidi override or
// a zero-width space, which would make a check name read as another.
func unprintable(r rune) bool {
	return unicode.In(r, unicode.Cc, unicode.Cf)
}

// cleanTime zeroes a time JSON cannot encode.
func cleanTime(t time.Time) time.Time {
	if t = t.UTC(); t.Year() < 1 || t.Year() > 9999 {
		return time.Time{}
	}
	return t
}

func cleanID(s string) string {
	if viewID.MatchString(s) {
		return s
	}
	return ""
}

func cleanWord(s string) string {
	if viewWord.MatchString(s) {
		return s
	}
	return ""
}

func cleanPath(s string) string {
	if len(s) > maxViewPath || !utf8.ValidString(s) || strings.ContainsFunc(s, unicode.IsControl) {
		return ""
	}
	return s
}

// The tags around the view context for an agent that takes no embedded
// resource.
const (
	viewOpen  = "<aether-view-context>"
	viewClose = "</aether-view-context>"
)

// promptView is the view context among a prompt's blocks for the agent,
// as promptBlocks wrote it, or nil.
func promptView(blocks []acp.ContentBlock) *protocol.AssistantViewContext {
	for _, b := range blocks {
		var raw string
		switch {
		case b.Resource != nil && b.Resource.Resource.TextResourceContents != nil && b.Resource.Resource.TextResourceContents.Uri == ViewContextURI:
			raw = b.Resource.Resource.TextResourceContents.Text
		case b.Text != nil && strings.HasPrefix(strings.TrimSpace(b.Text.Text), viewOpen):
			raw = strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(b.Text.Text), viewOpen), viewClose)
		default:
			continue
		}
		var v protocol.AssistantViewContext
		if json.Unmarshal([]byte(raw), &v) == nil {
			return &v
		}
	}
	return nil
}

// promptBlocks follows text with v, annotated for the agent alone so the
// transcript records only the member's text: an embedded resource, or
// delimited text for an agent that takes none.
func promptBlocks(text string, v *protocol.AssistantViewContext, embedded bool) ([]acp.ContentBlock, error) {
	blocks := []acp.ContentBlock{acp.TextBlock(text)}
	if v == nil {
		return blocks, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("assistant: encode the view context: %w", err)
	}
	agentOnly := &acp.Annotations{Audience: []acp.Role{acp.RoleAssistant}}
	if !embedded {
		block := acp.TextBlock("\n\n" + viewOpen + "\n" + string(b) + "\n" + viewClose)
		block.Text.Annotations = agentOnly
		return append(blocks, block), nil
	}
	mime := "application/json"
	block := acp.ResourceBlock(acp.EmbeddedResourceResource{TextResourceContents: &acp.TextResourceContents{
		Uri: ViewContextURI, MimeType: &mime, Text: string(b),
	}})
	block.Resource.Annotations = agentOnly
	return append(blocks, block), nil
}

// Sources of the held view context.
const (
	viewFromPrompt     = "prompt"
	viewFromNavigation = "navigation"
)

// viewRace is how long after a view arrives a navigation update captured
// before it counts as in flight, and loses. Past it the update wins, so a
// device whose clock runs slow still moves the view.
const viewRace = 10 * time.Second

// sharedView returns what a prompt or update may carry of v while the
// member is not fenced, else nil; the caller holds m.share. While the member
// shares their view that is v, else only its attachments, which the member
// picked for this message.
func (s *Service) sharedView(ctx context.Context, m *member, v *protocol.AssistantViewContext) (*protocol.AssistantViewContext, error) {
	if v == nil {
		return nil, nil
	}
	st, err := s.cfg.Store.AssistantSettings(ctx, m.id)
	if err != nil {
		return nil, fmt.Errorf("assistant: read the view sharing setting: %w", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	switch {
	case m.removing, !st.ShareView && len(v.Attachments) == 0:
		return nil, nil
	case !st.ShareView:
		return &protocol.AssistantViewContext{Version: v.Version, CapturedAt: v.CapturedAt, Attachments: v.Attachments}, nil
	}
	return v, nil
}

// holdView makes the page part of v, from sharedView under the same
// m.share, the held view; attachments belong to one message and are never
// held. A prompt's page always does. A navigation update does unless it was
// captured before the held view, which arrived within viewRace: the update
// was in flight when the newer view was sent. A navigation update without a
// page forgets the held view under the same rule, since the member removed
// the page from their draft; a prompt without one leaves it alone.
func (m *member) holdView(v *protocol.AssistantViewContext, source string, now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if v == nil || m.removing {
		return
	}
	stale := m.view != nil && v.CapturedAt.Before(m.view.CapturedAt) && now.Sub(m.viewAt) < viewRace
	switch {
	case v.Route.Name == "" && source == viewFromNavigation && !stale:
		m.forgetViewLocked()
	case v.Route.Name == "":
	case source == viewFromPrompt || !stale:
		if len(v.Attachments) > 0 {
			page := *v
			page.Attachments = nil
			v = &page
		}
		m.view, m.viewAt, m.viewSource = v, now, source
	}
}

// SetView offers a navigation update as the member's held view context. It
// is kept in memory only.
func (s *Service) SetView(ctx context.Context, id domain.MemberID, v *protocol.AssistantViewContext) error {
	m := s.member(id)
	m.share.RLock()
	defer m.share.RUnlock()
	v, err := s.sharedView(ctx, m, v)
	if err != nil {
		return err
	}
	m.holdView(v, viewFromNavigation, time.Now())
	return nil
}

// SetShareView stores whether the member shares their view context; off
// forgets the one held. It holds m.share, so neither an update already on
// its way nor a prompt being sent can carry the view past it, and
// m.settings, so it does not race the other writes of the settings row.
func (s *Service) SetShareView(ctx context.Context, id domain.MemberID, on bool) error {
	m := s.member(id)
	defer m.notify()
	m.share.Lock()
	defer m.share.Unlock()
	m.settings.Lock()
	defer m.settings.Unlock()
	st, err := s.cfg.Store.AssistantSettings(ctx, id)
	if err != nil {
		return fmt.Errorf("assistant: %w", err)
	}
	st.ShareView = on
	if err = s.cfg.Store.PutAssistantSettings(ctx, id, st); err != nil {
		return fmt.Errorf("assistant: %w", err)
	}
	if !on {
		m.mu.Lock()
		m.forgetViewLocked()
		m.mu.Unlock()
	}
	return nil
}

func (m *member) forgetViewLocked() {
	m.view, m.viewAt, m.viewSource = nil, time.Time{}, ""
}

// View is the member's held view context, where it came from, its age since
// it arrived, and whether a dashboard streams their assistant now.
func (s *Service) View(id domain.MemberID, now time.Time) protocol.AssistantViewContextResult {
	m := s.member(id)
	m.mu.Lock()
	out := protocol.AssistantViewContextResult{ViewContext: m.view, Source: m.viewSource}
	if m.view != nil {
		out.AgeSeconds = int64(max(now.Sub(m.viewAt), 0) / time.Second)
	}
	m.mu.Unlock()
	m.wmu.Lock()
	out.Connected = len(m.watchers) > 0
	m.wmu.Unlock()
	return out
}
