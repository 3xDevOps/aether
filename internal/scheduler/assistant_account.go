package scheduler

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/quota"
)

// AssistantAccount compares the account recorded for agent's assistant
// sign-in with the login in the member's environment. It only reads that
// login and never refreshes it: a refresh would spend the refresh token the
// member's sessions hold.
func (s *Scheduler) AssistantAccount(ctx context.Context, member domain.MemberID, agent string) (protocol.AssistantAccount, error) {
	out := protocol.AssistantAccount{Agent: agent}
	rel, ok := assistantSignInRel[agent]
	if !ok {
		return out, fmt.Errorf("agent %q cannot run the assistant", agent)
	}
	pin, err := os.ReadFile(filepath.Join(assistantSignInDir(s.cfg.AssistantDir, member), agent+".json"))
	if errors.Is(err, fs.ErrNotExist) || err == nil && len(pin) == 0 {
		out.State = protocol.AssistantAccountUnrecorded
		return out, nil
	}
	if err != nil {
		return out, fmt.Errorf("scheduler: read the assistant sign-in record: %w", err)
	}
	if s.cfg.Homes == nil {
		return out, errors.New("scheduler: member homes are not configured")
	}
	failed := func(err error) (protocol.AssistantAccount, error) {
		out.State, out.Error = protocol.AssistantAccountError, err.Error()
		return out, nil
	}
	data, err := s.cfg.Homes.ReadCredential(member, rel, maxSignInBytes)
	if err != nil {
		return failed(err)
	}
	if len(data) == 0 {
		return failed(fmt.Errorf("your environment has no ~/%s", rel))
	}
	var account string
	switch agent {
	case "claude":
		token, expired, err := quota.ClaudeAccessToken(data, time.Now())
		if expired {
			out.State = protocol.AssistantAccountExpired
			return out, nil
		}
		if err != nil {
			return failed(fmt.Errorf("~/%s: %w", rel, err))
		}
		profile, err := s.cfg.AssistantIssuer.withDefaults().claudeProfile(ctx, "your environment's sign-in", token)
		if errors.Is(err, ErrAssistantSignInRefused) {
			return failed(fmt.Errorf("%s, refusing your environment's Claude token. Start a session to refresh it", strings.TrimSuffix(err.Error(), ": "+ErrAssistantSignInRefused.Error())))
		}
		if err != nil {
			return failed(err)
		}
		account, out.Email = profile.account(), profile.Account.Email
		out.Environment = "account " + profile.Account.UUID + ", organization " + profile.Organization.UUID
	case "codex":
		auth, err := parseCodexSignIn(data)
		if err != nil {
			return failed(fmt.Errorf("~/%s: %w", rel, err))
		}
		if auth.mode != "chatgpt" {
			account, out.Environment = auth.mode+auth.keyID, "an API key"
			break
		}
		if auth.Tokens == nil {
			return failed(fmt.Errorf("~/%s holds no tokens", rel))
		}
		chatgpt, err := codexAccount(auth.Tokens.IDToken)
		if err != nil {
			chatgpt, err = codexAccount(auth.Tokens.AccessToken)
		}
		if err != nil {
			return failed(fmt.Errorf("~/%s: %w", rel, err))
		}
		// The pin includes the assistant's own API key, which another
		// login of the same account does not share.
		account, out.Environment = "chatgpt:"+chatgpt, "ChatGPT account "+chatgpt
		pinned, _, _ := strings.Cut(string(pin), ":key=")
		pin = []byte(pinned)
	}
	out.State = protocol.AssistantAccountOther
	if account == string(pin) {
		out.State, out.Environment = protocol.AssistantAccountSame, ""
	}
	return out, nil
}
