package scheduler

import (
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/ptyhost"
)

// codexToken is a Codex access token whose claims name account.
func codexToken(account string) string {
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"alg":"none"}`)) + "." + enc([]byte(`{"https://api.openai.com/auth":{"chatgpt_account_id":"`+account+`"}}`)) + ".c2lnbmF0dXJl"
}

// TestAssistantSignInPinRefusesAnotherAccount pins each sign-in the member
// made and refuses a start once the assistant's own agent swapped it for
// another account's, while a token refresh still starts.
func TestAssistantSignInPinRefusesAnotherAccount(t *testing.T) {
	t.Parallel()
	assistantDir := filepath.Join(t.TempDir(), "assistant")
	e := newTestEnv(t, func(c *Config) {
		c.ServerBinary = fakeServerBinary(t, "current build")
		c.AssistantDir = assistantDir
	})
	withCoordination(t, e)
	home := AssistantHomeDir(assistantDir, e.member.ID)
	start := func(agent string) error {
		_, err := e.sched.freshAssistantContainer(t.Context(), e.member.ID, agent, func() {}, "")
		return err
	}
	signIn := func(agent, rel, content string) {
		t.Helper()
		if err := e.sched.markAssistantSignIn(e.member.ID, agent); err != nil {
			t.Fatal(err)
		}
		writeHomeFile(t, home, rel, content)
		if err := start(agent); err != nil {
			t.Fatalf("the start after the %s sign-in: %v", agent, err)
		}
	}
	signIn("codex", ".codex/auth.json", `{"auth_mode":"chatgpt","tokens":{"access_token":"`+codexToken("acct-alice")+`","refresh_token":"r1"}}`)
	signIn("claude", ".claude/.credentials.json", `{"claudeAiOauth":{"accessToken":"a1","refreshToken":"r1","expiresAt":1,"scopes":["user:inference"],"subscriptionType":"max"}}`)

	for _, tc := range []struct {
		name, agent, rel, content string
		changed                   bool
	}{
		{"codex refresh", "codex", ".codex/auth.json", `{"auth_mode":"chatgpt","tokens":{"access_token":"` + codexToken("acct-alice") + `","refresh_token":"r2"}}`, false},
		{"codex foreign account", "codex", ".codex/auth.json", `{"auth_mode":"chatgpt","tokens":{"access_token":"` + codexToken("acct-mallory") + `","refresh_token":"r3","account_id":"acct-alice"}}`, true},
		{"codex foreign API key", "codex", ".codex/auth.json", `{"auth_mode":"apikey","OPENAI_API_KEY":"sk-synthetic-mallory"}`, true},
		{"claude refresh", "claude", ".claude/.credentials.json", `{"claudeAiOauth":{"accessToken":"a2","refreshToken":"r2","expiresAt":2,"scopes":["user:inference"],"subscriptionType":"max"}}`, false},
		{"claude other plan", "claude", ".claude/.credentials.json", `{"claudeAiOauth":{"accessToken":"a3","refreshToken":"r3","expiresAt":3,"scopes":["user:inference"],"subscriptionType":"pro"}}`, true},
		{"claude other fields", "claude", ".claude/.credentials.json", `{"claudeAiOauth":{"accessToken":"a3","refreshToken":"r3","expiresAt":3,"scopes":["user:inference"]}}`, true},
	} {
		writeHomeFile(t, home, tc.rel, tc.content)
		err := start(tc.agent)
		found, foundErr := e.sched.AssistantLoginFound(e.member.ID, tc.agent)
		if foundErr != nil {
			t.Fatal(foundErr)
		}
		if tc.changed != errors.Is(err, ErrAssistantSignInChanged) || found == tc.changed {
			t.Errorf("%s: start = %v, ready %v; want refused %v", tc.name, err, found, tc.changed)
		}
	}

	if _, err := e.sched.StartAssistantLogin(t.Context(), e.member.ID, "codex", 80, 24); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, ".codex", "auth.json")); !os.IsNotExist(err) {
		t.Fatalf("a sign-in kept the sign-in file the agent left: %v", err)
	}
	if _, err := os.Stat(filepath.Join(assistantSignInDir(assistantDir, e.member.ID), "codex.json")); !os.IsNotExist(err) {
		t.Fatalf("a sign-in kept the record of the earlier one: %v", err)
	}
}

// TestAssistantSignInOverTheCapIsRemoved deletes a sign-in file too large to
// be one before reading it, and refuses that start.
func TestAssistantSignInOverTheCapIsRemoved(t *testing.T) {
	t.Parallel()
	assistantDir := filepath.Join(t.TempDir(), "assistant")
	e := newTestEnv(t, func(c *Config) {
		c.ServerBinary = fakeServerBinary(t, "current build")
		c.AssistantDir = assistantDir
	})
	withCoordination(t, e)
	home := AssistantHomeDir(assistantDir, e.member.ID)
	writeHomeFile(t, home, ".claude/.credentials.json", `{"claudeAiOauth":{"accessToken":"`+strings.Repeat("x", maxSignInBytes)+`"}}`)
	_, err := e.sched.freshAssistantContainer(t.Context(), e.member.ID, "claude", func() {}, "")
	if err == nil || !strings.Contains(err.Error(), "larger than 64 KiB") {
		t.Fatalf("start = %v, want the oversized sign-in refused", err)
	}
	if _, err = os.Stat(filepath.Join(home, ".claude", ".credentials.json")); !os.IsNotExist(err) {
		t.Fatalf("the oversized sign-in is still there: %v", err)
	}
}

// TestAssistantLoginTranscriptsAreRemoved removes a sign-in's transcript,
// which shows its URL and codes, once it no longer runs, and every sign-in's
// with the member's assistant.
func TestAssistantLoginTranscriptsAreRemoved(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, func(c *Config) {
		c.ServerBinary = fakeServerBinary(t, "current build")
		c.AssistantDir = filepath.Join(t.TempDir(), "assistant")
	})
	withCoordination(t, e)
	key, err := e.sched.StartAssistantLogin(t.Context(), e.member.ID, "codex", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	e.pty.removedTranscripts = nil
	if err = e.sched.EndAssistantLogin(t.Context(), e.member.ID, "codex"); err != nil || slices.Contains(e.pty.removedTranscripts, key) {
		t.Fatalf("EndAssistantLogin = %v, removed %v; want a running sign-in's transcript kept", err, e.pty.removedTranscripts)
	}
	if err = e.pty.StopSession(t.Context(), key); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the sign-in to end", func() bool { return len(e.pty.ActiveSessions(string(key))) == 0 })
	if err = e.sched.EndAssistantLogin(t.Context(), e.member.ID, "codex"); err != nil || !slices.Contains(e.pty.removedTranscripts, key) {
		t.Fatalf("EndAssistantLogin = %v, removed %v; want the exited sign-in's transcript removed", err, e.pty.removedTranscripts)
	}
	e.pty.removedTranscripts = nil
	if err = e.sched.RemoveAssistant(t.Context(), e.member.ID); err != nil {
		t.Fatal(err)
	}
	if want := []ptyhost.SessionKey{ptyhost.AssistantLoginSession(e.member.ID, "claude"), key}; !slices.Equal(e.pty.removedTranscripts, want) {
		t.Fatalf("RemoveAssistant removed %v, want %v", e.pty.removedTranscripts, want)
	}
}
