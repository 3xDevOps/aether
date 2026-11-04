package scheduler

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/protocol"
)

// TestAssistantAccountNeverTouchesTheEnvironmentLogin compares the
// assistant's recorded account with the login in the member's environment
// and leaves that login as it found it: no write, no refresh, not even with
// an expired token.
func TestAssistantAccountNeverTouchesTheEnvironmentLogin(t *testing.T) {
	t.Parallel()
	assistantDir := filepath.Join(t.TempDir(), "assistant")
	issuer, fake := &fakeIssuer{}, AssistantIssuer{}
	e := newTestEnv(t, func(c *Config) {
		c.AssistantDir = assistantDir
		issuer, fake = newFakeIssuer(t)
		c.AssistantIssuer = fake
	})
	home := filepath.Join(e.sched.cfg.Homes.Root(), string(e.member.ID))
	check := func(agent string) protocol.AssistantAccount {
		t.Helper()
		got, err := e.sched.AssistantAccount(t.Context(), e.member.ID, agent)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if got := check("claude"); got.State != protocol.AssistantAccountUnrecorded {
		t.Fatalf("before the sign-in is recorded: %+v", got)
	}
	signin := assistantSignInDir(assistantDir, e.member.ID)
	if err := os.MkdirAll(signin, 0o700); err != nil {
		t.Fatal(err)
	}
	for agent, account := range map[string]string{"claude": "account=alice;organization=org-alice", "codex": "chatgpt:acct-alice:key=0123abcd"} {
		if err := os.WriteFile(filepath.Join(signin, agent+".json"), []byte(account), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	claude := func(token string, expiresAt time.Time) string {
		return fmt.Sprintf(`{"claudeAiOauth":{"accessToken":%q,"refreshToken":"alice~0","expiresAt":%d,"scopes":["user:inference"]}}`, token, expiresAt.UnixMilli())
	}
	codex := func(account string) string {
		return `{"auth_mode":"chatgpt","OPENAI_API_KEY":"sk-synthetic-env","tokens":{"id_token":"` + codexToken(account) + `","access_token":"opaque","refresh_token":"` + account + `~0"}}`
	}
	later, earlier := time.Now().Add(time.Hour), time.Now().Add(-time.Hour)
	stamp := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	for _, tc := range []struct {
		name, agent, content string
		profileDown          bool
		want                 protocol.AssistantAccount
		wantErr              string
	}{
		{name: "same Claude account", agent: "claude", content: claude("access-alice", later),
			want: protocol.AssistantAccount{Agent: "claude", State: protocol.AssistantAccountSame, Email: "alice@example.test"}},
		{name: "another Claude account", agent: "claude", content: claude("access-mallory", later),
			want: protocol.AssistantAccount{Agent: "claude", State: protocol.AssistantAccountOther, Environment: "account mallory, organization org-mallory", Email: "mallory@example.test"}},
		{name: "expired Claude token", agent: "claude", content: claude("access-alice", earlier),
			want: protocol.AssistantAccount{Agent: "claude", State: protocol.AssistantAccountExpired}},
		{name: "Claude token the profile refuses", agent: "claude", content: claude("revoked", later), wantErr: "Anthropic answered 401 Unauthorized, refusing your environment's Claude token"},
		{name: "profile down", agent: "claude", content: claude("access-alice", later), profileDown: true, wantErr: "couldn't verify your environment's sign-in with Anthropic: 503"},
		{name: "same Codex account under another API key", agent: "codex", content: codex("acct-alice"),
			want: protocol.AssistantAccount{Agent: "codex", State: protocol.AssistantAccountSame}},
		{name: "another Codex account", agent: "codex", content: codex("acct-mallory"),
			want: protocol.AssistantAccount{Agent: "codex", State: protocol.AssistantAccountOther, Environment: "ChatGPT account acct-mallory"}},
		{name: "Codex API key", agent: "codex", content: `{"OPENAI_API_KEY":"sk-synthetic-env"}`,
			want: protocol.AssistantAccount{Agent: "codex", State: protocol.AssistantAccountOther, Environment: "an API key"}},
		{name: "no Codex login", agent: "codex", wantErr: "your environment has no ~/.codex/auth.json"},
	} {
		rel := assistantSignInRel[tc.agent]
		path := filepath.Join(home, rel)
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			t.Fatal(err)
		}
		if tc.content != "" {
			writeHomeFile(t, home, rel, tc.content)
			if err := os.Chtimes(path, stamp, stamp); err != nil {
				t.Fatal(err)
			}
		}
		issuer.setProfileDown(tc.profileDown)
		got := check(tc.agent)
		if tc.wantErr != "" {
			if got.State != protocol.AssistantAccountError || !strings.Contains(got.Error, tc.wantErr) {
				t.Errorf("%s: %+v, want an error with %q", tc.name, got, tc.wantErr)
			}
		} else if got != tc.want {
			t.Errorf("%s: %+v, want %+v", tc.name, got, tc.want)
		}
		data, err := os.ReadFile(path)
		if tc.content == "" {
			if !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("%s: the check created %s: %v", tc.name, rel, err)
			}
			continue
		}
		info, statErr := os.Stat(path)
		if err != nil || statErr != nil || !bytes.Equal(data, []byte(tc.content)) || !info.ModTime().Equal(stamp) || info.Mode().Perm() != 0o600 {
			t.Errorf("%s: the check changed %s: %q %v %v", tc.name, rel, data, info, errors.Join(err, statErr))
		}
	}
	issuer.mu.Lock()
	defer issuer.mu.Unlock()
	if issuer.tokenCalls != 0 {
		t.Fatalf("the check called the token endpoint %d times", issuer.tokenCalls)
	}
}
