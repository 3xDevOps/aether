package quota

import (
	"testing"
	"time"
)

func TestLoginProblemNamesWhatTheLoginFileLacks(t *testing.T) {
	now := time.UnixMilli(2_000_000)
	for _, tc := range []struct {
		name, agent, file, want string
	}{
		{"claude signed in", "claude", `{"claudeAiOauth":{"accessToken":"fixture-access","refreshToken":"fixture-refresh","expiresAt":3000000,"scopes":["user:inference"]}}`, ""},
		{"claude token blanked", "claude", `{"claudeAiOauth":{"accessToken":"","refreshToken":"","expiresAt":0}}`, "Claude OAuth access token is missing or invalid"},
		{"claude token with a control character", "claude", `{"claudeAiOauth":{"accessToken":"fixture\naccess"}}`, "Claude OAuth access token is missing or invalid"},
		{"claude expired, refreshable", "claude", `{"claudeAiOauth":{"accessToken":"fixture-access","refreshToken":"fixture-refresh","expiresAt":1000000}}`, ""},
		{"claude expired, no refresh token", "claude", `{"claudeAiOauth":{"accessToken":"fixture-access","expiresAt":1000000}}`, "Claude OAuth credential is expired and holds no refresh token"},
		{"claude wrong scope", "claude", `{"claudeAiOauth":{"accessToken":"fixture-access","expiresAt":3000000,"scopes":["user:profile"]}}`, "Claude OAuth credential lacks user:inference scope"},
		{"claude API key", "claude", `{"apiKey":"fixture-key"}`, ""},
		{"claude file without a login", "claude", `{}`, ""},
		{"claude no file", "claude", ``, ""},
		{"claude malformed", "claude", `{"claudeAiOauth":`, ""},
		{"codex signed in", "codex", `{"tokens":{"access_token":"fixture-access","account_id":"fixture-account"}}`, ""},
		{"codex token blanked", "codex", `{"tokens":{"access_token":"","account_id":"fixture-account"}}`, "Codex OAuth access token is missing or invalid"},
		{"codex API key beside empty tokens", "codex", `{"OPENAI_API_KEY":"fixture-key","tokens":{"access_token":""}}`, ""},
		{"codex API key", "codex", `{"OPENAI_API_KEY":"fixture-key"}`, ""},
		{"codex file without a login", "codex", `{}`, ""},
		{"an agent whose login the server cannot read", "gemini", `{"access_token":""}`, ""},
	} {
		if got := LoginProblem(tc.agent, []byte(tc.file), now); got != tc.want {
			t.Errorf("%s: LoginProblem = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestLoginFileIsKnownForClaudeAndCodexOnly(t *testing.T) {
	for agent, want := range map[string]string{"claude": ".claude/.credentials.json", "codex": ".codex/auth.json", "gemini": ""} {
		if got := LoginFile(agent); got != want {
			t.Errorf("LoginFile(%s) = %q, want %q", agent, got, want)
		}
	}
}
