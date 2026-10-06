package acphost

import (
	"context"
	"errors"
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
