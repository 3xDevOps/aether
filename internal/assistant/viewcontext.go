package assistant

import (
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
	// ViewContextOpen and ViewContextClose wrap the view context in a text
	// block for an agent that takes no embedded resources; the dashboard
	// strips them from the transcript.
	ViewContextOpen  = "<aether-view-context>"
	ViewContextClose = "</aether-view-context>"

	viewContextVersion  = 1
	maxViewContextBytes = 4 << 10
	maxViewRecent       = 8
	maxViewNeedsYou     = 5
	maxViewLayout       = 8
	maxViewPath         = 512
)

var (
	viewID   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)
	viewWord = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,39}$`)
)

// CleanViewContext checks a view context from the dashboard: an id, word or
// path that is malformed is dropped, lists are cut to their caps, and the
// result must fit in 4 KiB.
func CleanViewContext(v *protocol.AssistantViewContext, now time.Time) (*protocol.AssistantViewContext, error) {
	if v == nil {
		return nil, nil
	}
	if v.Version != viewContextVersion {
		return nil, fmt.Errorf("view_context version %d is not supported; send version %d", v.Version, viewContextVersion)
	}
	out := &protocol.AssistantViewContext{Version: v.Version, CapturedAt: v.CapturedAt.UTC(), Route: cleanEntry(v.Route)}
	if out.CapturedAt.IsZero() || out.CapturedAt.After(now) {
		out.CapturedAt = now.UTC()
	}
	out.Route.At = time.Time{}
	if f := v.OpenFile; f != nil && (f.Kind == "workspace" || f.Kind == "home") && cleanPath(f.Path) != "" {
		out.OpenFile = &protocol.AssistantViewFile{Kind: f.Kind, Path: cleanPath(f.Path), RunID: cleanID(f.RunID), WorkspaceID: cleanID(f.WorkspaceID)}
	}
	for key, on := range v.Layout {
		if viewWord.MatchString(key) && len(out.Layout) < maxViewLayout {
			if out.Layout == nil {
				out.Layout = make(map[string]bool)
			}
			out.Layout[key] = on
		}
	}
	for _, e := range v.Recent {
		if e = cleanEntry(e); e.Name != "" && len(out.Recent) < maxViewRecent {
			out.Recent = append(out.Recent, e)
		}
	}
	if p := v.Pending; p != nil {
		out.Pending = &protocol.AssistantViewPending{Approvals: max(p.Approvals, 0), AssistantNotices: max(p.AssistantNotices, 0)}
		for _, n := range p.NeedsYou {
			if id := cleanID(n.RunID); id != "" && len(out.Pending.NeedsYou) < maxViewNeedsYou {
				out.Pending.NeedsYou = append(out.Pending.NeedsYou, protocol.AssistantViewNeedsYou{RunID: id, Reason: cleanWord(n.Reason)})
			}
		}
	}
	b, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("encode view_context: %w", err)
	}
	if len(b) > maxViewContextBytes {
		return nil, fmt.Errorf("view_context is %d bytes; the limit is %d", len(b), maxViewContextBytes)
	}
	return out, nil
}

func cleanEntry(e protocol.AssistantViewEntry) protocol.AssistantViewEntry {
	return protocol.AssistantViewEntry{
		Name: cleanWord(e.Name), WorkspaceID: cleanID(e.WorkspaceID), RunID: cleanID(e.RunID), MissionID: cleanID(e.MissionID),
		Subagent: cleanID(e.Subagent), View: cleanWord(e.View), Path: cleanPath(e.Path), At: e.At.UTC(),
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

// viewBlock carries v to the agent as an embedded resource, which the
// transcript lists as an attachment without its text, or, for an agent that
// takes none, as delimited text the dashboard strips.
func viewBlock(v *protocol.AssistantViewContext, embedded bool) (acp.ContentBlock, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return acp.ContentBlock{}, fmt.Errorf("assistant: encode the view context: %w", err)
	}
	if !embedded {
		return acp.TextBlock("\n\n" + ViewContextOpen + "\n" + string(b) + "\n" + ViewContextClose), nil
	}
	mime := "application/json"
	return acp.ResourceBlock(acp.EmbeddedResourceResource{TextResourceContents: &acp.TextResourceContents{
		Uri: ViewContextURI, MimeType: &mime, Text: string(b),
	}}), nil
}

func promptBlocks(text string, v *protocol.AssistantViewContext, embedded bool) ([]acp.ContentBlock, error) {
	blocks := []acp.ContentBlock{acp.TextBlock(text)}
	if v == nil {
		return blocks, nil
	}
	block, err := viewBlock(v, embedded)
	if err != nil {
		return nil, err
	}
	return append(blocks, block), nil
}

// SetView replaces the member's latest view context unless the one held was
// captured later, so a replayed or late update from another tab loses; nil
// clears it. It is kept in memory only.
func (s *Service) SetView(id domain.MemberID, v *protocol.AssistantViewContext) {
	m := s.member(id)
	m.mu.Lock()
	defer m.mu.Unlock()
	if v == nil || m.view == nil || !m.view.CapturedAt.After(v.CapturedAt) {
		m.view = v
	}
}

// View is the member's latest view context, its age, and whether a
// dashboard streams their assistant now.
func (s *Service) View(id domain.MemberID, now time.Time) protocol.AssistantViewContextResult {
	m := s.member(id)
	m.mu.Lock()
	out := protocol.AssistantViewContextResult{ViewContext: m.view}
	m.mu.Unlock()
	if out.ViewContext != nil {
		out.AgeSeconds = int64(max(now.Sub(out.ViewContext.CapturedAt), 0) / time.Second)
	}
	m.wmu.Lock()
	out.Connected = len(m.watchers) > 0
	m.wmu.Unlock()
	return out
}
