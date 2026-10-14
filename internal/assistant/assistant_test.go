package assistant

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/protocol"
)

func TestResolveNeverSwitchesUnasked(t *testing.T) {
	agents := []protocol.AssistantAgent{
		{Name: "claude", DisplayName: "Claude Code", Reason: "no login was found"},
		{Name: "codex", DisplayName: "Codex", Ready: true},
	}
	_, err := resolve(agents, "", "claude")
	var notReady *AgentNotReadyError
	if !errors.As(err, &notReady) || err.Error() != "Claude Code is not ready: no login was found; sign in again or start with another agent" {
		t.Fatalf("resolve = %v, want the stored agent's reason", err)
	}
	if got, err := resolve(agents, "codex", "claude"); err != nil || got != "codex" {
		t.Fatalf("explicit request = %q, %v, want codex", got, err)
	}
	if got, err := resolve(agents, "", ""); err != nil || got != "codex" {
		t.Fatalf("no stored agent = %q, %v, want the first ready", got, err)
	}
}

func TestPromptKeyIsSharedByDuplicates(t *testing.T) {
	s := New(Config{})
	s.cancel()
	m := s.member("m1")
	m.op.Lock()
	results := make(chan error, 2)
	for range 2 {
		go func() { results <- s.Prompt(t.Context(), "m1", "hi", "k") }()
	}
	time.Sleep(50 * time.Millisecond)
	m.op.Unlock()
	for range 2 {
		select {
		case err := <-results:
			if !errors.Is(err, ErrClosed) {
				t.Fatalf("prompt = %v, want the first attempt's failure for both", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("a duplicate never returned")
		}
	}
	if len(m.attempts) != 0 {
		t.Fatalf("failed key kept: %v", m.attempts)
	}
	a, first := m.begin("ok")
	m.finish("ok", a, nil)
	if _, again := m.begin("ok"); again || !first {
		t.Fatal("an accepted key must not run twice")
	}
}

func TestRemoveDeletesMemberFiles(t *testing.T) {
	dir := t.TempDir()
	s := New(Config{Dir: dir})
	sock := filepath.Join(dir, "m1", "sock")
	if err := os.MkdirAll(sock, 0o755); err != nil {
		t.Fatal(err)
	}
	s.member("m1")
	if err := s.Remove(t.Context(), "m1"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "m1")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("member directory after Remove: %v", err)
	}
	if _, kept := s.members["m1"]; kept {
		t.Fatal("member kept in memory")
	}
}
