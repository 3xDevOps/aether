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
)

var (
	viewID   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)
	viewWord = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,39}$`)
)

// CleanViewContext checks a view context from the dashboard: an id, word or
// path that is malformed is dropped, recent is cut to its cap, and the oldest
// recent pages, then the open file, go until it fits in 4 KiB. An unknown
// version is dropped whole.
func CleanViewContext(v *protocol.AssistantViewContext) (*protocol.AssistantViewContext, error) {
	if v == nil || v.Version != viewContextVersion {
		return nil, nil
	}
	out := &protocol.AssistantViewContext{Version: v.Version, CapturedAt: v.CapturedAt.UTC(), Route: cleanEntry(v.Route)}
	out.Route.At = time.Time{}
	if f := v.OpenFile; f != nil && (f.Kind == "workspace" || f.Kind == "home") && cleanPath(f.Path) != "" {
		out.OpenFile = &protocol.AssistantViewFile{Kind: f.Kind, Path: cleanPath(f.Path), RunID: cleanID(f.RunID), WorkspaceID: cleanID(f.WorkspaceID)}
	}
	for _, e := range v.Recent {
		if e = cleanEntry(e); e.Name != "" && len(out.Recent) < maxViewRecent {
			out.Recent = append(out.Recent, e)
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
		default:
			return nil, fmt.Errorf("view_context is %d bytes with only its route; the limit is %d", len(b), maxViewContextBytes)
		}
	}
}

func cleanEntry(e protocol.AssistantViewEntry) protocol.AssistantViewEntry {
	return protocol.AssistantViewEntry{
		Name: cleanWord(e.Name), WorkspaceID: cleanID(e.WorkspaceID), RunID: cleanID(e.RunID), MissionID: cleanID(e.MissionID),
		Subagent: cleanID(e.Subagent), View: cleanWord(e.View), At: e.At.UTC(),
	}
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
		block := acp.TextBlock("\n\n<aether-view-context>\n" + string(b) + "\n</aether-view-context>")
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

// keepView makes v the member's latest view context, ordered by when it
// arrived, and returns it; it returns nil and keeps nothing while the member
// has sharing off.
func (s *Service) keepView(ctx context.Context, m *member, v *protocol.AssistantViewContext) (*protocol.AssistantViewContext, error) {
	if v == nil {
		return nil, nil
	}
	m.settings.Lock()
	defer m.settings.Unlock()
	on, err := s.cfg.Store.AssistantShareView(ctx, m.id)
	if err != nil {
		return nil, fmt.Errorf("assistant: read the view sharing setting: %w", err)
	}
	if !on {
		return nil, nil
	}
	m.mu.Lock()
	m.view, m.viewAt = v, time.Now()
	m.mu.Unlock()
	return v, nil
}

// SetView replaces the member's latest view context. It is kept in memory
// only.
func (s *Service) SetView(ctx context.Context, id domain.MemberID, v *protocol.AssistantViewContext) error {
	_, err := s.keepView(ctx, s.member(id), v)
	return err
}

// SetShareView stores whether the member shares their view context; off
// forgets the one held, and the setting orders with keepView so an update
// already on its way cannot bring it back.
func (s *Service) SetShareView(ctx context.Context, id domain.MemberID, on bool) error {
	m := s.member(id)
	defer m.notify()
	m.settings.Lock()
	defer m.settings.Unlock()
	if err := s.cfg.Store.SetAssistantShareView(ctx, id, on); err != nil {
		return fmt.Errorf("assistant: %w", err)
	}
	if !on {
		m.clearView()
	}
	return nil
}

func (m *member) clearView() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.view, m.viewAt = nil, time.Time{}
}

// View is the member's latest view context, its age since it arrived, and
// whether a dashboard streams their assistant now.
func (s *Service) View(id domain.MemberID, now time.Time) protocol.AssistantViewContextResult {
	m := s.member(id)
	m.mu.Lock()
	out := protocol.AssistantViewContextResult{ViewContext: m.view}
	if m.view != nil {
		out.AgeSeconds = int64(max(now.Sub(m.viewAt), 0) / time.Second)
	}
	m.mu.Unlock()
	m.wmu.Lock()
	out.Connected = len(m.watchers) > 0
	m.wmu.Unlock()
	return out
}
