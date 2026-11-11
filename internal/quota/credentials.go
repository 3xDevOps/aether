package quota

import (
	"bytes"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
)

type claudeCredentialFileShape struct {
	ClaudeAIOAuth *struct {
		AccessToken      string   `json:"accessToken"`
		RefreshToken     string   `json:"refreshToken"`
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
		return credential{status: "unauthenticated", err: "Claude OAuth access token is missing or invalid", signedOut: true, fingerprint: fingerprint("claude-no-token", string(data))}
	}
	if oauth.ExpiresAt > 0 && now.UnixMilli() >= oauth.ExpiresAt {
		return credential{status: "unauthenticated", err: "Claude OAuth credential is expired", expired: true, signedOut: strings.TrimSpace(oauth.RefreshToken) == "", fingerprint: fingerprint("claude-expired", token, string(data))}
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

// ClaudeAccessToken returns the access token in a Claude Code credential
// file without refreshing it. expired reports one past its expiresAt.
func ClaudeAccessToken(data []byte, now time.Time) (token string, expired bool, err error) {
	cred := parseClaudeCredential(data, nil, now)
	switch {
	case cred.status == "ok":
		return cred.token, false, nil
	case cred.expired:
		return "", true, nil
	}
	return "", false, errors.New(cred.err)
}

// LoginFile is the home-relative file LoginProblem reads for agent; empty
// for an agent whose login it cannot read.
func LoginFile(agent string) string {
	switch agent {
	case "claude":
		return claudeCredentialFile
	case "codex":
		return codexCredentialFile
	}
	return ""
}

// LoginProblem is why the OAuth login in agent's login file cannot sign the
// agent in; empty when it can, when the file holds no OAuth login, or when
// only the vendor could tell. An expired access token is a problem only
// without a refresh token: the agent renews one that has it. A login with
// other scopes is not judged: only the usage read needs user:inference.
func LoginProblem(agent string, data []byte, now time.Time) string {
	var cred credential
	switch agent {
	case "claude":
		cred = parseClaudeCredential(data, nil, now)
	case "codex":
		cred = parseCodexCredential(data, nil, now)
	}
	switch {
	case !cred.signedOut:
		return ""
	case cred.expired:
		return cred.err + " and holds no refresh token"
	}
	return cred.err
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
		return credential{status: "unauthenticated", err: "Codex OAuth access token is missing or invalid", signedOut: strings.TrimSpace(shape.APIKey) == "" && strings.TrimSpace(shape.APIKeyAlt) == "", fingerprint: fingerprint("codex-no-token", string(data))}
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
