package assistant

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"github.com/3xDevOps/Aether/internal/acphost"
	"github.com/3xDevOps/Aether/internal/acphost/acpmock"
	"github.com/3xDevOps/Aether/internal/assistant/tools"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
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
	far := &protocol.AssistantViewContext{Version: 1, CapturedAt: time.Date(9999, 12, 31, 23, 0, 0, 0, time.FixedZone("", -2*3600)), Route: protocol.AssistantViewEntry{Name: "board"}}
	if got, err := CleanViewContext(far); err != nil || !got.CapturedAt.IsZero() {
		t.Fatalf("a time past year 9999 in UTC = %+v, %v, want it zeroed", got, err)
	}
	if got, err := CleanViewContext(&protocol.AssistantViewContext{Version: 2}); got != nil || err != nil {
		t.Fatalf("an unknown version = %+v, %v, want it dropped", got, err)
	}
}

func TestViewIsPerMemberAndOptOutHolds(t *testing.T) {
	s := New(Config{Store: newMemStore()})
	s.cancel()
	ctx := t.Context()
	now := time.Now()
	v := &protocol.AssistantViewContext{Version: 1, CapturedAt: now, Route: protocol.AssistantViewEntry{Name: "board"}}
	if err := s.SetView(ctx, "m1", v); err != nil {
		t.Fatal(err)
	}
	if got := s.View("m1", now); got.ViewContext != v || got.Source != viewFromNavigation || got.Connected {
		t.Fatalf("View(m1) = %+v, want the navigation update and no dashboard", got)
	}
	if got := s.View("m2", now); got.ViewContext != nil {
		t.Fatalf("View(m2) = %+v, want another member's context kept from them", got.ViewContext)
	}

	if err := s.SetShareView(ctx, "m1", false); err != nil {
		t.Fatal(err)
	}
	if err := s.SetView(ctx, "m1", v); err != nil {
		t.Fatal(err)
	}
	if got := s.View("m1", now); got.ViewContext != nil {
		t.Fatalf("View after sharing was turned off and a late update = %+v, want none", got.ViewContext)
	}
	if kept, err := s.sharedView(ctx, s.member("m1"), v); kept != nil || err != nil {
		t.Fatalf("a prompt's context with sharing off = %+v, %v, want it dropped", kept, err)
	}

	if err := s.SetShareView(ctx, "m1", true); err != nil {
		t.Fatal(err)
	}
	if err := s.Fence(ctx, "m1"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetView(ctx, "m1", v); err != nil {
		t.Fatal(err)
	}
	if got := s.View("m1", now); got.ViewContext != nil {
		t.Fatalf("View after Fence and a late update = %+v, want none", got.ViewContext)
	}
}

// noAgents fails the agent list, so no session can start.
type noAgents struct{}

func (noAgents) Call(context.Context, string, json.RawMessage) (json.RawMessage, *protocol.Error) {
	return nil, &protocol.Error{Code: protocol.CodeInternal, Message: "agent list unavailable"}
}

func TestPromptViewHoldsOnceSentUntilANewerNavigation(t *testing.T) {
	db := newMemStore()
	db.settings["m1"] = store.AssistantSettings{ShareView: true}
	s := New(Config{Dir: t.TempDir(), Store: db, Caller: func(domain.MemberID) tools.Caller { return noAgents{} }})
	t.Cleanup(func() { _ = s.Close() })
	ctx := t.Context()
	m := s.member("m1")
	t0 := time.Now()
	view := func(run string, captured time.Time) *protocol.AssistantViewContext {
		return &protocol.AssistantViewContext{Version: 1, CapturedAt: captured, Route: protocol.AssistantViewEntry{Name: "run", RunID: run}}
	}
	held := func(at time.Time) protocol.AssistantViewContextResult {
		m.mu.Lock()
		defer m.mu.Unlock()
		return protocol.AssistantViewContextResult{ViewContext: m.view, Source: m.viewSource, AgeSeconds: int64(at.Sub(m.viewAt) / time.Second)}
	}

	first := view("run-a", t0)
	m.holdView(first, viewFromNavigation, t0)
	if err := s.Prompt(ctx, "m1", "fix this", "k0", view("run-b", t0.Add(2*time.Second))); err == nil || !strings.Contains(err.Error(), "agent list unavailable") {
		t.Fatalf("Prompt = %v, want the failed session start", err)
	}
	if got := held(t0); got.ViewContext != first || got.Source != viewFromNavigation {
		t.Fatalf("held after a prompt that never started = %+v, want the navigation update", got)
	}

	fix, err := acpmock.Load("claude")
	if err != nil {
		t.Fatal(err)
	}
	agentIn, hostOut := io.Pipe()
	hostIn, agentOut := io.Pipe()
	go acpmock.New(fix).Serve(agentIn, agentOut)
	sess, err := acphost.Start(ctx, hostIn, hostOut, acphost.Config{LogPath: s.logPath("m1"), Cwd: t.TempDir(), Logger: slog.Default()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		m.mu.Lock()
		m.sess = nil
		m.mu.Unlock()
		_ = sess.Close()
		_ = agentOut.Close()
		_ = agentIn.Close()
	})
	m.mu.Lock()
	m.sess = &session{acp: sess, ended: make(chan struct{})}
	m.mu.Unlock()

	prompt := view("run-b", t0.Add(2*time.Second))
	if err := s.Prompt(ctx, "m1", "fix this", "k1", prompt); err != nil {
		t.Fatal(err)
	}
	sent := time.Now()
	if got := held(sent); got.ViewContext != prompt || got.Source != viewFromPrompt {
		t.Fatalf("held after the prompt = %+v, want the prompt's view", got)
	}

	m.holdView(view("run-a", t0.Add(time.Second)), viewFromNavigation, sent.Add(time.Second))
	if got := held(sent); got.ViewContext != prompt {
		t.Fatalf("held after an update captured before the prompt = %+v, want the prompt's view", got.ViewContext.Route)
	}

	newer := view("run-c", t0.Add(4*time.Second))
	m.holdView(newer, viewFromNavigation, sent.Add(2*time.Second))
	if got := held(sent.Add(7 * time.Second)); got.ViewContext != newer || got.Source != viewFromNavigation || got.AgeSeconds != 5 {
		t.Fatalf("held after a newer navigation = %+v, want it, 5s old", got)
	}

	slow := view("run-d", t0.Add(-time.Hour))
	m.holdView(slow, viewFromNavigation, sent.Add(2*time.Second+viewRace))
	if got := held(sent); got.ViewContext != slow {
		t.Fatalf("held after a slow device's update past the race = %+v, want it", got.ViewContext.Route)
	}

	if err := s.Prompt(ctx, "m1", "fix this", "k1", view("run-old", t0)); err != nil {
		t.Fatalf("a prompt sent before = %v, want it skipped", err)
	}
	if got := held(sent); got.ViewContext != slow {
		t.Fatalf("held after a retried prompt = %+v, want it unchanged", got.ViewContext.Route)
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
