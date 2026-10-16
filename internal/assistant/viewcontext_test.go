package assistant

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"github.com/3xDevOps/Aether/internal/protocol"
)

func TestCleanViewContextDropsMalformedFieldsAndTrimsToFit(t *testing.T) {
	in := &protocol.AssistantViewContext{
		Version:  1,
		Route:    protocol.AssistantViewEntry{Name: "run", RunID: "run-1", WorkspaceID: "ws 1", View: "Changes!", At: time.Now()},
		OpenFile: &protocol.AssistantViewFile{Kind: "secret", Path: "x.go"},
	}
	for range 12 {
		in.Recent = append(in.Recent, protocol.AssistantViewEntry{Name: "board"})
	}
	got, err := CleanViewContext(in)
	if err != nil {
		t.Fatal(err)
	}
	if want := (protocol.AssistantViewEntry{Name: "run", RunID: "run-1"}); got.Route != want || got.OpenFile != nil || len(got.Recent) != maxViewRecent {
		t.Fatalf("cleaned = %+v, want malformed ids, words and files dropped and recent capped", got)
	}

	in.OpenFile = &protocol.AssistantViewFile{Kind: "workspace", Path: strings.Repeat("p", maxViewPath)}
	in.Recent = nil
	for range maxViewRecent {
		in.Recent = append(in.Recent, protocol.AssistantViewEntry{Name: "run", RunID: strings.Repeat("r", 128), MissionID: strings.Repeat("m", 128), Subagent: strings.Repeat("s", 128)})
	}
	got, err = CleanViewContext(in)
	b, _ := json.Marshal(got)
	if err != nil || len(b) > maxViewContextBytes || got.OpenFile == nil || len(got.Recent) == 0 || len(got.Recent) == maxViewRecent {
		t.Fatalf("oversized context = %d bytes, %d recent, %v, want the oldest pages trimmed to fit", len(b), len(got.Recent), err)
	}
	if got, err := CleanViewContext(&protocol.AssistantViewContext{Version: 2}); got != nil || err != nil {
		t.Fatalf("an unknown version = %+v, %v, want it dropped", got, err)
	}
}

func TestViewIsPerMemberLastReceivedWinsAndOptOutHolds(t *testing.T) {
	s := New(Config{Store: newMemStore()})
	s.cancel()
	ctx := t.Context()
	now := time.Now()
	newer := &protocol.AssistantViewContext{Version: 1, CapturedAt: now, Route: protocol.AssistantViewEntry{Name: "board"}}
	skewed := &protocol.AssistantViewContext{Version: 1, CapturedAt: now.Add(-time.Hour), Route: protocol.AssistantViewEntry{Name: "run", RunID: "run-new"}}
	for _, v := range []*protocol.AssistantViewContext{newer, skewed} {
		if err := s.SetView(ctx, "m1", v); err != nil {
			t.Fatal(err)
		}
	}
	if got := s.View("m1", time.Now()); got.ViewContext != skewed || got.Connected {
		t.Fatalf("View(m1) = %+v, want the one received last, whatever its clock, and no dashboard", got)
	}
	if got := s.View("m2", now); got.ViewContext != nil {
		t.Fatalf("View(m2) = %+v, want another member's context kept from them", got.ViewContext)
	}

	if err := s.SetShareView(ctx, "m1", false); err != nil {
		t.Fatal(err)
	}
	if err := s.SetView(ctx, "m1", newer); err != nil {
		t.Fatal(err)
	}
	if got := s.View("m1", now); got.ViewContext != nil {
		t.Fatalf("View after sharing was turned off and a late update = %+v, want none", got.ViewContext)
	}
	if kept, err := s.keepView(ctx, s.member("m1"), newer); kept != nil || err != nil {
		t.Fatalf("a prompt's context with sharing off = %+v, %v, want it dropped", kept, err)
	}

	if err := s.SetShareView(ctx, "m1", true); err != nil {
		t.Fatal(err)
	}
	if err := s.SetView(ctx, "m1", newer); err != nil {
		t.Fatal(err)
	}
	if err := s.Fence(ctx, "m1"); err != nil {
		t.Fatal(err)
	}
	if got := s.View("m1", now); got.ViewContext != nil {
		t.Fatalf("View after Fence = %+v, want it cleared", got.ViewContext)
	}
}

func TestPromptBlocksKeepTheViewContextForTheAgent(t *testing.T) {
	v := &protocol.AssistantViewContext{Version: 1, Route: protocol.AssistantViewEntry{Name: "run", RunID: "run-1"}}
	for _, embedded := range []bool{true, false} {
		blocks, err := promptBlocks("why is this stuck?", v, embedded)
		if err != nil || len(blocks) != 2 || blocks[0].Text.Text != "why is this stuck?" {
			t.Fatalf("embedded=%v: blocks = %+v, %v", embedded, blocks, err)
		}
		var a *acp.Annotations
		if embedded {
			a = blocks[1].Resource.Annotations
		} else {
			a = blocks[1].Text.Annotations
			if !strings.Contains(blocks[1].Text.Text, `"run_id":"run-1"`) {
				t.Fatalf("fallback text = %q, want the context", blocks[1].Text.Text)
			}
		}
		if a == nil || !slices.Equal(a.Audience, []acp.Role{acp.RoleAssistant}) {
			t.Fatalf("embedded=%v: annotations = %+v, want the agent as the only audience", embedded, a)
		}
	}
	if blocks, _ := promptBlocks("hi", nil, true); len(blocks) != 1 {
		t.Fatalf("blocks without a context = %d, want 1", len(blocks))
	}
}
