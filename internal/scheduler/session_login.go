package scheduler

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/harness"
	"github.com/3xDevOps/Aether/internal/memberhome"
	"github.com/3xDevOps/Aether/internal/runtime"
)

const (
	sessionLoginDirName = "logins"
	// sessionLoginInterval is how often live session login copies follow
	// their owner's login.
	sessionLoginInterval = 30 * time.Second
	// sessionLoginAhead is how long before its access token expires the
	// server refreshes an owner's login for session login copies.
	sessionLoginAhead = 15 * time.Minute
	// ownerLoginReads is how often an empty or unparsable owner login is
	// read before it counts: both CLIs and the server rewrite it in place.
	ownerLoginReads = 5
	ownerLoginRetry = 50 * time.Millisecond
)

// sessionLoginLocks are the locks each CLI rereads and refreshes its login
// under.
var sessionLoginLocks = map[string]memberhome.LoginLock{
	"claude": {Name: ".oauth_refresh.lock"},
	"codex":  {Name: "auth.lock", Flock: true},
}

// signedOutLogin reads as no login to both CLIs.
var signedOutLogin = []byte("{}")

var errNotOAuthLogin = errors.New("not an OAuth login")

func (s *Scheduler) sessionLoginRoot() string {
	return filepath.Join(s.cfg.StateDir, sessionLoginDirName)
}

// sessionLoginPath is run's copy of account's login. A run that moves to
// another account mounts another file, so a write for the old account never
// reaches the new container.
func (s *Scheduler) sessionLoginPath(run domain.RunID, account domain.MemberID, agent string) string {
	return filepath.Join(s.sessionLoginRoot(), string(run), string(account), path.Base(assistantSignInRel[agent]))
}

// sessionLoginAccount reports whether a launch of profile on account mounts a
// session login copy rather than the owner's login.
func (s *Scheduler) sessionLoginAccount(account domain.MemberID, profile harness.Profile) bool {
	rel, known := assistantSignInRel[profile.Name]
	if !known || !s.cfg.SessionLoginAccounts[account] {
		return false
	}
	paths, err := profile.LoginPaths()
	return err == nil && slices.Equal(paths, []string{rel})
}

// ownerLogin reads account's login for agent, rereading an empty or
// unparsable one so that a read torn by an in-place rewrite never passes for
// a missing login.
func (s *Scheduler) ownerLogin(account domain.MemberID, agent string) ([]byte, error) {
	var data []byte
	var err error
	for i := range ownerLoginReads {
		if i > 0 {
			time.Sleep(ownerLoginRetry)
		}
		data, err = s.cfg.Homes.ReadLogin(account, assistantSignInRel[agent])
		if err == nil && json.Valid(data) || err != nil && !errors.Is(err, fs.ErrNotExist) {
			return data, err
		}
	}
	if err == nil {
		err = fmt.Errorf("memberhome: %s in %q is not JSON", assistantSignInRel[agent], account)
	}
	return nil, err
}

// sessionLogin is the copy of the owner's login a session gets: the OAuth
// login without its refresh token, or a signed-out login when the owner holds
// none.
func sessionLogin(agent string, owner []byte) ([]byte, error) {
	login, err := sessionLoginCopy(agent, owner)
	if errors.Is(err, errNotOAuthLogin) {
		return signedOutLogin, nil
	}
	return login, err
}

// sessionLoginMounts mounts, at the agent's login path, run's copy of the
// account owner's login, written for run alone. A missing owner login refuses
// the launch as loginMounts would.
func (s *Scheduler) sessionLoginMounts(ctx context.Context, run *domain.Run, launcher domain.MemberID, profile harness.Profile, home, user string) ([]runtime.Mount, error) {
	account := run.AccountMemberID
	owner, err := s.ownerLogin(account, profile.Name)
	if errors.Is(err, fs.ErrNotExist) {
		_, err = s.accountLogins(ctx, account, profile)
		if err == nil {
			err = fmt.Errorf("scheduler: %s's %s login disappeared during the launch", account, profile.Name)
		}
		return nil, err
	}
	if err != nil {
		return nil, fmt.Errorf("scheduler: %w", err)
	}
	if loginDue(profile.Name, owner, time.Now()) {
		s.refreshOwnerLogin(ctx, account, profile.Name)
		if owner, err = s.ownerLogin(account, profile.Name); err != nil {
			return nil, fmt.Errorf("scheduler: %w", err)
		}
	}
	login, err := sessionLogin(profile.Name, owner)
	if err != nil {
		return nil, fmt.Errorf("scheduler: copy %s's %s login: %w", account, profile.Name, err)
	}
	file, err := s.writeSessionLogin(run.ID, account, profile.Name, login, user)
	if err != nil {
		return nil, err
	}
	rel := assistantSignInRel[profile.Name]
	if err := s.cfg.Homes.PrepareLoginMountpoint(launcher, rel, false); err != nil {
		return nil, fmt.Errorf("scheduler: %w", err)
	}
	for state, keys := range profile.BorrowedState {
		if err := s.cfg.Homes.MarkBorrowedState(launcher, state, keys); err != nil {
			return nil, fmt.Errorf("scheduler: %w", err)
		}
	}
	// Read-only: the ownership pass chowns only directories, and neither
	// CLI writes a login it holds no refresh token for.
	return []runtime.Mount{{HostPath: file, ContainerPath: path.Join(home, rel), ReadOnly: true, CheckInode: true}}, nil
}

// writeSessionLogin writes run's copy of the agent's login with one write
// after the truncate, on the file's own inode: a rename would detach the
// container's bind of it.
func (s *Scheduler) writeSessionLogin(run domain.RunID, account domain.MemberID, agent string, login []byte, user string) (string, error) {
	file := s.sessionLoginPath(run, account, agent)
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return "", fmt.Errorf("scheduler: create the session login directory: %w", err)
	}
	f, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return "", fmt.Errorf("scheduler: write the session login: %w", err)
	}
	_, err = f.Write(login)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err == nil && user != "" {
		var uid, gid int
		if uid, gid, err = parseNumericUser(user); err == nil {
			err = os.Chown(file, uid, gid)
		}
	}
	if err != nil {
		return "", fmt.Errorf("scheduler: write the session login: %w", err)
	}
	return file, nil
}

// rewriteSessionLogin writes login over run's copy when the copy holds
// anything else. It never creates the copy: a new file is not the inode the
// container mounts.
func (s *Scheduler) rewriteSessionLogin(run domain.RunID, account domain.MemberID, agent string, login []byte) error {
	file := s.sessionLoginPath(run, account, agent)
	current, err := os.ReadFile(file)
	if err != nil {
		return fmt.Errorf("scheduler: read the session login: %w", err)
	}
	if bytes.Equal(current, login) {
		return nil
	}
	f, err := os.OpenFile(file, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		return fmt.Errorf("scheduler: rewrite the session login: %w", err)
	}
	_, err = f.Write(login)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("scheduler: rewrite the session login: %w", err)
	}
	return nil
}

func (s *Scheduler) removeSessionLogin(run domain.RunID) {
	if err := os.RemoveAll(filepath.Join(s.sessionLoginRoot(), string(run))); err != nil {
		slog.Warn("scheduler: remove session login", "run", run, "error", err)
	}
}

// sweepSessionLogins removes session login copies no run owns any more.
func (s *Scheduler) sweepSessionLogins(ctx context.Context) {
	entries, err := os.ReadDir(s.sessionLoginRoot())
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			slog.Warn("scheduler: list session logins", "error", err)
		}
		return
	}
	owned, err := s.ownedScratch(ctx)
	if err != nil {
		slog.Warn("scheduler: session login sweep", "error", err)
		return
	}
	for _, entry := range entries {
		name := entry.Name()
		info, err := entry.Info()
		if owned[name] || err != nil || time.Since(info.ModTime()) < scratchOrphanAge {
			continue
		}
		if _, err := os.Stat(s.sidecarPath(domain.RunID(name))); err == nil {
			continue
		}
		s.removeSessionLogin(domain.RunID(name))
	}
}

// refreshOwnerLogin refreshes account's agent login with the agent's issuer
// when its access token expires within sessionLoginAhead, under the lock the
// agent's own CLI rereads the login under, so the owner's CLIs adopt the
// rotated login instead of spending the refresh token again. A refusal leaves
// the login as it is: the sessions show the agent's own sign-in error once the
// access token expires.
func (s *Scheduler) refreshOwnerLogin(ctx context.Context, account domain.MemberID, agent string) {
	issuer := s.cfg.AssistantIssuer.withDefaults()
	refreshed := false
	err := s.cfg.Homes.RefreshLogin(account, assistantSignInRel[agent], sessionLoginLocks[agent], func(old []byte) ([]byte, error) {
		if !loginDue(agent, old, time.Now()) {
			return nil, nil
		}
		var rotated []byte
		var err error
		if agent == "claude" {
			_, rotated, err = issuer.refreshClaude(ctx, old)
		} else {
			_, rotated, err = issuer.refreshCodex(ctx, old)
		}
		refreshed = rotated != nil
		return rotated, err
	})
	switch {
	case err != nil:
		slog.Warn("scheduler: refresh the account's login for session logins", "account", account, "agent", agent, "error", err)
	case refreshed:
		slog.Info("scheduler: refreshed the account's login for session logins", "account", account, "agent", agent)
	}
}

func (s *Scheduler) pumpSessionLogins(ctx context.Context) {
	tick := time.NewTicker(sessionLoginInterval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			s.syncSessionLogins(ctx)
		}
	}
}

type sessionLoginKey struct {
	account domain.MemberID
	agent   string
}

type sessionLoginRun struct {
	run      domain.RunID
	launcher domain.MemberID
}

// syncSessionLogins writes every session login copy its owner's current
// login, refreshed first when due, or a signed-out login once the account is
// no longer shared with the run's launcher. Retained runs are kept current
// too, since a reopen thaws them with the copy they hold. Signed-out copies
// are written before any refresh, so a slow issuer cannot delay them.
func (s *Scheduler) syncSessionLogins(ctx context.Context) {
	groups := map[sessionLoginKey][]sessionLoginRun{}
	s.mu.Lock()
	for id, entry := range s.runs {
		if entry.sessionLogin == "" || entry.loginMember == "" || entry.status.Terminal() && !entry.retained {
			continue
		}
		key := sessionLoginKey{account: entry.loginMember, agent: entry.sessionLogin}
		groups[key] = append(groups[key], sessionLoginRun{run: id, launcher: entry.memberID})
	}
	s.mu.Unlock()
	live := map[sessionLoginKey][]sessionLoginRun{}
	for key, runs := range groups {
		for _, r := range runs {
			shared, err := s.cfg.Store.AccountSharedWith(ctx, key.account, r.launcher)
			switch {
			case err != nil:
				slog.Warn("scheduler: check the share behind a session login", "run", r.run, "account", key.account, "error", err)
			case shared:
				live[key] = append(live[key], r)
			default:
				s.writeSessionLoginCopy(ctx, key, r, signedOutLogin)
			}
		}
	}
	for key, runs := range live {
		login := s.ownerSessionLogin(ctx, key)
		if login == nil {
			continue
		}
		for _, r := range runs {
			s.writeSessionLoginCopy(ctx, key, r, login)
		}
	}
}

// writeSessionLoginCopy writes login over r's copy. A login that is not
// signed out is written only while the share still holds, under the lock a
// revocation takes, so a revocation during a refresh never receives the
// refreshed token.
func (s *Scheduler) writeSessionLoginCopy(ctx context.Context, key sessionLoginKey, r sessionLoginRun, login []byte) {
	if !bytes.Equal(login, signedOutLogin) {
		if f := s.usageFallbackSeam(); f != nil && f.AuthorizationMu != nil {
			f.AuthorizationMu.Lock()
			defer f.AuthorizationMu.Unlock()
		}
		shared, err := s.cfg.Store.AccountSharedWith(ctx, key.account, r.launcher)
		if err != nil {
			slog.Warn("scheduler: check the share behind a session login", "run", r.run, "account", key.account, "error", err)
			return
		}
		if !shared {
			login = signedOutLogin
		}
	}
	if err := s.rewriteSessionLogin(r.run, key.account, key.agent, login); err != nil {
		slog.Warn("scheduler: update the session login", "run", r.run, "account", key.account, "error", err)
	}
}

// ownerSessionLogin is the session copy of the owner's current login,
// refreshed first when due: signed out when the owner holds no OAuth login,
// nil when the login cannot be read now.
func (s *Scheduler) ownerSessionLogin(ctx context.Context, key sessionLoginKey) []byte {
	owner, err := s.ownerLogin(key.account, key.agent)
	if err == nil && loginDue(key.agent, owner, time.Now()) {
		s.refreshOwnerLogin(ctx, key.account, key.agent)
		owner, err = s.ownerLogin(key.account, key.agent)
	}
	if errors.Is(err, fs.ErrNotExist) {
		return signedOutLogin
	}
	var login []byte
	if err == nil {
		login, err = sessionLogin(key.agent, owner)
	}
	if err != nil {
		slog.Warn("scheduler: read the account's login for session logins", "account", key.account, "agent", key.agent, "error", err)
		return nil
	}
	return login
}

// sessionLoginCopy derives a session's copy of an OAuth login: the access
// token and what the CLI shows of the plan, never the refresh token or
// anything else the file holds. Codex loads a login without the
// refresh_token key as signed out, so the copy keeps the key, empty.
func sessionLoginCopy(agent string, data []byte) ([]byte, error) {
	switch agent {
	case "claude":
		var file struct {
			OAuth map[string]json.RawMessage `json:"claudeAiOauth"`
		}
		if json.Unmarshal(data, &file) != nil || !jsonString(file.OAuth["refreshToken"]) || !jsonString(file.OAuth["accessToken"]) {
			return nil, errNotOAuthLogin
		}
		oauth := map[string]json.RawMessage{}
		for _, key := range []string{"accessToken", "expiresAt", "scopes", "subscriptionType", "rateLimitTier"} {
			if v, ok := file.OAuth[key]; ok {
				oauth[key] = v
			}
		}
		return json.Marshal(map[string]any{"claudeAiOauth": oauth})
	case "codex":
		var file struct {
			Mode        json.RawMessage            `json:"auth_mode"`
			Key         *string                    `json:"OPENAI_API_KEY"`
			Tokens      map[string]json.RawMessage `json:"tokens"`
			LastRefresh json.RawMessage            `json:"last_refresh"`
		}
		if json.Unmarshal(data, &file) != nil || !jsonString(file.Tokens["refresh_token"]) || !jsonString(file.Tokens["access_token"]) {
			return nil, errNotOAuthLogin
		}
		var mode string
		if file.Mode != nil && json.Unmarshal(file.Mode, &mode) != nil {
			return nil, errNotOAuthLogin
		}
		if mode != "chatgpt" && (mode != "" || (file.Key != nil && *file.Key != "")) {
			return nil, errNotOAuthLogin
		}
		tokens := map[string]json.RawMessage{"refresh_token": json.RawMessage(`""`)}
		for _, key := range []string{"id_token", "access_token", "account_id"} {
			if v, ok := file.Tokens[key]; ok {
				tokens[key] = v
			}
		}
		out := map[string]any{"OPENAI_API_KEY": nil, "tokens": tokens}
		if mode != "" {
			out["auth_mode"] = mode
		}
		if file.LastRefresh != nil {
			out["last_refresh"] = file.LastRefresh
		}
		return json.Marshal(out)
	}
	return nil, errNotOAuthLogin
}

func jsonString(raw json.RawMessage) bool {
	var v string
	return json.Unmarshal(raw, &v) == nil && v != ""
}

// loginDue reports whether the login's access token expires within
// sessionLoginAhead of now: Claude Code's expiresAt, or the exp claim of
// Codex's access token. A login whose expiry cannot be read is never due.
func loginDue(agent string, data []byte, now time.Time) bool {
	var expiry time.Time
	switch agent {
	case "claude":
		var file struct {
			OAuth struct {
				ExpiresAt int64 `json:"expiresAt"`
			} `json:"claudeAiOauth"`
		}
		if json.Unmarshal(data, &file) != nil || file.OAuth.ExpiresAt == 0 {
			return false
		}
		expiry = time.UnixMilli(file.OAuth.ExpiresAt)
	case "codex":
		var file struct {
			Tokens struct {
				AccessToken string `json:"access_token"`
			} `json:"tokens"`
		}
		if json.Unmarshal(data, &file) != nil {
			return false
		}
		parts := strings.Split(file.Tokens.AccessToken, ".")
		if len(parts) != 3 {
			return false
		}
		payload, err := base64.RawURLEncoding.DecodeString(parts[1])
		var claims struct {
			Exp int64 `json:"exp"`
		}
		if err != nil || json.Unmarshal(payload, &claims) != nil || claims.Exp == 0 {
			return false
		}
		expiry = time.Unix(claims.Exp, 0)
	default:
		return false
	}
	return expiry.Sub(now) < sessionLoginAhead
}
