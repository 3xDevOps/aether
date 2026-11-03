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

func (s *Scheduler) sessionLoginPath(run domain.RunID, agent string) string {
	return filepath.Join(s.sessionLoginRoot(), string(run), path.Base(assistantSignInRel[agent]))
}

// sessionLoginMounts mounts, at the agent's login path, a copy of the
// account owner's login without its refresh token, written for run alone.
// It returns no mount, for loginMounts to share the owner's own login,
// unless the account is one of SessionLoginAccounts and holds a claude or
// codex OAuth login.
func (s *Scheduler) sessionLoginMounts(ctx context.Context, run *domain.Run, launcher domain.MemberID, profile harness.Profile, home, user string) ([]runtime.Mount, error) {
	account := run.AccountMemberID
	rel, known := assistantSignInRel[profile.Name]
	if !known || !s.cfg.SessionLoginAccounts[account] {
		return nil, nil
	}
	if paths, err := profile.LoginPaths(); err != nil || !slices.Equal(paths, []string{rel}) {
		return nil, nil
	}
	owner, err := s.cfg.Homes.ReadLogin(account, rel)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("scheduler: %w", err)
	}
	if _, copyErr := sessionLoginCopy(profile.Name, owner); errors.Is(copyErr, errNotOAuthLogin) {
		return nil, nil
	}
	if loginDue(profile.Name, owner, time.Now()) {
		s.refreshOwnerLogin(ctx, account, profile.Name)
		if owner, err = s.cfg.Homes.ReadLogin(account, rel); err != nil {
			return nil, fmt.Errorf("scheduler: %w", err)
		}
	}
	login, err := sessionLoginCopy(profile.Name, owner)
	if err != nil {
		return nil, fmt.Errorf("scheduler: copy %s's %s login: %w", account, profile.Name, err)
	}
	file, err := s.writeSessionLogin(run.ID, profile.Name, login, user)
	if err != nil {
		return nil, err
	}
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
func (s *Scheduler) writeSessionLogin(run domain.RunID, agent string, login []byte, user string) (string, error) {
	file := s.sessionLoginPath(run, agent)
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
func (s *Scheduler) rewriteSessionLogin(run domain.RunID, agent string, login []byte) error {
	file := s.sessionLoginPath(run, agent)
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
		return rotated, err
	})
	if err != nil {
		slog.Warn("scheduler: refresh the account's login for session logins", "account", account, "agent", agent, "error", err)
	}
}

// pumpSessionLogins keeps every live session login copy on its owner's
// current login until ctx ends.
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

// syncSessionLogins refreshes each account's login at most once for all
// its live session login runs, and writes each run its copy, or a signed-out
// login once the account is no longer shared with the run's launcher.
func (s *Scheduler) syncSessionLogins(ctx context.Context) {
	groups := map[sessionLoginKey][]sessionLoginRun{}
	s.mu.Lock()
	for id, entry := range s.runs {
		if entry.sessionLogin == "" || entry.loginMember == "" || entry.retained || entry.status.Terminal() {
			continue
		}
		key := sessionLoginKey{account: entry.loginMember, agent: entry.sessionLogin}
		groups[key] = append(groups[key], sessionLoginRun{run: id, launcher: entry.memberID})
	}
	s.mu.Unlock()
	for key, runs := range groups {
		s.syncSessionLoginGroup(ctx, key, runs)
	}
}

func (s *Scheduler) syncSessionLoginGroup(ctx context.Context, key sessionLoginKey, runs []sessionLoginRun) {
	shared := map[domain.MemberID]bool{}
	anyShared := false
	for _, r := range runs {
		if _, seen := shared[r.launcher]; seen {
			continue
		}
		ok, err := s.cfg.Store.AccountSharedWith(ctx, key.account, r.launcher)
		if err != nil {
			slog.Warn("scheduler: check the share behind session logins", "account", key.account, "launcher", r.launcher, "error", err)
			return
		}
		shared[r.launcher] = ok
		anyShared = anyShared || ok
	}
	var login []byte
	if anyShared {
		login = s.ownerSessionLogin(ctx, key)
	}
	for _, r := range runs {
		want := login
		if !shared[r.launcher] {
			want = signedOutLogin
		}
		if want == nil {
			continue
		}
		if err := s.rewriteSessionLogin(r.run, key.agent, want); err != nil {
			slog.Warn("scheduler: update the session login", "run", r.run, "account", key.account, "error", err)
		}
	}
}

// ownerSessionLogin is the copy of the owner's current login, refreshed
// first when due, or nil when the owner holds no OAuth login to copy.
func (s *Scheduler) ownerSessionLogin(ctx context.Context, key sessionLoginKey) []byte {
	rel := assistantSignInRel[key.agent]
	owner, err := s.cfg.Homes.ReadLogin(key.account, rel)
	if err == nil && loginDue(key.agent, owner, time.Now()) {
		s.refreshOwnerLogin(ctx, key.account, key.agent)
		owner, err = s.cfg.Homes.ReadLogin(key.account, rel)
	}
	var login []byte
	if err == nil {
		login, err = sessionLoginCopy(key.agent, owner)
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
