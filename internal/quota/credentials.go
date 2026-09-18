package quota

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

type claudeCredentialFileShape struct {
	ClaudeAIOAuth *struct {
		AccessToken      string   `json:"accessToken"`
		ExpiresAt        int64    `json:"expiresAt"`
		Scopes           []string `json:"scopes"`
		SubscriptionType string   `json:"subscriptionType"`
	} `json:"claudeAiOauth"`
	APIKey    string `json:"apiKey"`
	APIKeyAlt string `json:"api_key"`
	APIKeyEnv string `json:"ANTHROPIC_API_KEY"`
}

type codexCredentialFileShape struct {
	Tokens *struct {
		AccessToken string `json:"access_token"`
		AccountID   string `json:"account_id"`
	} `json:"tokens"`
	APIKey    string `json:"OPENAI_API_KEY"`
	APIKeyAlt string `json:"api_key"`
}

func parseClaudeCredential(data []byte, readErr error, now time.Time) credential {
	if readErr != nil {
		return credential{status: "unavailable", err: "credential file unavailable", fingerprint: fingerprint("claude-read-error")}
	}
	if len(data) == 0 {
		return credential{status: "unauthenticated", err: "Claude OAuth credential not found", fingerprint: fingerprint("claude-missing")}
	}
	var shape claudeCredentialFileShape
	if err := parseJSON(data, &shape); err != nil {
		return credential{status: "error", err: "Claude credential file is malformed", fingerprint: fingerprint("claude-malformed", string(data))}
	}
	if shape.ClaudeAIOAuth == nil {
		if strings.TrimSpace(shape.APIKey) != "" || strings.TrimSpace(shape.APIKeyAlt) != "" || strings.TrimSpace(shape.APIKeyEnv) != "" {
			return credential{status: "unsupported", err: "API-key authentication does not expose Claude subscription usage", fingerprint: fingerprint("claude-api-key", string(data))}
		}
		return credential{status: "unauthenticated", err: "Claude OAuth credential not found", fingerprint: fingerprint("claude-missing-oauth", string(data))}
	}
	oauth := shape.ClaudeAIOAuth
	token, validToken := cleanHeaderValue(oauth.AccessToken)
	if !validToken {
		return credential{status: "unauthenticated", err: "Claude OAuth access token is missing or invalid", fingerprint: fingerprint("claude-no-token", string(data))}
	}
	if oauth.ExpiresAt > 0 && now.UnixMilli() >= oauth.ExpiresAt {
		return credential{status: "unauthenticated", err: "Claude OAuth credential is expired", fingerprint: fingerprint("claude-expired", token, string(data))}
	}
	if len(oauth.Scopes) > 0 && !hasScope(oauth.Scopes, "user:inference") {
		return credential{status: "unauthenticated", err: "Claude OAuth credential lacks user:inference scope", fingerprint: fingerprint("claude-wrong-scope", token, sortedScopes(oauth.Scopes))}
	}
	return credential{
		status:      "ok",
		token:       token,
		plan:        oauth.SubscriptionType,
		fingerprint: fingerprint("claude-oauth", token, oauth.SubscriptionType, strconv.FormatInt(oauth.ExpiresAt, 10), sortedScopes(oauth.Scopes)),
	}
}

func parseCodexCredential(data []byte, readErr error, _ time.Time) credential {
	if readErr != nil {
		return credential{status: "unavailable", err: "credential file unavailable", fingerprint: fingerprint("codex-read-error")}
	}
	if len(data) == 0 {
		return credential{status: "unauthenticated", err: "Codex OAuth credential not found", fingerprint: fingerprint("codex-missing")}
	}
	var shape codexCredentialFileShape
	if err := parseJSON(data, &shape); err != nil {
		return credential{status: "error", err: "Codex credential file is malformed", fingerprint: fingerprint("codex-malformed", string(data))}
	}
	if shape.Tokens == nil {
		if strings.TrimSpace(shape.APIKey) != "" || strings.TrimSpace(shape.APIKeyAlt) != "" {
			return credential{status: "unsupported", err: "API-key authentication does not expose ChatGPT subscription usage", fingerprint: fingerprint("codex-api-key", string(data))}
		}
		return credential{status: "unauthenticated", err: "Codex OAuth credential not found", fingerprint: fingerprint("codex-missing-oauth", string(data))}
	}
	token, validToken := cleanHeaderValue(shape.Tokens.AccessToken)
	accountID, validAccount := cleanHeaderValue(shape.Tokens.AccountID)
	if !validToken {
		return credential{status: "unauthenticated", err: "Codex OAuth access token is missing or invalid", fingerprint: fingerprint("codex-no-token", string(data))}
	}
	if !validAccount {
		return credential{status: "unauthenticated", err: "Codex OAuth account id is missing or invalid", fingerprint: fingerprint("codex-no-account", token)}
	}
	return credential{
		status:      "ok",
		token:       token,
		accountID:   accountID,
		fingerprint: fingerprint("codex-oauth", token, accountID),
	}
}

func hasScope(scopes []string, want string) bool {
	for _, scope := range scopes {
		if scope == want {
			return true
		}
	}
	return false
}

func cleanHeaderValue(raw string) (string, bool) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", false
	}
	for i := range len(value) {
		if value[i] < 0x20 || value[i] == 0x7f {
			return "", false
		}
	}
	return value, true
}

func rawIsNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}
