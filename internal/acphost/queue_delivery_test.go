package acphost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"
)

type deliveries chan error

func (d deliveries) report(_ bool, err error) { d <- err }

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

func TestPromptsAreReportedInTheOrderTheAgentTookThem(t *testing.T) {
	m := newMockAgent(t, loadFixture(t, "claude"))
	release := make(chan struct{})
	m.onPrompt = func(_ *mockAgent, call promptCall) (any, *acp.RequestError) {
		if strings.Contains(string(call.params.Prompt), "first") {
			<-release
		}
		return map[string]any{"stopReason": "end_turn"}, nil
	}
	m.onSteer = func(*mockAgent, json.RawMessage) (any, *acp.RequestError) {
		return map[string]any{"outcome": OutcomeInjected}, nil
	}
	s, rec := startMock(t, m, Config{})
	reports := make(chan string, 3)
	report := func(name string) func(bool, error) {
		return func(queued bool, err error) { reports <- fmt.Sprintf("%s queued=%v err=%v", name, queued, err) }
	}
	next := func(want string) {
		t.Helper()
		select {
		case got := <-reports:
			if got != want {
				t.Fatalf("report %q, want %q", got, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("no report, want %q", want)
		}
	}

	go func() { _, _ = s.Prompt(context.Background(), textPrompt("first"), false, report("first")) }()
	for deadline := time.Now().Add(5 * time.Second); !m.called(acp.AgentMethodSessionPrompt); {
		if time.Now().After(deadline) {
			t.Fatal("the first prompt never reached the agent")
		}
		time.Sleep(time.Millisecond)
	}
	if r, err := s.Prompt(context.Background(), textPrompt("steered"), true, report("steered")); err != nil || r.Outcome != OutcomeInjected {
		t.Fatalf("steered: %+v %v", r, err)
	}
	if r, err := s.Prompt(context.Background(), textPrompt("queued"), false, report("queued")); err != nil || r.Outcome != OutcomeQueued {
		t.Fatalf("queued: %+v %v", r, err)
	}
	next("first queued=false err=<nil>")
	next("steered queued=false err=<nil>")
	select {
	case got := <-reports:
		t.Fatalf("reported before the agent took the queued prompt: %s", got)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	next("queued queued=true err=<nil>")
	rec.waitIdle(t)
}

// The agent may answer a steer only after the turn it joined has ended and the
// next queued turn has started; that answer says nothing about the new turn.
func TestLateSteerAnswerDoesNotAcceptTheNextTurn(t *testing.T) {
	m := newMockAgent(t, loadFixture(t, "claude"))
	releaseFirst, answerSteer, refuseQueued := make(chan struct{}), make(chan struct{}), make(chan struct{})
	m.onPrompt = func(m *mockAgent, call promptCall) (any, *acp.RequestError) {
		if strings.Contains(string(call.params.Prompt), "queued") {
			<-refuseQueued
			return nil, &acp.RequestError{Code: -32000, Message: "Authentication required"}
		}
		m.update(map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "working"}})
		<-releaseFirst
		return map[string]any{"stopReason": "end_turn"}, nil
	}
	m.onSteer = func(*mockAgent, json.RawMessage) (any, *acp.RequestError) {
		<-answerSteer
		return map[string]any{"outcome": OutcomeInjected}, nil
	}
	s, rec := startMock(t, m, Config{})
	if _, err := s.Prompt(context.Background(), textPrompt("first"), false, nil); err != nil {
		t.Fatal(err)
	}
	queued := make(deliveries, 1)
	if r, err := s.Prompt(context.Background(), textPrompt("queued"), false, queued.report); err != nil || r.Outcome != OutcomeQueued {
		t.Fatalf("queued: %+v %v", r, err)
	}
	steered := make(chan error, 1)
	go func() {
		_, err := s.Prompt(context.Background(), textPrompt("steered"), true, nil)
		steered <- err
	}()
	received := func(method string, n int) {
		t.Helper()
		for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
			m.mu.Lock()
			got := 0
			for _, called := range m.methods {
				if called == method {
					got++
				}
			}
			m.mu.Unlock()
			if got >= n {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("the agent received %d %s, want %d", got, method, n)
			}
		}
	}
	received(methodSteering, 1)
	close(releaseFirst)
	received(acp.AgentMethodSessionPrompt, 2)
	close(answerSteer)
	if err := <-steered; err != nil {
		t.Fatal(err)
	}
	queued.none(t)
	close(refuseQueued)
	if err := queued.next(t); err == nil || !strings.Contains(err.Error(), "Authentication required") {
		t.Fatalf("queued prompt reported %v, want the agent's refusal", err)
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
