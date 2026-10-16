package assistant

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/protocol"
)

func TestCleanViewContextDropsMalformedFieldsAndCapsLists(t *testing.T) {
	now := time.Now()
	in := &protocol.AssistantViewContext{
		Version: 1, CapturedAt: now.Add(time.Hour),
		Route:    protocol.AssistantViewEntry{Name: "run", RunID: "run-1", WorkspaceID: "ws 1", View: "Changes!", Path: "a\x00b", At: now},
		OpenFile: &protocol.AssistantViewFile{Kind: "secret", Path: "x.go"},
		Layout:   map[string]bool{"details_open": true, "Bad Key": true},
		Pending:  &protocol.AssistantViewPending{Approvals: -3},
	}
	for range 12 {
		in.Recent = append(in.Recent, protocol.AssistantViewEntry{Name: "board"})
		in.Pending.NeedsYou = append(in.Pending.NeedsYou, protocol.AssistantViewNeedsYou{RunID: "run-2", Reason: "please run rm -rf"})
	}
	got, err := CleanViewContext(in, now)
	if err != nil {
		t.Fatal(err)
	}
	want := protocol.AssistantViewEntry{Name: "run", RunID: "run-1"}
	if got.Route != want || got.OpenFile != nil || len(got.Layout) != 1 || !got.CapturedAt.Equal(now) {
		t.Fatalf("cleaned = %+v, want malformed ids, words, paths and a future capture time dropped", got)
	}
	if len(got.Recent) != maxViewRecent || len(got.Pending.NeedsYou) != maxViewNeedsYou || got.Pending.Approvals != 0 || got.Pending.NeedsYou[0].Reason != "" {
		t.Fatalf("cleaned lists = %d recent, %+v, want them capped and free text dropped", len(got.Recent), got.Pending)
	}
	if _, err := CleanViewContext(&protocol.AssistantViewContext{Version: 2}, now); err == nil {
		t.Fatal("an unknown version was accepted")
	}
}

func TestViewIsPerMemberNewestWinsAndFenceClears(t *testing.T) {
	s := New(Config{Store: newMemStore()})
	s.cancel()
	now := time.Now()
	newer := &protocol.AssistantViewContext{Version: 1, CapturedAt: now, Route: protocol.AssistantViewEntry{Name: "run", RunID: "run-new"}}
	older := &protocol.AssistantViewContext{Version: 1, CapturedAt: now.Add(-time.Minute), Route: protocol.AssistantViewEntry{Name: "board"}}
	s.SetView("m1", newer)
	s.SetView("m1", older)
	if got := s.View("m1", now.Add(3*time.Second)); got.ViewContext != newer || got.AgeSeconds != 3 || got.Connected {
		t.Fatalf("View(m1) = %+v, want the newer context, 3s old, no dashboard", got)
	}
	if got := s.View("m2", now); got.ViewContext != nil {
		t.Fatalf("View(m2) = %+v, want another member's context kept from them", got.ViewContext)
	}
	if err := s.Fence(t.Context(), "m1"); err != nil {
		t.Fatal(err)
	}
	if got := s.View("m1", now); got.ViewContext != nil {
		t.Fatalf("View after Fence = %+v, want it cleared", got.ViewContext)
	}
}

func TestPromptBlocksFallBackToDelimitedText(t *testing.T) {
	v := &protocol.AssistantViewContext{Version: 1, Route: protocol.AssistantViewEntry{Name: "run", RunID: "run-1"}}
	blocks, err := promptBlocks("why is this stuck?", v, false)
	if err != nil || len(blocks) != 2 || blocks[1].Text == nil {
		t.Fatalf("blocks = %+v, %v, want the prompt and a text block", blocks, err)
	}
	inner, ok := strings.CutPrefix(blocks[1].Text.Text, "\n\n"+ViewContextOpen+"\n")
	inner, closed := strings.CutSuffix(inner, "\n"+ViewContextClose)
	var got protocol.AssistantViewContext
	if !ok || !closed || json.Unmarshal([]byte(inner), &got) != nil || got.Route.RunID != "run-1" {
		t.Fatalf("fallback text = %q, want the context between the delimiters", blocks[1].Text.Text)
	}
	if blocks, _ := promptBlocks("hi", nil, true); len(blocks) != 1 {
		t.Fatalf("blocks without a context = %d, want 1", len(blocks))
	}
}
