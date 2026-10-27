package scheduler

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

// ErrAssistantSignInChanged means an agent's sign-in in the assistant home
// belongs to another account than the one the member signed in with: only
// the assistant's own agent writes there.
var ErrAssistantSignInChanged = errors.New("the assistant's sign-in changed since you signed in. Sign in again")

// ErrAssistantNotSignedIn means the member made no sign-in for the agent
// that the server recorded.
var ErrAssistantNotSignedIn = errors.New("not signed in for the assistant. Sign in for the assistant first")

// ErrAssistantSignInRefused means the agent's sign-in service refused the
// sign-in's refresh token.
var ErrAssistantSignInRefused = errors.New("the sign-in service refused the assistant's sign-in. Sign in again")

// AssistantIssuer is where the server checks the assistant's sign-ins: the
// endpoints the pinned CLIs refresh and identify a sign-in at. The zero
// value is the vendors'.
type AssistantIssuer struct {
	ClaudeToken   string
	ClaudeProfile string
	CodexToken    string
	Client        *http.Client
}

func (i AssistantIssuer) withDefaults() AssistantIssuer {
	if i.ClaudeToken == "" {
		i.ClaudeToken = claudeTokenURL
	}
	if i.ClaudeProfile == "" {
		i.ClaudeProfile = claudeProfileURL
	}
	if i.CodexToken == "" {
		i.CodexToken = codexTokenURL
	}
	if i.Client == nil {
		i.Client = &http.Client{Timeout: issuerTimeout}
	}
	return i
}

const issuerTimeout = 30 * time.Second

// assistantSignInDir holds what the server recorded of each agent's sign-in:
// <agent>.json, the account of the sign-in the member made, and
// <agent>.pending, left by a sign-in until the next start records it. No
// container mounts it.
func assistantSignInDir(root string, member domain.MemberID) string {
	return filepath.Join(root, string(member), "signin")
}

// assistantSignInRel is each agent's sign-in file in the assistant home.
var assistantSignInRel = map[string]string{"claude": ".claude/.credentials.json", "codex": ".codex/auth.json"}

// markAssistantSignIn records that a sign-in for agent began; the next
// start records the account it signed in.
func (s *Scheduler) markAssistantSignIn(member domain.MemberID, agent string) error {
	dir := assistantSignInDir(s.cfg.AssistantDir, member)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("scheduler: record the assistant sign-in: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, agent+".pending"), nil, 0o600); err != nil {
		return fmt.Errorf("scheduler: record the assistant sign-in: %w", err)
	}
	return nil
}

// forgetAssistantSignIn deletes agent's sign-in file and its record.
func (s *Scheduler) forgetAssistantSignIn(member domain.MemberID, agent string) error {
	root, err := os.OpenRoot(AssistantHomeDir(s.cfg.AssistantDir, member))
	if err != nil {
		return fmt.Errorf("scheduler: open the assistant home: %w", err)
	}
	defer func() { _ = root.Close() }()
	for _, err := range []error{
		root.Remove(assistantSignInRel[agent]),
		os.Remove(filepath.Join(assistantSignInDir(s.cfg.AssistantDir, member), agent+".json")),
	} {
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("scheduler: forget the assistant's %s sign-in: %w", agent, err)
		}
	}
	return nil
}

// assistantSignedIn reports whether agent has a sign-in the server recorded
// or a finished sign-in it has yet to record. It does not contact the
// issuer.
func (s *Scheduler) assistantSignedIn(member domain.MemberID, agent string) (bool, error) {
	dir := assistantSignInDir(s.cfg.AssistantDir, member)
	for _, name := range []string{agent + ".pending", agent + ".json"} {
		_, err := os.Stat(filepath.Join(dir, name))
		if err == nil {
			return true, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return false, fmt.Errorf("scheduler: read the assistant sign-in record: %w", err)
		}
	}
	return false, nil
}

// verifyAssistantSignIn refreshes agent's sign-in with its issuer, writes the
// rotated tokens back, and checks the account the issuer names against the
// one recorded, recording it first when a sign-in is pending. A sign-in the
// issuer refuses, or that holds nothing to refresh, leaves the agent not
// signed in. No agent may run while it does.
func (s *Scheduler) verifyAssistantSignIn(ctx context.Context, member domain.MemberID, agent string) error {
	dir := assistantSignInDir(s.cfg.AssistantDir, member)
	pending := filepath.Join(dir, agent+".pending")
	record := filepath.Join(dir, agent+".json")
	_, err := os.Stat(pending)
	isPending := err == nil
	want, err := os.ReadFile(record)
	if !isPending && errors.Is(err, fs.ErrNotExist) {
		return ErrAssistantNotSignedIn
	}
	if !isPending && err != nil {
		return fmt.Errorf("scheduler: read the assistant sign-in record: %w", err)
	}
	notSignedIn := func(cause error) error {
		for _, p := range []string{pending, record} {
			if rmErr := os.Remove(p); rmErr != nil && !errors.Is(rmErr, fs.ErrNotExist) {
				return fmt.Errorf("scheduler: forget the assistant sign-in: %w", rmErr)
			}
		}
		return cause
	}
	root, err := os.OpenRoot(AssistantHomeDir(s.cfg.AssistantDir, member))
	if err != nil {
		return fmt.Errorf("scheduler: open the assistant home: %w", err)
	}
	defer func() { _ = root.Close() }()
	rel := assistantSignInRel[agent]
	data, err := readSignInFile(root, rel)
	if errors.Is(err, fs.ErrNotExist) {
		return notSignedIn(ErrAssistantNotSignedIn)
	}
	if err != nil {
		return fmt.Errorf("scheduler: read the assistant's %s sign-in: %w", agent, err)
	}
	issuer := s.cfg.AssistantIssuer.withDefaults()
	var account string
	var rotated []byte
	switch agent {
	case "claude":
		account, rotated, err = issuer.refreshClaude(ctx, data)
	case "codex":
		account, rotated, err = issuer.refreshCodex(ctx, data)
	default:
		return fmt.Errorf("agent %q cannot run the assistant", agent)
	}
	if errors.Is(err, ErrAssistantSignInRefused) || errors.Is(err, ErrAssistantNotSignedIn) {
		return notSignedIn(fmt.Errorf("scheduler: %w", err))
	}
	if err != nil {
		return fmt.Errorf("scheduler: %w", err)
	}
	if rotated != nil {
		if err = root.WriteFile(rel, rotated, 0o600); err != nil {
			return fmt.Errorf("scheduler: write back the assistant's %s sign-in: %w", agent, err)
		}
	}
	if isPending {
		if err = os.WriteFile(record, []byte(account), 0o600); err != nil {
			return fmt.Errorf("scheduler: record the assistant sign-in: %w", err)
		}
		if err = os.Remove(pending); err != nil {
			return fmt.Errorf("scheduler: record the assistant sign-in: %w", err)
		}
		return nil
	}
	if account != string(want) {
		return fmt.Errorf("scheduler: %w", ErrAssistantSignInChanged)
	}
	return nil
}

// refreshClaude refreshes a Claude Code sign-in and returns the account and
// organization the profile endpoint names for the new access token, and the
// file with the rotated tokens.
func (i AssistantIssuer) refreshClaude(ctx context.Context, data []byte) (string, []byte, error) {
	var file map[string]json.RawMessage
	var oauth map[string]json.RawMessage
	var cred struct {
		RefreshToken string   `json:"refreshToken"`
		Scopes       []string `json:"scopes"`
	}
	if json.Unmarshal(data, &file) != nil || json.Unmarshal(file["claudeAiOauth"], &oauth) != nil || oauth == nil ||
		json.Unmarshal(file["claudeAiOauth"], &cred) != nil || cred.RefreshToken == "" {
		return "", nil, fmt.Errorf("the Claude Code sign-in holds no refresh token: %w", ErrAssistantNotSignedIn)
	}
	var tokens struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
		Scope        string `json:"scope"`
	}
	if err := i.post(ctx, "Anthropic", i.ClaudeToken, map[string]string{
		"grant_type": "refresh_token", "refresh_token": cred.RefreshToken, "client_id": claudeClientID, "scope": strings.Join(cred.Scopes, " "),
	}, &tokens); err != nil {
		return "", nil, err
	}
	if tokens.AccessToken == "" {
		return "", nil, errors.New("couldn't verify the assistant's sign-in with Anthropic: the token answer holds no access token")
	}
	if tokens.RefreshToken == "" {
		tokens.RefreshToken = cred.RefreshToken
	}
	var profile struct {
		Account struct {
			UUID string `json:"uuid"`
		} `json:"account"`
		Organization struct {
			UUID string `json:"uuid"`
		} `json:"organization"`
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, i.ClaudeProfile, nil)
	if err != nil {
		return "", nil, fmt.Errorf("couldn't verify the assistant's sign-in with Anthropic: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+tokens.AccessToken)
	if err = i.do(req, "Anthropic", &profile); err != nil {
		return "", nil, err
	}
	if profile.Account.UUID == "" {
		return "", nil, errors.New("couldn't verify the assistant's sign-in with Anthropic: the profile names no account")
	}
	fields := map[string]any{
		"accessToken":  tokens.AccessToken,
		"refreshToken": tokens.RefreshToken,
		"expiresAt":    time.Now().Add(time.Duration(tokens.ExpiresIn) * time.Second).UnixMilli(),
	}
	if tokens.Scope != "" {
		fields["scopes"] = strings.Fields(tokens.Scope)
	}
	rotated, err := rewriteSignIn(file, "claudeAiOauth", oauth, fields)
	if err != nil {
		return "", nil, err
	}
	return "account=" + profile.Account.UUID + ";organization=" + profile.Organization.UUID, rotated, nil
}

// refreshCodex refreshes a Codex ChatGPT sign-in and returns the mode Codex
// will use with the account the issuer's new tokens name, and the file with
// the rotated tokens. Codex 0.159.3 uses auth_mode when it is set, else an
// API key over tokens; an API-key sign-in has nothing to refresh, so its
// identity is a hash of the key. A ChatGPT sign-in may hold an API key from
// its token exchange, which is part of its identity too.
func (i AssistantIssuer) refreshCodex(ctx context.Context, data []byte) (string, []byte, error) {
	var file map[string]json.RawMessage
	var auth struct {
		Mode   string  `json:"auth_mode"`
		Key    *string `json:"OPENAI_API_KEY"`
		Tokens *struct {
			RefreshToken string `json:"refresh_token"`
		} `json:"tokens"`
	}
	if json.Unmarshal(data, &file) != nil || json.Unmarshal(data, &auth) != nil {
		return "", nil, fmt.Errorf("the Codex sign-in is not JSON: %w", ErrAssistantNotSignedIn)
	}
	keyID := ""
	if auth.Key != nil && *auth.Key != "" {
		sum := sha256.Sum256([]byte(*auth.Key))
		keyID = ":key=" + hex.EncodeToString(sum[:])
	}
	mode := auth.Mode
	switch {
	case mode != "":
	case keyID != "":
		mode = "apikey"
	default:
		mode = "chatgpt"
	}
	if mode != "chatgpt" {
		if keyID == "" {
			return "", nil, fmt.Errorf("the Codex sign-in holds no API key: %w", ErrAssistantNotSignedIn)
		}
		return mode + keyID, nil, nil
	}
	if auth.Tokens == nil || auth.Tokens.RefreshToken == "" {
		return "", nil, fmt.Errorf("the Codex sign-in holds no refresh token: %w", ErrAssistantNotSignedIn)
	}
	var tokens struct {
		IDToken      string `json:"id_token"`
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := i.post(ctx, "OpenAI", i.CodexToken, map[string]string{
		"client_id": codexClientID, "grant_type": "refresh_token", "refresh_token": auth.Tokens.RefreshToken, "scope": "openid profile email",
	}, &tokens); err != nil {
		return "", nil, err
	}
	// The tokens came from the issuer over TLS, so their claims need no
	// signature check.
	account, err := codexAccount(tokens.IDToken)
	if err != nil {
		account, err = codexAccount(tokens.AccessToken)
	}
	if err != nil || tokens.AccessToken == "" {
		return "", nil, fmt.Errorf("couldn't verify the assistant's sign-in with OpenAI: the token answer names no account: %v", err)
	}
	var stored map[string]json.RawMessage
	if json.Unmarshal(file["tokens"], &stored) != nil || stored == nil {
		stored = map[string]json.RawMessage{}
	}
	fields := map[string]any{"access_token": tokens.AccessToken, "account_id": account}
	for key, v := range map[string]string{"id_token": tokens.IDToken, "refresh_token": tokens.RefreshToken} {
		if v != "" {
			fields[key] = v
		}
	}
	last, err := json.Marshal(time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return "", nil, fmt.Errorf("encode the rotated sign-in: %w", err)
	}
	file["last_refresh"] = last
	rotated, err := rewriteSignIn(file, "tokens", stored, fields)
	if err != nil {
		return "", nil, err
	}
	return "chatgpt:" + account + keyID, rotated, nil
}

// rewriteSignIn sets fields in the object file[key] and encodes file.
func rewriteSignIn(file map[string]json.RawMessage, key string, inner map[string]json.RawMessage, fields map[string]any) ([]byte, error) {
	for name, v := range fields {
		raw, err := json.Marshal(v)
		if err != nil {
			return nil, fmt.Errorf("encode the rotated sign-in: %w", err)
		}
		inner[name] = raw
	}
	raw, err := json.Marshal(inner)
	if err != nil {
		return nil, fmt.Errorf("encode the rotated sign-in: %w", err)
	}
	file[key] = raw
	out, err := json.Marshal(file)
	if err != nil {
		return nil, fmt.Errorf("encode the rotated sign-in: %w", err)
	}
	return out, nil
}

// post sends body as JSON to url and decodes the answer into out.
func (i AssistantIssuer) post(ctx context.Context, vendor, url string, body map[string]string, out any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("couldn't verify the assistant's sign-in with %s: %w", vendor, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("couldn't verify the assistant's sign-in with %s: %w", vendor, err)
	}
	req.Header.Set("Content-Type", "application/json")
	return i.do(req, vendor, out)
}

// do sends req and decodes a 2xx answer into out. A 400 or 401 is the
// issuer refusing the sign-in; any other failure is reported as it came.
func (i AssistantIssuer) do(req *http.Request, vendor string, out any) error {
	resp, err := i.Client.Do(req)
	if err != nil {
		return fmt.Errorf("couldn't verify the assistant's sign-in with %s: %w", vendor, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("couldn't verify the assistant's sign-in with %s: %w", vendor, err)
	}
	detail := strings.TrimSpace(string(body[:min(len(body), 512)]))
	switch {
	case resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnauthorized:
		return fmt.Errorf("%s answered %s: %s: %w", vendor, resp.Status, detail, ErrAssistantSignInRefused)
	case resp.StatusCode/100 != 2:
		return fmt.Errorf("couldn't verify the assistant's sign-in with %s: %s: %s", vendor, resp.Status, detail)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("couldn't verify the assistant's sign-in with %s: malformed answer: %w", vendor, err)
	}
	return nil
}

// codexAccount reads the ChatGPT account from a Codex token's claims.
func codexAccount(token string) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", errors.New("the Codex token is not a JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", fmt.Errorf("decode the Codex token: %w", err)
	}
	var claims struct {
		Auth struct {
			Account string `json:"chatgpt_account_id"`
		} `json:"https://api.openai.com/auth"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil || claims.Auth.Account == "" {
		return "", errors.New("the Codex token names no ChatGPT account")
	}
	return claims.Auth.Account, nil
}
