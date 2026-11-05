package sshd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/assistant"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// askAssistant holds one queue entry per key, which runs until done is
// set.
type askAssistant struct {
	AssistantService
	mu      sync.Mutex
	entries map[string]*assistant.Entry
	keys    map[string]string
	prompts int
	sent    []string
}

func (a *askAssistant) Prompt(ctx context.Context, _ domain.MemberID, text, key string, _ *protocol.AssistantViewContext, origin assistant.Origin) (assistant.Entry, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sent = append(a.sent, key)
	if text == "overlapping retry" {
		// Leaves room for an overlapping retry's lookup before the entry
		// exists.
		a.mu.Unlock()
		time.Sleep(20 * time.Millisecond)
		a.mu.Lock()
	}
	if text == "slow start" {
		a.mu.Unlock()
		<-ctx.Done()
		a.mu.Lock()
		e := &assistant.Entry{AssistantQueueEntry: protocol.AssistantQueueEntry{ID: "turn-slow", State: protocol.AssistantQueueQueued, Text: text}, Origin: origin}
		a.entries[e.ID] = e
		return *e, nil
	}
	if text == "fail" {
		return assistant.Entry{}, errors.New("assistant: no agent is ready; sign in")
	}
	if id, ok := a.keys[key]; ok {
		return *a.entries[id], nil
	}
	a.prompts++
	e := &assistant.Entry{AssistantQueueEntry: protocol.AssistantQueueEntry{ID: fmt.Sprintf("turn-%d", a.prompts), State: protocol.AssistantQueueRunning, Text: text}, Origin: origin}
	a.entries[e.ID], a.keys[key] = e, e.ID
	return *e, nil
}

func (a *askAssistant) GrantTurn(_ context.Context, _ domain.MemberID, grant, text string, _ time.Time) (assistant.Entry, bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, e := range a.entries {
		if e.Text == text && e.Origin.Grant == grant && (e.State == protocol.AssistantQueueQueued || e.State == protocol.AssistantQueueRunning) {
			return *e, true, nil
		}
	}
	return assistant.Entry{}, false, nil
}

func (a *askAssistant) Entry(_ context.Context, _ domain.MemberID, id string) (assistant.Entry, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	e, ok := a.entries[id]
	if !ok {
		return assistant.Entry{}, fmt.Errorf("%w: %s", assistant.ErrNoEntry, id)
	}
	return *e, nil
}

func (a *askAssistant) ConnectorReply(_ context.Context, _ domain.MemberID, reply string) (string, error) {
	return "redacted " + reply, nil
}

func (a *askAssistant) finish(id string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.entries[id].State, a.entries[id].Reply = protocol.AssistantQueueDone, "all done"
}

// A turn still running when assistant.ask stops waiting returns its
// turn_id; polling it from the same connector returns the reply once done,
// and no other connector or door reaches it.
func TestAssistantAskWaitsThenPolls(t *testing.T) {
	wait := askWait
	askWait = 50 * time.Millisecond
	t.Cleanup(func() { askWait = wait })
	svc := &askAssistant{entries: map[string]*assistant.Entry{}, keys: map[string]string{}}
	e := newTestEnv(t, func(c *Config) { c.Services.Assistant = svc })
	write := []string{ConnectorScopeRead, ConnectorScopeWrite}
	conA := e.srv.ConnectorLocal(ConnectorGrant{Member: e.member.ID}, "con-a", "ChatGPT", write)
	conB := e.srv.ConnectorLocal(ConnectorGrant{Member: e.member.ID}, "con-b", "ChatGPT", write)
	ask := func(l *Local, p protocol.AssistantAskParams) (protocol.AssistantAskResult, *protocol.Error) {
		raw, _ := json.Marshal(p)
		var out protocol.AssistantAskResult
		result, perr := l.Call(t.Context(), protocol.MethodAssistantAsk, raw)
		if perr == nil {
			_ = json.Unmarshal(result, &out)
		}
		return out, perr
	}

	got, perr := ask(conA, protocol.AssistantAskParams{Prompt: "fix the login test"})
	if perr != nil || got.State != protocol.AssistantAskRunning || got.TurnID != "turn-1" || got.Reply != "" {
		t.Fatalf("prompt = %+v, %v, want turn-1 running", got, perr)
	}
	entry := svc.entries["turn-1"]
	if entry.Origin.Door != protocol.AssistantDoorConnector || entry.Origin.Grant != "con-a" || entry.Origin.Client != "ChatGPT" ||
		len(entry.Origin.Scopes) != 2 {
		t.Fatalf("origin = %+v, want the connector's grant and scopes", entry.Origin)
	}
	if got, perr = ask(conA, protocol.AssistantAskParams{Prompt: "fix the login test"}); perr != nil || got.TurnID != "turn-1" || svc.prompts != 1 {
		t.Fatalf("keyless retry = %+v, %v after %d prompts, want turn-1 again", got, perr, svc.prompts)
	}
	if got, perr = ask(conB, protocol.AssistantAskParams{Prompt: "fix the login test"}); perr != nil || got.TurnID == "turn-1" {
		t.Fatalf("the same prompt from another connector = %+v, %v, want a turn of its own", got, perr)
	}
	if _, perr = ask(conB, protocol.AssistantAskParams{TurnID: "turn-1"}); perr == nil || perr.Code != protocol.CodeNotFound ||
		perr.Message != "assistant: no such queued prompt: turn-1" {
		t.Fatalf("another connector polling turn-1 = %v, want the error of a missing id", perr)
	}
	if _, perr = ask(conA, protocol.AssistantAskParams{TurnID: "turn-9"}); perr == nil || perr.Message != "assistant: no such queued prompt: turn-9" {
		t.Fatalf("polling a missing turn = %v", perr)
	}

	svc.finish("turn-1")
	got, perr = ask(conA, protocol.AssistantAskParams{TurnID: "turn-1"})
	if perr != nil || got.State != protocol.AssistantAskDone || got.Reply != "redacted all done" {
		t.Fatalf("poll = %+v, %v, want done with the redacted reply", got, perr)
	}
	if got, perr = ask(conA, protocol.AssistantAskParams{Prompt: "fix the login test"}); perr != nil || got.TurnID == "turn-1" {
		t.Fatalf("asking again after turn-1 ended = %+v, %v, want a new turn", got, perr)
	}
	for range 2 {
		if _, perr = ask(conA, protocol.AssistantAskParams{Prompt: "fail"}); perr == nil || perr.Message != "assistant: no agent is ready; sign in" {
			t.Fatalf("failing prompt = %v, want the service's error", perr)
		}
	}
	if n := len(svc.sent); svc.sent[n-1] == svc.sent[n-2] {
		t.Fatalf("a failed keyless prompt kept its key %s for the retry", svc.sent[n-1])
	}
	var wg sync.WaitGroup
	turns := make(chan string, 2)
	for range 2 {
		wg.Go(func() {
			if res, err := ask(conA, protocol.AssistantAskParams{Prompt: "overlapping retry"}); err == nil {
				turns <- res.TurnID
			}
		})
	}
	wg.Wait()
	close(turns)
	if first, second := <-turns, <-turns; first == "" || first != second {
		t.Fatalf("overlapping keyless retries = %q and %q, want one turn", first, second)
	}
	start := time.Now()
	if got, perr = ask(conA, protocol.AssistantAskParams{Prompt: "slow start"}); perr != nil || got.TurnID != "turn-slow" ||
		got.State != protocol.AssistantAskQueued || time.Since(start) > 10*askWait {
		t.Fatalf("prompt whose start outlasts the wait = %+v, %v after %s, want it queued within the wait", got, perr, time.Since(start))
	}
	if _, perr = ask(conA, protocol.AssistantAskParams{Prompt: "x", TurnID: "turn-1"}); perr == nil || perr.Code != protocol.CodeInvalidParams {
		t.Fatalf("two modes = %v, want invalid params", perr)
	}
}
