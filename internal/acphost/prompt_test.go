package acphost

import (
	"context"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/acphost/acpmock"
)

// CurrentPrompt is the member's message only while the turn it started
// runs: not once it ended, nor while the agent works on its own.
func TestCurrentPromptOnlyDuringItsTurn(t *testing.T) {
	mockDelay(t, 10*time.Second)
	s, rec := startReal(t, Config{ClaudeCode: true})
	if _, ok := s.CurrentPrompt(); ok {
		t.Fatal("a new session reports a prompt")
	}
	go func() { _, _ = s.Prompt(context.Background(), textPrompt(acpmock.PromptWait), false, nil) }()
	deadline := time.Now().Add(5 * time.Second)
	msg, ok := s.CurrentPrompt()
	for !ok && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
		msg, ok = s.CurrentPrompt()
	}
	if !ok || msg.Text != acpmock.PromptWait || msg.MessageID == "" || msg.Turn != 1 {
		t.Fatalf("during the turn = %+v, %v", msg, ok)
	}
	if err := s.Cancel(context.Background()); err != nil {
		t.Fatal(err)
	}
	rec.waitStates(t, "working:prompt", "idle:cancelled")
	if _, ok = s.CurrentPrompt(); ok {
		t.Fatal("a cancelled turn still reports its prompt")
	}

	if _, err := s.Prompt(context.Background(), textPrompt(acpmock.PromptBackground), false, nil); err != nil {
		t.Fatal(err)
	}
	rec.waitStates(t, "working:prompt", "idle:cancelled", "working:prompt", "working:background")
	if _, ok = s.CurrentPrompt(); ok {
		t.Fatal("background work after the turn reports its prompt")
	}
}
