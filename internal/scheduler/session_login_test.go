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
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/runtime"
)

const codexLogin = ".codex/auth.json"

// claudeOwnerLogin is an owner's Claude Code OAuth login with state a
// session must never get: the refresh token and an MCP server's login.
func claudeOwnerLogin(access, refresh string, expires time.Time) string {
	return fmt.Sprintf(`{"claudeAiOauth":{"accessToken":%q,"refreshToken":%q,"expiresAt":%d,"scopes":["user:inference","user:profile"],"subscriptionType":"max","rateLimitTier":"default_claude_max_20x"},"mcpOAuth":{"srv":{"accessToken":"mcp-secret"}}}`,
		access, refresh, expires.UnixMilli())
}

// codexAccess is a Codex access token for account expiring at exp.
func codexAccess(account string, exp time.Time) string {
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"alg":"none"}`)) + "." + enc(fmt.Appendf(nil, `{"exp":%d,"https://api.openai.com/auth":{"chatgpt_account_id":%q}}`, exp.Unix(), account)) + ".c2ln"
}

func codexOwnerLogin(access, refresh string) string {
	return fmt.Sprintf(`{"auth_mode":"chatgpt","OPENAI_API_KEY":null,"tokens":{"id_token":"id-grace","access_token":%q,"refresh_token":%q,"account_id":"acct-grace"},"last_refresh":"2026-11-03T10:00:00Z"}`,
		access, refresh)
}

// sessionLoginEnv is a shareEnv whose owner, Grace, has shared her account
// with Ada and is listed in SessionLoginAccounts, checked at issuer.
type sessionLoginEnv struct {
	*shareEnv
	issuer *fakeIssuer
	posts  *atomic.Int32
}

func newSessionLoginEnv(t *testing.T) *sessionLoginEnv {
	t.Helper()
	e := newShareEnv(t, func(cfg *Config) {
		cfg.Harnesses["codex"] = HarnessSpec{TUIArgs: []string{"fake-codex", "{task}"}}
		cfg.RunContainerTTL = -time.Second
	})
	if _, err := e.db.ShareAccount(t.Context(), e.owner.ID, e.member.ID); err != nil {
		t.Fatal(err)
	}
	f := &fakeIssuer{used: map[string]bool{}}
	posts := &atomic.Int32{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			posts.Add(1)
		}
		f.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	e.sched.cfg.SessionLoginAccounts = map[domain.MemberID]bool{e.owner.ID: true}
	e.sched.cfg.AssistantIssuer = AssistantIssuer{ClaudeToken: srv.URL + "/claude/token", ClaudeProfile: srv.URL + "/claude/profile", CodexToken: srv.URL + "/codex/token", Client: srv.Client()}
	return &sessionLoginEnv{shareEnv: e, issuer: f, posts: posts}
}

func (e *sessionLoginEnv) ownerFile(rel string) string {
	return filepath.Join(e.ownerHome, filepath.FromSlash(rel))
}

func (e *sessionLoginEnv) writeOwner(t *testing.T, rel, content string) {
	t.Helper()
	name := e.ownerFile(rel)
	if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func inodeOf(t *testing.T, name string) uint64 {
	t.Helper()
	info, err := os.Stat(name)
	if err != nil {
		t.Fatal(err)
	}
	return info.Sys().(*syscall.Stat_t).Ino
}

func TestSessionLoginCopyKeepsOnlyTheAccessToken(t *testing.T) {
	t.Parallel()
	expires := time.UnixMilli(1893456000000)
	for _, tc := range []struct {
		name, agent, owner, want string
	}{
		{"claude", "claude", claudeOwnerLogin("access-1", "refresh-1", expires),
			`{"claudeAiOauth":{"accessToken":"access-1","expiresAt":1893456000000,"rateLimitTier":"default_claude_max_20x","scopes":["user:inference","user:profile"],"subscriptionType":"max"}}`},
		{"codex", "codex", codexOwnerLogin("access-1", "refresh-1"),
			`{"OPENAI_API_KEY":null,"auth_mode":"chatgpt","last_refresh":"2026-11-03T10:00:00Z","tokens":{"access_token":"access-1","account_id":"acct-grace","id_token":"id-grace","refresh_token":""}}`},
		{"codex without auth_mode", "codex", `{"OPENAI_API_KEY":null,"tokens":{"id_token":"i","access_token":"a","refresh_token":"r"}}`,
			`{"OPENAI_API_KEY":null,"tokens":{"access_token":"a","id_token":"i","refresh_token":""}}`},
		{"claude API key", "claude", `{"claudeAiOauth":{"accessToken":"a","expiresAt":1}}`, ""},
		{"codex API key", "codex", `{"OPENAI_API_KEY":"sk-test","tokens":null}`, ""},
		{"codex API key mode", "codex", `{"auth_mode":"apikey","OPENAI_API_KEY":"sk-test","tokens":{"access_token":"a","refresh_token":"r"}}`, ""},
		{"codex ChatGPT without a refresh token", "codex", `{"auth_mode":"chatgpt","tokens":{"access_token":"a","refresh_token":""}}`, ""},
		{"other agent", "gemini", `{"access_token":"a","refresh_token":"r"}`, ""},
		{"not JSON", "claude", `not json`, ""},
	} {
		got, err := sessionLoginCopy(tc.agent, []byte(tc.owner))
		if tc.want == "" {
			if !errors.Is(err, errNotOAuthLogin) {
				t.Errorf("%s: copy = %s, %v; want errNotOAuthLogin", tc.name, got, err)
			}
			continue
		}
		if err != nil || string(got) != tc.want {
			t.Errorf("%s: copy = %s, %v\nwant %s", tc.name, got, err, tc.want)
		}
	}
}

// A listed account's claude or codex share mounts the run's own copy of
// the owner's login, read-only and inode-checked, over the launcher's login
// path, and nothing from the owner's home.
func TestSharedLaunchMountsASessionLoginForListedAccounts(t *testing.T) {
	t.Parallel()
	e := newSessionLoginEnv(t)
	in := time.Now().Add(8 * time.Hour)
	claude, codex := claudeOwnerLogin("access-grace", "grace~1", in), codexOwnerLogin(codexAccess("grace", in), "grace~1")
	e.writeOwner(t, claudeLogin, claude)
	e.writeOwner(t, codexLogin, codex)
	for _, tc := range []struct{ agent, rel, owner string }{
		{"claude", claudeLogin, claude},
		{"codex", codexLogin, codex},
	} {
		run, err := e.launch(t, tc.agent)
		if err != nil {
			t.Fatalf("%s: Launch: %v", tc.agent, err)
		}
		spec := e.rt.byName(string(run.ID)).spec
		e.assertOwnerExposedOnlyAt(t, spec)
		file, err := filepath.EvalSymlinks(e.sched.sessionLoginPath(run.ID, tc.agent))
		if err != nil {
			t.Fatal(err)
		}
		want := runtime.Mount{HostPath: file, ContainerPath: "/root/" + tc.rel, ReadOnly: true, CheckInode: true}
		if got, ok := mountFor(spec, want.ContainerPath); !ok || got != want {
			t.Fatalf("%s: login mount = %+v, want %+v", tc.agent, got, want)
		}
		copied, err := sessionLoginCopy(tc.agent, []byte(tc.owner))
		if err != nil {
			t.Fatal(err)
		}
		if got := readFile(t, file); got != string(copied) {
			t.Fatalf("%s: session login = %s, want %s", tc.agent, got, copied)
		}
		for name, mode := range map[string]os.FileMode{file: 0o600, filepath.Dir(file): 0o700 | os.ModeDir, e.sched.sessionLoginRoot(): 0o700 | os.ModeDir} {
			if info, statErr := os.Stat(name); statErr != nil || info.Mode() != mode {
				t.Fatalf("%s: %s = %v, %v; want mode %v", tc.agent, name, info, statErr, mode)
			}
		}
		if got := readFile(t, e.ownerFile(tc.rel)); got != tc.owner {
			t.Fatalf("%s: owner's login changed to %s", tc.agent, got)
		}
		if info, statErr := os.Lstat(filepath.Join(e.adaHome, filepath.FromSlash(tc.rel))); statErr != nil || !info.Mode().IsRegular() {
			t.Fatalf("%s: launcher mountpoint = %v, %v", tc.agent, info, statErr)
		}
		sc, err := e.sched.readSidecar(run.ID)
		if err != nil || sc.SessionLogin != tc.agent || sc.LoginMember != string(e.owner.ID) || len(sc.VerifiedLogins) != 1 {
			t.Fatalf("%s: sidecar = %+v, %v; want session login %s on %s", tc.agent, sc, err, tc.agent, e.owner.ID)
		}
	}
	if state := readFile(t, filepath.Join(e.adaHome, ".claude.json")); state != `{"hasCompletedOnboarding":true}` {
		t.Fatalf("launcher's .claude.json = %s, want onboarding marked", state)
	}
	if e.posts.Load() != 0 {
		t.Fatalf("%d refreshes for logins hours from expiry", e.posts.Load())
	}
}

// Every other share keeps mounting the owner's own login: an unlisted
// account, an API-key login, a claude login without a refresh token, and an
// agent other than claude and codex.
func TestSharedLaunchKeepsTheOwnersLoginOtherwise(t *testing.T) {
	t.Parallel()
	in := time.Now().Add(8 * time.Hour)
	for _, tc := range []struct {
		name, agent, rel, owner string
		unlisted                bool
	}{
		{"unlisted account", "claude", claudeLogin, claudeOwnerLogin("access-grace", "grace~1", in), true},
		{"codex API key", "codex", codexLogin, `{"OPENAI_API_KEY":"sk-test","tokens":null}`, false},
		{"claude without refresh token", "claude", claudeLogin, `{"claudeAiOauth":{"accessToken":"a","expiresAt":1}}`, false},
		{"omp", "omp", ".omp/agent", "", false},
	} {
		e := newSessionLoginEnv(t)
		if tc.unlisted {
			e.sched.cfg.SessionLoginAccounts = map[domain.MemberID]bool{"someone-else": true}
		}
		if tc.owner == "" {
			writeHomeFiles(t, e.ownerHome, ".omp/agent/agent.db")
		} else {
			e.writeOwner(t, tc.rel, tc.owner)
		}
		run, err := e.launch(t, tc.agent)
		if err != nil {
			t.Fatalf("%s: Launch: %v", tc.name, err)
		}
		spec := e.rt.byName(string(run.ID)).spec
		want := runtime.Mount{HostPath: e.ownerHome, Subpath: tc.rel, ContainerPath: "/root/" + tc.rel}
		if got, ok := mountFor(spec, want.ContainerPath); !ok || got != want {
			t.Fatalf("%s: login mount = %+v, want the owner's %+v", tc.name, got, want)
		}
		if _, err := os.Stat(filepath.Join(e.sched.sessionLoginRoot(), string(run.ID))); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s: session login directory = %v, want none", tc.name, err)
		}
		if sc, err := e.sched.readSidecar(run.ID); err != nil || sc.SessionLogin != "" || sc.LoginMember != string(e.owner.ID) {
			t.Fatalf("%s: sidecar = %+v, %v", tc.name, sc, err)
		}
	}
}

// A launch on a login that expires within sessionLoginAhead refreshes the
// owner's login first, under the CLI's own lock, and writes the rotated
// login back on the owner's inode; the copy gets the new access token only.
func TestSessionLoginRefreshesTheOwnersLoginInPlace(t *testing.T) {
	t.Parallel()
	e := newSessionLoginEnv(t)
	soon := time.Now().Add(5 * time.Minute)
	e.writeOwner(t, claudeLogin, claudeOwnerLogin("access-old", "grace~1", soon))
	e.writeOwner(t, codexLogin, codexOwnerLogin(codexAccess("grace", soon), "codex-grace~1"))
	before := map[string]uint64{claudeLogin: inodeOf(t, e.ownerFile(claudeLogin)), codexLogin: inodeOf(t, e.ownerFile(codexLogin))}
	var lockedDuringPost atomic.Bool
	lockDir := e.ownerFile(".claude/.oauth_refresh.lock")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/claude/token" {
			info, err := os.Stat(lockDir)
			lockedDuringPost.Store(err == nil && info.IsDir())
		}
		e.issuer.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	e.sched.cfg.AssistantIssuer.ClaudeToken = srv.URL + "/claude/token"

	run, err := e.launch(t, "claude")
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if !lockedDuringPost.Load() {
		t.Fatal("the refresh was sent without the Claude Code refresh lock held")
	}
	if _, err = os.Lstat(lockDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("refresh lock left behind: %v", err)
	}
	var owner struct {
		OAuth struct {
			AccessToken, RefreshToken string
			ExpiresAt                 int64
		} `json:"claudeAiOauth"`
		MCP json.RawMessage `json:"mcpOAuth"`
	}
	if err = json.Unmarshal([]byte(readFile(t, e.ownerFile(claudeLogin))), &owner); err != nil {
		t.Fatal(err)
	}
	if owner.OAuth.AccessToken != "access-grace" || owner.OAuth.RefreshToken != "grace~10" || time.UnixMilli(owner.OAuth.ExpiresAt).Before(time.Now().Add(50*time.Minute)) || owner.MCP == nil {
		t.Fatalf("owner's login after refresh = %+v", owner)
	}
	if got := inodeOf(t, e.ownerFile(claudeLogin)); got != before[claudeLogin] {
		t.Fatalf("owner's login inode = %d, want %d: the refresh replaced the file", got, before[claudeLogin])
	}
	copied := readFile(t, e.sched.sessionLoginPath(run.ID, "claude"))
	want, err := sessionLoginCopy("claude", []byte(readFile(t, e.ownerFile(claudeLogin))))
	if err != nil || copied != string(want) {
		t.Fatalf("session login = %s, %v; want %s", copied, err, want)
	}

	if _, err := e.launch(t, "codex"); err != nil {
		t.Fatalf("codex Launch: %v", err)
	}
	if got := inodeOf(t, e.ownerFile(codexLogin)); got != before[codexLogin] {
		t.Fatalf("owner's codex login inode = %d, want %d", got, before[codexLogin])
	}
	if got := readFile(t, e.ownerFile(codexLogin)); !strings.Contains(got, `"refresh_token":"codex-grace~10"`) || !strings.Contains(got, codexToken("codex-grace")) {
		t.Fatalf("owner's codex login after refresh = %s", got)
	}
	if info, err := os.Stat(e.ownerFile(".codex/auth.lock")); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("codex refresh lock = %v, %v; want the file the CLI locks too", info, err)
	}
	if e.posts.Load() != 1 {
		t.Fatalf("%d codex refreshes, want 1", e.posts.Load())
	}
}

// A refresh lock another process holds stops the server's refresh; one
// that went stale is removed and taken.
func TestSessionLoginRefreshRespectsTheCLILocks(t *testing.T) {
	t.Parallel()
	e := newSessionLoginEnv(t)
	soon := time.Now().Add(5 * time.Minute)
	claude := claudeOwnerLogin("access-old", "grace~1", soon)
	codex := codexOwnerLogin(codexAccess("grace", soon), "grace~1")
	e.writeOwner(t, claudeLogin, claude)
	e.writeOwner(t, codexLogin, codex)
	lockDir := e.ownerFile(".claude/.oauth_refresh.lock")
	if err := os.Mkdir(lockDir, 0o755); err != nil {
		t.Fatal(err)
	}
	lockFile, err := os.OpenFile(e.ownerFile(".codex/auth.lock"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lockFile.Close() }()
	if err := unix.Flock(int(lockFile.Fd()), unix.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for _, agent := range []string{"claude", "codex"} {
		wg.Go(func() { e.sched.refreshOwnerLogin(t.Context(), e.owner.ID, agent) })
	}
	wg.Wait()
	if e.posts.Load() != 0 || readFile(t, e.ownerFile(claudeLogin)) != claude || readFile(t, e.ownerFile(codexLogin)) != codex {
		t.Fatalf("%d refreshes under held locks; logins %s %s", e.posts.Load(), readFile(t, e.ownerFile(claudeLogin)), readFile(t, e.ownerFile(codexLogin)))
	}
	if _, err := os.Stat(lockDir); err != nil {
		t.Fatalf("a held lock was removed: %v", err)
	}

	stale := time.Now().Add(-2 * time.Minute)
	if err := os.Chtimes(lockDir, stale, stale); err != nil {
		t.Fatal(err)
	}
	e.sched.refreshOwnerLogin(t.Context(), e.owner.ID, "claude")
	if e.posts.Load() != 1 || !strings.Contains(readFile(t, e.ownerFile(claudeLogin)), `"refreshToken":"grace~10"`) {
		t.Fatalf("%d refreshes past a stale lock; login %s", e.posts.Load(), readFile(t, e.ownerFile(claudeLogin)))
	}
	if _, err := os.Lstat(lockDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale lock = %v, want removed", err)
	}
}

// A refresh the issuer refuses leaves the owner's login as it was.
func TestSessionLoginRefusedRefreshLeavesTheOwnersLogin(t *testing.T) {
	t.Parallel()
	e := newSessionLoginEnv(t)
	soon := time.Now().Add(5 * time.Minute)
	claude := claudeOwnerLogin("access-old", "revoked", soon)
	e.writeOwner(t, claudeLogin, claude)
	before := inodeOf(t, e.ownerFile(claudeLogin))
	run, err := e.launch(t, "claude")
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if e.posts.Load() != 1 || readFile(t, e.ownerFile(claudeLogin)) != claude || inodeOf(t, e.ownerFile(claudeLogin)) != before {
		t.Fatalf("%d refreshes; owner's login after refusal = %s", e.posts.Load(), readFile(t, e.ownerFile(claudeLogin)))
	}
	if !strings.Contains(readFile(t, e.sched.sessionLoginPath(run.ID, "claude")), `"accessToken":"access-old"`) {
		t.Fatalf("session login = %s, want the current access token", readFile(t, e.sched.sessionLoginPath(run.ID, "claude")))
	}
}

// The pump writes the owner's new login into the copy on its own inode,
// undoes a change to the copy, and signs the copy out once the account is
// no longer shared with the launcher.
func TestSessionLoginPumpFollowsTheOwner(t *testing.T) {
	t.Parallel()
	e := newSessionLoginEnv(t)
	in := time.Now().Add(8 * time.Hour)
	e.writeOwner(t, claudeLogin, claudeOwnerLogin("access-1", "grace~1", in))
	adaOwn := filepath.Join(e.adaHome, filepath.FromSlash(claudeLogin))
	run, err := e.launch(t, "claude")
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	file := e.sched.sessionLoginPath(run.ID, "claude")
	inode := inodeOf(t, file)
	ownerLogin := claudeOwnerLogin("access-2", "grace~2", in)
	e.writeOwner(t, claudeLogin, ownerLogin)
	want, err := sessionLoginCopy("claude", []byte(ownerLogin))
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range []string{"owner's new login", "tampered copy"} {
		e.sched.syncSessionLogins(t.Context())
		if got := readFile(t, file); got != string(want) || inodeOf(t, file) != inode {
			t.Fatalf("%s: session login = %s (inode %d), want %s (inode %d)", step, got, inodeOf(t, file), want, inode)
		}
		if err := os.WriteFile(file, []byte(`{"claudeAiOauth":{"accessToken":"ada-own"}}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.db.RevokeAccountShare(t.Context(), e.owner.ID, e.member.ID); err != nil {
		t.Fatal(err)
	}
	e.sched.syncSessionLogins(t.Context())
	if got := readFile(t, file); got != "{}" || inodeOf(t, file) != inode {
		t.Fatalf("session login after revoke = %s, want signed out", got)
	}
	if got := readFile(t, adaOwn); got != "" {
		t.Fatalf("launcher's own login = %q, want the untouched mountpoint", got)
	}
	if e.posts.Load() != 0 {
		t.Fatalf("%d refreshes", e.posts.Load())
	}
}

// A restarted server keeps pumping the copies of the runs it recovers, and
// a run's copy goes with its sidecar.
func TestSessionLoginSurvivesRecoveryAndGoesWithTheRun(t *testing.T) {
	t.Parallel()
	e := newSessionLoginEnv(t)
	in := time.Now().Add(8 * time.Hour)
	e.writeOwner(t, codexLogin, codexOwnerLogin(codexAccess("grace", in), "grace~1"))
	run, err := e.launch(t, "codex")
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if err = e.sched.Close(); err != nil {
		t.Fatal(err)
	}
	s2 := e.newScheduler(t, e.rt, newFakePTY())
	if err = s2.recoverRuns(t.Context()); err != nil {
		t.Fatalf("recoverRuns: %v", err)
	}
	s2.mu.Lock()
	entry := s2.runs[run.ID]
	s2.mu.Unlock()
	if entry == nil || entry.sessionLogin != "codex" || entry.loginMember != e.owner.ID {
		t.Fatalf("recovered entry = %+v, want codex's session login on %s", entry, e.owner.ID)
	}
	ownerLogin := codexOwnerLogin(codexAccess("grace", in.Add(time.Hour)), "grace~2")
	e.writeOwner(t, codexLogin, ownerLogin)
	s2.syncSessionLogins(t.Context())
	want, err := sessionLoginCopy("codex", []byte(ownerLogin))
	if err != nil {
		t.Fatal(err)
	}
	file := e.sched.sessionLoginPath(run.ID, "codex")
	if got := readFile(t, file); got != string(want) {
		t.Fatalf("session login after recovery = %s, want %s", got, want)
	}

	if err := s2.CloseRun(t.Context(), run.ID, e.member.ID, domain.RunMerged); err != nil {
		t.Fatalf("CloseRun: %v", err)
	}
	waitFor(t, "sidecar removed", func() bool {
		_, err := os.Stat(s2.sidecarPath(run.ID))
		return errors.Is(err, os.ErrNotExist)
	})
	if _, err := os.Stat(filepath.Dir(file)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("session login directory after close = %v, want removed", err)
	}

	orphan := filepath.Join(s2.sessionLoginRoot(), "run-gone")
	if err := os.MkdirAll(orphan, 0o700); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * scratchOrphanAge)
	if err := os.Chtimes(orphan, old, old); err != nil {
		t.Fatal(err)
	}
	s2.sweepSessionLogins(t.Context())
	if _, err := os.Stat(orphan); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("orphan session login = %v, want swept", err)
	}
}

func TestLoginDue(t *testing.T) {
	t.Parallel()
	now := time.Now()
	for _, tc := range []struct {
		name, agent, login string
		due                bool
	}{
		{"claude soon", "claude", claudeOwnerLogin("a", "r", now.Add(10*time.Minute)), true},
		{"claude later", "claude", claudeOwnerLogin("a", "r", now.Add(20*time.Minute)), false},
		{"claude expired", "claude", claudeOwnerLogin("a", "r", now.Add(-time.Hour)), true},
		{"codex soon", "codex", codexOwnerLogin(codexAccess("g", now.Add(10*time.Minute)), "r"), true},
		{"codex later", "codex", codexOwnerLogin(codexAccess("g", now.Add(time.Hour)), "r"), false},
		{"codex opaque token", "codex", codexOwnerLogin("opaque", "r"), false},
	} {
		if got := loginDue(tc.agent, []byte(tc.login), now); got != tc.due {
			t.Errorf("%s: due = %v, want %v", tc.name, got, tc.due)
		}
	}
}
