package scheduler

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/3xDevOps/Aether/internal/ptyhost"
)

// codexToken is a Codex token whose claims name account.
func codexToken(account string) string {
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"alg":"none"}`)) + "." + enc([]byte(`{"https://api.openai.com/auth":{"chatgpt_account_id":"`+account+`"}}`)) + ".c2lnbmF0dXJl"
}

// fakeIssuer is the Claude and Codex sign-in services. A refresh token is
// "<account>~<n>"; each is good once, as the vendors' are, and its refresh
// returns "<account>~<n+1>".
type fakeIssuer struct {
	mu          sync.Mutex
	used        map[string]bool
	down        bool
	profileDown bool
}

func newFakeIssuer(t *testing.T) (*fakeIssuer, AssistantIssuer) {
	t.Helper()
	f := &fakeIssuer{used: map[string]bool{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, AssistantIssuer{ClaudeToken: srv.URL + "/claude/token", ClaudeProfile: srv.URL + "/claude/profile", CodexToken: srv.URL + "/codex/token", Client: srv.Client()}
}

func (f *fakeIssuer) setDown(down bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.down = down
}

func (f *fakeIssuer) setProfileDown(down bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.profileDown = down
}

func (f *fakeIssuer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		http.Error(w, "upstream unavailable", http.StatusServiceUnavailable)
		return
	}
	if r.URL.Path == "/claude/profile" {
		if f.profileDown {
			http.Error(w, "profile unavailable", http.StatusServiceUnavailable)
			return
		}
		account, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer access-")
		if !ok {
			http.Error(w, `{"error":"invalid token"}`, http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"account": map[string]string{"uuid": account}, "organization": map[string]string{"uuid": "org-" + account}})
		return
	}
	var req map[string]string
	_ = json.NewDecoder(r.Body).Decode(&req)
	client := map[string]string{"/claude/token": claudeClientID, "/codex/token": codexClientID}[r.URL.Path]
	account, n, ok := strings.Cut(req["refresh_token"], "~")
	if req["grant_type"] != "refresh_token" || req["client_id"] != client || !ok || f.used[req["refresh_token"]] {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
		return
	}
	f.used[req["refresh_token"]] = true
	next := fmt.Sprintf("%s~%s0", account, n)
	if r.URL.Path == "/claude/token" {
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "access-" + account, "refresh_token": next, "expires_in": 3600, "scope": "user:inference"})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"id_token": codexToken(account), "access_token": codexToken(account), "refresh_token": next})
}

// TestAssistantSignInVerifiedWithTheIssuer records the account the issuer
// names when the member signs in and refuses a start once the assistant's
// own agent swapped in another account's sign-in, while the server's own
// refreshes keep starting it.
func TestAssistantSignInVerifiedWithTheIssuer(t *testing.T) {
	t.Parallel()
	assistantDir := filepath.Join(t.TempDir(), "assistant")
	issuer, fake := &fakeIssuer{}, AssistantIssuer{}
	e := newTestEnv(t, func(c *Config) {
		c.ServerBinary = fakeServerBinary(t, "current build")
		c.AssistantDir = assistantDir
		issuer, fake = newFakeIssuer(t)
		c.AssistantIssuer = fake
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
	read := func(rel string) string {
		data, err := os.ReadFile(filepath.Join(home, rel))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	signIn("claude", ".claude/.credentials.json", `{"claudeAiOauth":{"accessToken":"a","refreshToken":"alice~0","scopes":["user:inference"],"subscriptionType":"max"}}`)
	signIn("codex", ".codex/auth.json", `{"auth_mode":"chatgpt","tokens":{"access_token":"`+codexToken("acct-alice")+`","refresh_token":"acct-alice~0"}}`)
	if record, err := os.ReadFile(filepath.Join(assistantSignInDir(assistantDir, e.member.ID), "claude.json")); err != nil || string(record) != "account=alice;organization=org-alice" {
		t.Fatalf("Claude record = %q, %v; want the account the profile names", record, err)
	}
	for range 3 {
		for _, agent := range []string{"claude", "codex"} {
			if err := start(agent); err != nil {
				t.Fatalf("a %s start after the server's own refresh: %v", agent, err)
			}
		}
	}
	if got := read(".claude/.credentials.json"); !strings.Contains(got, `"refreshToken":"alice~00000"`) || !strings.Contains(got, `"subscriptionType":"max"`) {
		t.Fatalf("the rotated Claude sign-in = %s", got)
	}

	for _, tc := range []struct {
		name, agent, rel, content string
		want                      error
	}{
		{"claude account on the same plan", "claude", ".claude/.credentials.json", `{"claudeAiOauth":{"accessToken":"a","refreshToken":"mallory~0","scopes":["user:inference"],"subscriptionType":"max"}}`, ErrAssistantSignInChanged},
		{"codex refresh token of another account", "codex", ".codex/auth.json", `{"auth_mode":"chatgpt","tokens":{"access_token":"` + codexToken("acct-alice") + `","refresh_token":"acct-mallory~0"}}`, ErrAssistantSignInChanged},
		{"codex API key mode over Alice's tokens", "codex", ".codex/auth.json", `{"auth_mode":"apikey","OPENAI_API_KEY":"sk-synthetic-mallory","tokens":{"refresh_token":"acct-alice~1"}}`, ErrAssistantSignInChanged},
		{"codex API key beside Alice's tokens", "codex", ".codex/auth.json", `{"OPENAI_API_KEY":"sk-synthetic-mallory","tokens":{"refresh_token":"acct-alice~2"}}`, ErrAssistantSignInChanged},
		{"codex API key added in ChatGPT mode", "codex", ".codex/auth.json", `{"auth_mode":"chatgpt","OPENAI_API_KEY":"sk-synthetic-mallory","tokens":{"refresh_token":"acct-alice~3"}}`, ErrAssistantSignInChanged},
		{"claude refresh token the issuer refuses", "claude", ".claude/.credentials.json", `{"claudeAiOauth":{"accessToken":"a","refreshToken":"alice~0","scopes":["user:inference"]}}`, ErrAssistantSignInRefused},
	} {
		writeHomeFile(t, home, tc.rel, tc.content)
		if err := start(tc.agent); !errors.Is(err, tc.want) {
			t.Errorf("%s: start = %v, want %v", tc.name, err, tc.want)
		}
	}
	if found, err := e.sched.AssistantLoginFound(e.member.ID, "claude"); err != nil || found {
		t.Errorf("after the issuer refused the Claude sign-in: ready %v, %v; want not signed in", found, err)
	}

	signIn("codex", ".codex/auth.json", `{"auth_mode":"chatgpt","tokens":{"refresh_token":"acct-alice~9"}}`)
	issuer.setDown(true)
	err := start("codex")
	if err == nil || !strings.Contains(err.Error(), "couldn't verify the assistant's sign-in with OpenAI: 503") {
		t.Fatalf("a start with the issuer down = %v, want its real error", err)
	}
	if found, foundErr := e.sched.AssistantLoginFound(e.member.ID, "codex"); foundErr != nil || !found {
		t.Fatalf("the issuer being down dropped the sign-in: %v, %v", found, foundErr)
	}
	issuer.setDown(false)
	if err := start("codex"); err != nil {
		t.Fatalf("the start once the issuer is back: %v", err)
	}

	signIn("claude", ".claude/.credentials.json", `{"claudeAiOauth":{"accessToken":"a","refreshToken":"alice~p","scopes":["user:inference"]}}`)
	issuer.setProfileDown(true)
	if err := start("claude"); err == nil || !strings.Contains(err.Error(), "couldn't verify the assistant's sign-in with Anthropic: 503") {
		t.Fatalf("a start with the profile down = %v, want its real error", err)
	}
	issuer.setProfileDown(false)
	if err := start("claude"); err != nil {
		t.Fatalf("the start once the profile is back, after the failed start spent the refresh token: %v", err)
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

// TestAssistantSignInThatWroteNothingUsable leaves an agent whose sign-in
// wrote no usable file not signed in, with no pending record to refuse every
// later start or sign-in on.
func TestAssistantSignInThatWroteNothingUsable(t *testing.T) {
	t.Parallel()
	assistantDir := filepath.Join(t.TempDir(), "assistant")
	e := newTestEnv(t, func(c *Config) {
		c.ServerBinary = fakeServerBinary(t, "current build")
		c.AssistantDir = assistantDir
	})
	withCoordination(t, e)
	home := AssistantHomeDir(assistantDir, e.member.ID)
	start := func() error {
		_, err := e.sched.freshAssistantContainer(t.Context(), e.member.ID, "claude", func() {}, "")
		return err
	}
	if err := start(); !errors.Is(err, ErrAssistantNotSignedIn) {
		t.Fatalf("a start with no sign-in = %v, want not signed in", err)
	}
	if err := e.sched.markAssistantSignIn(e.member.ID, "claude"); err != nil {
		t.Fatal(err)
	}
	writeHomeFile(t, home, ".claude/.credentials.json", `{}`)
	if err := start(); !errors.Is(err, ErrAssistantNotSignedIn) {
		t.Fatalf("a start after an unusable sign-in = %v, want not signed in", err)
	}
	if _, err := os.Stat(filepath.Join(assistantSignInDir(assistantDir, e.member.ID), "claude.pending")); !os.IsNotExist(err) {
		t.Fatalf("the unusable sign-in is still pending: %v", err)
	}
	if _, err := e.sched.StartAssistantLogin(t.Context(), e.member.ID, "claude", 80, 24); err != nil {
		t.Fatalf("a sign-in after the unusable one: %v", err)
	}
}

// TestAssistantPendingSignInIsRecordedBeforeAnotherAgentRuns records a
// Codex sign-in when Claude Code starts first, so a Claude Code session that
// rewrites the Codex sign-in cannot have its account recorded; and a pending
// sign-in that cannot be verified leaves its agent not signed in without
// refusing the other's start.
func TestAssistantPendingSignInIsRecordedBeforeAnotherAgentRuns(t *testing.T) {
	t.Parallel()
	assistantDir := filepath.Join(t.TempDir(), "assistant")
	e := newTestEnv(t, func(c *Config) {
		c.ServerBinary = fakeServerBinary(t, "current build")
		c.AssistantDir = assistantDir
		_, c.AssistantIssuer = newFakeIssuer(t)
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
	}
	signIn("claude", ".claude/.credentials.json", `{"claudeAiOauth":{"accessToken":"a","refreshToken":"alice~0"}}`)
	if err := start("claude"); err != nil {
		t.Fatal(err)
	}
	signIn("codex", ".codex/auth.json", `{"auth_mode":"chatgpt","tokens":{"refresh_token":"acct-alice~0"}}`)
	if err := start("claude"); err != nil {
		t.Fatalf("the Claude Code start with a Codex sign-in pending: %v", err)
	}
	// What a steered Claude Code session could write into the shared home.
	writeHomeFile(t, home, ".codex/auth.json", `{"auth_mode":"chatgpt","tokens":{"refresh_token":"acct-mallory~0"}}`)
	if err := start("codex"); !errors.Is(err, ErrAssistantSignInChanged) {
		t.Fatalf("the first Codex start after the rewrite = %v, want the sign-in refused", err)
	}

	signIn("codex", ".codex/auth.json", `{"auth_mode":"chatgpt","tokens":{"refresh_token":"acct-alice~0"}}`)
	if err := start("claude"); err != nil {
		t.Fatalf("a Codex sign-in the issuer refuses refused the Claude Code start: %v", err)
	}
	if found, err := e.sched.AssistantLoginFound(e.member.ID, "codex"); err != nil || found {
		t.Fatalf("Codex after its sign-in was refused: ready %v, %v; want not signed in", found, err)
	}
	if _, err := os.Stat(filepath.Join(home, ".codex", "auth.json")); !os.IsNotExist(err) {
		t.Fatalf("the refused Codex sign-in is still there: %v", err)
	}
}

// TestAssistantSignInRefusalAndWriteBack keeps a refusal's answer, which can
// echo the token, out of the error, and leaves no temporary file behind the
// rotated sign-in.
func TestAssistantSignInRefusalAndWriteBack(t *testing.T) {
	t.Parallel()
	assistantDir := filepath.Join(t.TempDir(), "assistant")
	e := newTestEnv(t, func(c *Config) {
		c.ServerBinary = fakeServerBinary(t, "current build")
		c.AssistantDir = assistantDir
		_, c.AssistantIssuer = newFakeIssuer(t)
	})
	withCoordination(t, e)
	home := AssistantHomeDir(assistantDir, e.member.ID)
	for _, refresh := range []string{"alice~0", "unknown-token"} {
		if err := e.sched.markAssistantSignIn(e.member.ID, "claude"); err != nil {
			t.Fatal(err)
		}
		writeHomeFile(t, home, ".claude/.credentials.json", `{"claudeAiOauth":{"accessToken":"a","refreshToken":"`+refresh+`"}}`)
		_, err := e.sched.freshAssistantContainer(t.Context(), e.member.ID, "claude", func() {}, "")
		if refresh == "alice~0" && err != nil {
			t.Fatal(err)
		}
		if refresh != "alice~0" && (!errors.Is(err, ErrAssistantSignInRefused) || strings.Contains(err.Error(), "invalid_grant")) {
			t.Fatalf("a refused start = %v, want the refusal without the issuer's answer", err)
		}
	}
	entries, err := os.ReadDir(filepath.Join(home, ".claude"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".aether-new") {
			t.Errorf("the write-back left %s", e.Name())
		}
	}
}
