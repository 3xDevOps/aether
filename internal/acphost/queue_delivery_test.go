package acphost

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"
)

type deliveries chan error

func (d deliveries) report(err error) { d <- err }

func (d deliveries) next(t *testing.T) error {
	t.Helper()
	select {
	case err := <-d:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("no delivery report")
		return nil
	}
}

func (d deliveries) none(t *testing.T) {
	t.Helper()
	select {
	case err := <-d:
		t.Fatalf("reported before the agent took the prompt: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestQueuedPromptReportsDeliveryOrRefusal(t *testing.T) {
	m := newMockAgent(t, loadFixture(t, "claude"))
	release := make(chan struct{})
	m.onPrompt = func(m *mockAgent, call promptCall) (any, *acp.RequestError) {
		switch {
		case strings.Contains(string(call.params.Prompt), "first"):
			<-release
		case strings.Contains(string(call.params.Prompt), "refused"):
			return nil, &acp.RequestError{Code: -32000, Message: "Authentication required"}
		}
		m.update(map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "ok"}})
		return map[string]any{"stopReason": "end_turn"}, nil
	}
	s, rec := startMock(t, m, Config{})
	if _, err := s.Prompt(context.Background(), textPrompt("first"), false, nil); err != nil {
		t.Fatal(err)
	}
	taken, refused := make(deliveries, 1), make(deliveries, 1)
	if r, err := s.Prompt(context.Background(), textPrompt("taken"), false, taken.report); err != nil || r.Outcome != OutcomeQueued {
		t.Fatalf("taken: %+v %v", r, err)
	}
	if r, err := s.Prompt(context.Background(), textPrompt("refused"), false, refused.report); err != nil || r.Outcome != OutcomeQueued {
		t.Fatalf("refused: %+v %v", r, err)
	}
	taken.none(t)
	close(release)
	if err := taken.next(t); err != nil {
		t.Fatalf("taken prompt: %v", err)
	}
	if err := refused.next(t); err == nil || !strings.Contains(err.Error(), "the agent refused the prompt") || !strings.Contains(err.Error(), "Authentication required") {
		t.Fatalf("refused prompt: %v", err)
	}
	rec.waitIdle(t)
}

func TestQueuedPromptReportsTheConnectionClosing(t *testing.T) {
	m := newMockAgent(t, loadFixture(t, "codex"))
	started := make(chan struct{})
	m.onPrompt = func(m *mockAgent, call promptCall) (any, *acp.RequestError) {
		m.update(map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "working"}})
		close(started)
		<-call.ctx.Done()
		return nil, acp.NewRequestCancelled(nil)
	}
	s, _ := startMock(t, m, Config{})
	if _, err := s.Prompt(context.Background(), textPrompt("first"), false, nil); err != nil {
		t.Fatal(err)
	}
	<-started
	lost := make(deliveries, 1)
	if r, err := s.Prompt(context.Background(), textPrompt("second"), false, lost.report); err != nil || r.Outcome != OutcomeQueued {
		t.Fatalf("second: %+v %v", r, err)
	}
	_ = m.stdout.Close()
	if err := lost.next(t); !errors.Is(err, ErrClosed) {
		t.Fatalf("lost prompt: %v", err)
	}
}

func TestQueuedPromptReportsABrokenAgentInputAsClosed(t *testing.T) {
	m := newMockAgent(t, loadFixture(t, "codex"))
	release := make(chan struct{})
	m.onPrompt = func(m *mockAgent, call promptCall) (any, *acp.RequestError) {
		m.update(map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "working"}})
		<-release
		return map[string]any{"stopReason": "end_turn"}, nil
	}
	s, _ := startMock(t, m, Config{})
	if _, err := s.Prompt(context.Background(), textPrompt("first"), false, nil); err != nil {
		t.Fatal(err)
	}
	lost := make(deliveries, 1)
	if r, err := s.Prompt(context.Background(), textPrompt("second"), false, lost.report); err != nil || r.Outcome != OutcomeQueued {
		t.Fatalf("second: %+v %v", r, err)
	}
	_ = m.stdin.Close()
	close(release)
	if err := lost.next(t); !errors.Is(err, ErrClosed) {
		t.Fatalf("lost prompt: %v", err)
	}
}

func TestBrokenAgentInputSettlesTheQueueWhileItsOutputStaysOpen(t *testing.T) {
	m := newMockAgent(t, loadFixture(t, "codex"))
	release := make(chan struct{})
	m.onPrompt = func(m *mockAgent, call promptCall) (any, *acp.RequestError) {
		m.update(map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "working"}})
		<-release
		return map[string]any{"stopReason": "end_turn"}, nil
	}
	path := filepath.Join(t.TempDir(), "run.items.jsonl")
	s, _ := startMock(t, m, Config{LogPath: path})
	if _, err := s.Prompt(context.Background(), textPrompt("first"), false, nil); err != nil {
		t.Fatal(err)
	}
	sent, waiting := make(deliveries, 1), make(deliveries, 1)
	for _, p := range []struct {
		text   string
		report deliveries
	}{{"second", sent}, {"third", waiting}} {
		if r, err := s.Prompt(context.Background(), textPrompt(p.text), false, p.report.report); err != nil || r.Outcome != OutcomeQueued {
			t.Fatalf("%s: %+v %v", p.text, r, err)
		}
	}
	_ = m.stdin.Close()
	close(release)
	for _, d := range []deliveries{sent, waiting} {
		select {
		case err := <-d:
			if !errors.Is(err, ErrClosed) {
				t.Fatalf("lost prompt: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("a queued prompt waited for the agent's output to close")
		}
	}
	<-s.Done()
	log, err := OpenLogReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close() }()
	its, err := log.ReadAfter(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	var notice bool
	for _, it := range its {
		if it.Kind == KindNotice && strings.HasPrefix(it.Notice.Title, "Message not delivered") && it.Notice.Description == "third" {
			notice = true
		}
	}
	if !notice {
		t.Fatal("no Message not delivered notice for the third prompt")
	}
}
