package scheduler

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/3xDevOps/Aether/internal/domain"
)

// ErrAssistantSignInChanged means an agent's sign-in in the assistant home is
// not the one the member made: only the assistant's own agent writes there.
var ErrAssistantSignInChanged = errors.New("the assistant's sign-in changed since you signed in. Sign in again")

// assistantSignInDir holds what the server recorded of each agent's sign-in:
// <agent>.json, the identity of the sign-in the member made, and
// <agent>.pending, left by a sign-in until the next start records it. No
// container mounts it.
func assistantSignInDir(root string, member domain.MemberID) string {
	return filepath.Join(root, string(member), "signin")
}

// assistantSignInRel is each agent's sign-in file in the assistant home.
var assistantSignInRel = map[string]string{"claude": ".claude/.credentials.json", "codex": ".codex/auth.json"}

// assistantSignInIdentity is what of agent's sign-in file must not change
// between the member's sign-in and a start. Codex names its account in the
// access token (an OpenAI-signed JWT) or is an API key; the Claude Code file
// names no account, so its key set and the fields a token refresh leaves
// alone stand in for one.
func assistantSignInIdentity(agent string, data []byte) (string, error) {
	switch agent {
	case "claude":
		var file struct {
			OAuth map[string]json.RawMessage `json:"claudeAiOauth"`
		}
		if err := json.Unmarshal(data, &file); err != nil || file.OAuth == nil {
			return "", errors.New("the Claude Code sign-in holds no claudeAiOauth")
		}
		parts := []string{"keys=" + strings.Join(slices.Sorted(maps.Keys(file.OAuth)), ",")}
		for _, key := range []string{"scopes", "subscriptionType", "rateLimitTier"} {
			parts = append(parts, key+"="+string(file.OAuth[key]))
		}
		return strings.Join(parts, ";"), nil
	case "codex":
		var file struct {
			Key    *string `json:"OPENAI_API_KEY"`
			Tokens *struct {
				Access string `json:"access_token"`
			} `json:"tokens"`
		}
		if err := json.Unmarshal(data, &file); err != nil {
			return "", fmt.Errorf("read the Codex sign-in: %w", err)
		}
		if file.Tokens != nil && file.Tokens.Access != "" {
			account, err := codexAccount(file.Tokens.Access)
			if err != nil {
				return "", err
			}
			return "chatgpt:" + account, nil
		}
		if file.Key != nil && *file.Key != "" {
			sum := sha256.Sum256([]byte(*file.Key))
			return "apikey:" + hex.EncodeToString(sum[:]), nil
		}
		return "", errors.New("the Codex sign-in holds neither tokens nor an API key")
	}
	return "", fmt.Errorf("agent %q cannot run the assistant", agent)
}

// codexAccount reads the ChatGPT account from a Codex access token's claims.
// The signature is not checked: a token naming another account than its own
// is refused by OpenAI.
func codexAccount(token string) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", errors.New("the Codex access token is not a JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", fmt.Errorf("decode the Codex access token: %w", err)
	}
	var claims struct {
		Auth struct {
			Account string `json:"chatgpt_account_id"`
		} `json:"https://api.openai.com/auth"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil || claims.Auth.Account == "" {
		return "", errors.New("the Codex access token names no ChatGPT account")
	}
	return claims.Auth.Account, nil
}

// markAssistantSignIn records that a sign-in for agent began; the next
// start records what it wrote.
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

// pinAssistantSignIns records the identity of each sign-in a finished
// sign-in left, while no agent runs to change it. A sign-in that wrote no
// file records nothing.
func (s *Scheduler) pinAssistantSignIns(member domain.MemberID) error {
	dir := assistantSignInDir(s.cfg.AssistantDir, member)
	for _, agent := range assistantAgents {
		pending := filepath.Join(dir, agent+".pending")
		if _, err := os.Stat(pending); errors.Is(err, fs.ErrNotExist) {
			continue
		} else if err != nil {
			return fmt.Errorf("scheduler: read the assistant sign-in record: %w", err)
		}
		data, err := s.readAssistantSignIn(member, agent)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		if err == nil {
			identity, idErr := assistantSignInIdentity(agent, data)
			if idErr != nil {
				return fmt.Errorf("scheduler: %w", idErr)
			}
			if err = os.WriteFile(filepath.Join(dir, agent+".json"), []byte(identity), 0o600); err != nil {
				return fmt.Errorf("scheduler: record the assistant sign-in: %w", err)
			}
		}
		if err = os.Remove(pending); err != nil {
			return fmt.Errorf("scheduler: record the assistant sign-in: %w", err)
		}
	}
	return nil
}

// checkAssistantSignIn returns ErrAssistantSignInChanged unless agent's
// sign-in matches the one recorded, or a sign-in is pending a record.
func (s *Scheduler) checkAssistantSignIn(member domain.MemberID, agent string) error {
	dir := assistantSignInDir(s.cfg.AssistantDir, member)
	if _, err := os.Stat(filepath.Join(dir, agent+".pending")); err == nil {
		return nil
	}
	want, err := os.ReadFile(filepath.Join(dir, agent+".json"))
	if errors.Is(err, fs.ErrNotExist) {
		return ErrAssistantSignInChanged
	}
	if err != nil {
		return fmt.Errorf("scheduler: read the assistant sign-in record: %w", err)
	}
	data, err := s.readAssistantSignIn(member, agent)
	if err != nil {
		return err
	}
	if got, err := assistantSignInIdentity(agent, data); err != nil || got != string(want) {
		return ErrAssistantSignInChanged
	}
	return nil
}

func (s *Scheduler) readAssistantSignIn(member domain.MemberID, agent string) ([]byte, error) {
	root, err := os.OpenRoot(AssistantHomeDir(s.cfg.AssistantDir, member))
	if err != nil {
		return nil, fmt.Errorf("scheduler: open the assistant home: %w", err)
	}
	defer func() { _ = root.Close() }()
	data, err := readSignInFile(root, assistantSignInRel[agent])
	if err != nil {
		return nil, fmt.Errorf("scheduler: read the assistant's %s sign-in: %w", agent, err)
	}
	return data, nil
}
