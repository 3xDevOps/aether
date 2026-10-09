package scheduler

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/runtime"
)

// Only the runtime/provider boundary is fake. The scheduler creates/adopts the
// real test member Environment, drains independent blocking streams, waits on
// command exit and completes its existing home/signing configuration path.
type githubOAuthTestRuntime struct {
	*fakeRuntime
	started     chan *githubOAuthTestExec
	startErr    error
	startGate   <-chan struct{}
	failDestroy atomic.Bool
}

type githubOAuthTestExec struct {
	identity     runtime.ExecIdentity
	spec         runtime.ExecSpec
	stdout       *io.PipeReader
	stdoutWriter *io.PipeWriter
	stderr       *io.PipeReader
	stderrWriter *io.PipeWriter
	done         chan struct{}
	exitOnce     sync.Once
	mu           sync.Mutex
	code         int
	events       []string
	stopErr      error
}

func (r *githubOAuthTestRuntime) StartExecTTY(context.Context, runtime.ID, runtime.ExecSpec) (runtime.ManagedExec, error) {
	return nil, errors.New("OAuth must not use a visible TTY")
}

func (r *githubOAuthTestRuntime) RecoverExec(context.Context, runtime.ExecIdentity) (runtime.ManagedExec, error) {
	return nil, runtime.ErrExecUnavailable
}

func (r *githubOAuthTestRuntime) StartExecPipe(ctx context.Context, container runtime.ID, spec runtime.ExecSpec) (runtime.ManagedExec, error) {
	if r.startGate != nil {
		select {
		case <-r.startGate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if r.startErr != nil {
		return nil, r.startErr
	}
	out, outWriter := io.Pipe()
	errOut, errWriter := io.Pipe()
	e := &githubOAuthTestExec{
		identity: runtime.ExecIdentity{ContainerID: container, ExecID: spec.CreationKey, CreationKey: spec.CreationKey, ClaimToken: "test-claim"},
		spec:     spec, stdout: out, stdoutWriter: outWriter, stderr: errOut, stderrWriter: errWriter, done: make(chan struct{}),
	}
	r.started <- e
	return e, nil
}

func (r *githubOAuthTestRuntime) Destroy(ctx context.Context, id runtime.ID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.failDestroy.Load() {
		return errors.New("test: destroy unavailable")
	}
	return r.fakeRuntime.Destroy(ctx, id)
}

func (e *githubOAuthTestExec) Identity() runtime.ExecIdentity { return e.identity }
func (e *githubOAuthTestExec) Attachment() runtime.Attachment { return e }
func (e *githubOAuthTestExec) Stdin() io.WriteCloser          { return githubOAuthTestStdin{} }
func (e *githubOAuthTestExec) Stdout() io.Reader              { return e.stdout }
func (e *githubOAuthTestExec) Stderr() io.Reader              { return e.stderr }
func (e *githubOAuthTestExec) Resize(context.Context, uint, uint) error {
	return errors.New("not a TTY")
}
func (e *githubOAuthTestExec) Close() error { return e.Detach() }

type githubOAuthTestStdin struct{}

func (githubOAuthTestStdin) Write(p []byte) (int, error) { return len(p), nil }
func (githubOAuthTestStdin) Close() error                { return nil }

func (e *githubOAuthTestExec) Status(context.Context) (runtime.ExecState, error) {
	select {
	case <-e.done:
		e.mu.Lock()
		code := e.code
		e.mu.Unlock()
		return runtime.ExecState{Exited: true, ExitCode: &code}, nil
	default:
		return runtime.ExecState{Running: true, Attached: true}, nil
	}
}

func (e *githubOAuthTestExec) Wait(ctx context.Context) (runtime.ExitStatus, error) {
	select {
	case <-e.done:
		e.mu.Lock()
		defer e.mu.Unlock()
		return runtime.ExitStatus{Code: e.code}, nil
	case <-ctx.Done():
		return runtime.ExitStatus{}, ctx.Err()
	}
}

func (e *githubOAuthTestExec) finish(code int) {
	e.exitOnce.Do(func() {
		e.mu.Lock()
		e.code = code
		e.mu.Unlock()
		_ = e.stdoutWriter.Close()
		_ = e.stderrWriter.Close()
		close(e.done)
	})
}

func (e *githubOAuthTestExec) Stop(ctx context.Context, _ time.Duration) (runtime.ExitStatus, error) {
	e.mu.Lock()
	e.events = append(e.events, "stop")
	err := e.stopErr
	e.mu.Unlock()
	if err != nil {
		return runtime.ExitStatus{}, err
	}
	e.finish(143)
	return e.Wait(ctx)
}

func (e *githubOAuthTestExec) Detach() error {
	e.mu.Lock()
	e.events = append(e.events, "detach")
	e.mu.Unlock()
	_ = e.stdout.Close()
	_ = e.stderr.Close()
	return nil
}

func newGitHubOAuthTestEnv(t *testing.T) (*testEnv, *githubOAuthTestRuntime, *atomic.Value) {
	t.Helper()
	var rt *githubOAuthTestRuntime
	e := newTestEnv(t, func(cfg *Config) {
		rt = &githubOAuthTestRuntime{fakeRuntime: cfg.Runtime.(*fakeRuntime), started: make(chan *githubOAuthTestExec, 16)}
		cfg.Runtime = rt
	})
	status := &atomic.Value{}
	status.Store(`{"hosts":{}}`)
	e.rt.execHandler = func(_ runtime.ID, argv []string) (int, string, error) {
		switch {
		case slices.Contains(argv, "--version"):
			return 0, ghVersionCurrent, nil
		case slices.Contains(argv, "status"):
			return 0, status.Load().(string), nil
		case slices.Contains(argv, "list"):
			return 0, "aether\t" + homeKeyFingerprint(t, e) + "\t2026-01-01\t1\tsigning\n", nil
		default:
			return 0, "", nil
		}
	}
	return e, rt, status
}

func startGitHubOAuthTest(t *testing.T, e *testEnv, member domain.MemberID) protocol.GitHubOAuthResult {
	t.Helper()
	result, err := e.sched.StartGitHubOAuth(t.Context(), member)
	if err != nil || result.SessionID == "" {
		t.Fatalf("start = %+v, %v", result, err)
	}
	return result
}

func nextGitHubOAuthExec(t *testing.T, rt *githubOAuthTestRuntime) *githubOAuthTestExec {
	t.Helper()
	select {
	case command := <-rt.started:
		return command
	case <-time.After(5 * time.Second):
		t.Fatal("native OAuth command did not start")
		return nil
	}
}

func publishGitHubDeviceCode(t *testing.T, command *githubOAuthTestExec) {
	t.Helper()
	if _, err := io.WriteString(command.stderrWriter, "! First copy your one-time code: ABCD-1234\nOpen this URL to continue in your web browser: https://github.com/login/device\n"); err != nil {
		t.Fatalf("write device output: %v", err)
	}
	// Force another Read, so the scanner has processed both device lines
	// before tests observe status rather than racing a buffered Scan.
	if _, err := io.WriteString(command.stderrWriter, "\n"); err != nil {
		t.Fatalf("flush device output: %v", err)
	}
}

func waitGitHubOAuthAttempt(t *testing.T, e *testEnv, member domain.MemberID) protocol.GitHubOAuthResult {
	t.Helper()
	e.sched.githubOAuthMu.Lock()
	a := e.sched.githubOAuth[member]
	e.sched.githubOAuthMu.Unlock()
	if a == nil {
		t.Fatal("no OAuth attempt")
	}
	select {
	case <-a.done:
		e.sched.githubOAuthMu.Lock()
		defer e.sched.githubOAuthMu.Unlock()
		return a.result
	case <-time.After(5 * time.Second):
		t.Fatal("OAuth attempt did not finish")
		return protocol.GitHubOAuthResult{}
	}
}

func requireGitHubOAuthStopped(t *testing.T, command *githubOAuthTestExec) {
	t.Helper()
	command.mu.Lock()
	defer command.mu.Unlock()
	if !slices.Equal(command.events, []string{"stop", "detach"}) {
		t.Errorf("owned command cleanup = %v, want exactly Stop then Detach", command.events)
	}
}

func TestGitHubOAuthCompletesNativeConnectionWithoutTerminalUI(t *testing.T) {
	t.Parallel()
	requireSSHKeygen(t)
	e, rt, auth := newGitHubOAuthTestEnv(t)
	started := startGitHubOAuthTest(t, e, e.member.ID)
	command := nextGitHubOAuthExec(t, rt)
	publishGitHubDeviceCode(t, command)
	pending, err := e.sched.GitHubOAuthStatus(t.Context(), e.member.ID, started.SessionID)
	if err != nil || pending.State != "pending" || pending.UserCode != "ABCD-1234" || pending.VerificationURL != githubDeviceURL {
		t.Fatalf("pending = %+v, %v", pending, err)
	}
	if command.identity.ContainerID != e.sched.lookupLiveTerminal(e.member.ID).containerID || command.spec.WorkingDir == "" {
		t.Fatalf("authorization did not use member Environment: %+v", command.spec)
	}
	if command.spec.Cols != 0 || command.spec.Rows != 0 || !slices.Contains(command.spec.Env, "GH_PROMPT_DISABLED=1") || !slices.Contains(command.spec.Env, "NO_COLOR=1") {
		t.Fatalf("authorization not noninteractive: %+v", command.spec)
	}
	// Human approval must not own the terminal lock.
	if _, err = e.sched.EnsureTerminal(t.Context(), e.member.ID); err != nil {
		t.Fatalf("EnsureTerminal during approval: %v", err)
	}
	auth.Store(ghStatusLoggedIn)
	command.finish(0)
	result := waitGitHubOAuthAttempt(t, e, e.member.ID)
	if result.State != "connected" || result.Login != "octocat" || result.Connection == nil || result.Connection.Fingerprint == "" || result.UserCode != "" {
		t.Fatalf("finished = %+v", result)
	}
	requireGitHubOAuthStopped(t, command)
	home, err := e.cfg.Homes.Path(e.member.ID)
	if err != nil {
		t.Fatal(err)
	}
	config, err := os.ReadFile(filepath.Join(home, ".gitconfig"))
	if err != nil || !strings.Contains(string(config), "gpgsign = true") {
		t.Fatalf("native signing config = %q, %v", config, err)
	}
	calls := e.rt.execRuns()
	if !slices.ContainsFunc(calls, func(call fakeExecCall) bool { return slices.Contains(call.argv, "setup-git") }) ||
		!slices.ContainsFunc(calls, func(call fakeExecCall) bool { return slices.Contains(call.argv, "add") }) {
		t.Fatalf("native git/key setup missing from calls: %+v", calls)
	}

	before := len(calls)
	fresh, err := e.sched.GitHubOAuthStatus(t.Context(), e.member.ID, "")
	if err != nil || fresh.State != "connected" {
		t.Fatalf("fresh status = %+v, %v", fresh, err)
	}
	for _, call := range e.rt.execRuns()[before:] {
		if slices.Contains(call.argv, "setup-git") || slices.Contains(call.argv, "add") {
			t.Fatalf("read-only status mutated GitHub/native config: %v", call.argv)
		}
	}
	auth.Store(`{"hosts":{}}`)
	loggedOut, err := e.sched.GitHubOAuthStatus(t.Context(), e.member.ID, started.SessionID)
	if err != nil || loggedOut.State != "disconnected" || loggedOut.Connection != nil {
		t.Fatalf("logout retained cached connection: %+v, %v", loggedOut, err)
	}
}

func TestGitHubOAuthSessionIdentityCancellationAndRestart(t *testing.T) {
	t.Parallel()
	e, rt, _ := newGitHubOAuthTestEnv(t)
	first := startGitHubOAuthTest(t, e, e.member.ID)
	command := nextGitHubOAuthExec(t, rt)
	publishGitHubDeviceCode(t, command)
	duplicate := startGitHubOAuthTest(t, e, e.member.ID)
	if duplicate.SessionID != first.SessionID {
		t.Fatal("duplicate start replaced live attempt")
	}
	other := &domain.Member{DisplayName: "Other", PublicKey: testPublicKey(t), Role: domain.RoleAdmin}
	if err := e.db.CreateMember(t.Context(), other); err != nil {
		t.Fatal(err)
	}
	if _, err := e.sched.GitHubOAuthStatus(t.Context(), other.ID, first.SessionID); err == nil {
		t.Fatal("another member read attempt")
	}
	if _, err := e.sched.CancelGitHubOAuth(t.Context(), other.ID, first.SessionID); err == nil {
		t.Fatal("another member cancelled attempt")
	}
	if _, err := e.sched.CancelGitHubOAuth(t.Context(), e.member.ID, ""); err == nil {
		t.Fatal("cancel without exact session was accepted")
	}
	cancelled, err := e.sched.CancelGitHubOAuth(t.Context(), e.member.ID, first.SessionID)
	if err != nil || cancelled.State != "cancelled" || cancelled.UserCode != "" {
		t.Fatalf("cancel = %+v, %v", cancelled, err)
	}
	requireGitHubOAuthStopped(t, command)
	second := startGitHubOAuthTest(t, e, e.member.ID)
	secondCommand := nextGitHubOAuthExec(t, rt)
	if second.SessionID == first.SessionID {
		t.Fatal("new attempt reused identity")
	}
	if _, err := e.sched.GitHubOAuthStatus(t.Context(), e.member.ID, first.SessionID); err == nil {
		t.Fatal("stale status accepted")
	}
	if _, err := e.sched.CancelGitHubOAuth(t.Context(), e.member.ID, first.SessionID); err == nil {
		t.Fatal("stale cancel accepted")
	}
	state, _ := secondCommand.Status(t.Context())
	if !state.Running {
		t.Fatal("stale cancellation stopped replacement")
	}
	if _, err := e.sched.CancelGitHubOAuth(t.Context(), e.member.ID, second.SessionID); err != nil {
		t.Fatal(err)
	}
	requireGitHubOAuthStopped(t, secondCommand)
}

func TestGitHubOAuthPendingCommandsStopWithLifecycle(t *testing.T) {
	for _, reason := range []string{"terminal-stop", "terminal-exit", "scheduler-close"} {
		t.Run(reason, func(t *testing.T) {
			t.Parallel()
			e, rt, _ := newGitHubOAuthTestEnv(t)
			startGitHubOAuthTest(t, e, e.member.ID)
			command := nextGitHubOAuthExec(t, rt)
			publishGitHubDeviceCode(t, command)
			switch reason {
			case "terminal-stop":
				if err := e.sched.StopTerminal(t.Context(), e.member.ID); err != nil {
					t.Fatal(err)
				}
			case "terminal-exit":
				e.rt.mu.Lock()
				container := e.rt.containers[command.identity.ContainerID]
				e.rt.mu.Unlock()
				container.endProcess(0)
			case "scheduler-close":
				if err := e.sched.Close(); err != nil {
					t.Fatal(err)
				}
			}
			result := waitGitHubOAuthAttempt(t, e, e.member.ID)
			if result.State != "cancelled" {
				t.Fatalf("lifecycle result = %+v", result)
			}
			requireGitHubOAuthStopped(t, command)
			if reason == "scheduler-close" {
				if _, err := e.sched.StartGitHubOAuth(t.Context(), e.member.ID); err == nil {
					t.Fatal("started after Close")
				}
			}
		})
	}
}

func TestGitHubOAuthDeadlineStopsOwnedCommand(t *testing.T) {
	t.Parallel()
	e, rt, _ := newGitHubOAuthTestEnv(t)
	if _, err := e.sched.EnsureTerminal(t.Context(), e.member.ID); err != nil {
		t.Fatal(err)
	}
	// Shorten only this attempt's context, not a package-global timeout shared
	// with other tests. The production admission path always uses 15 minutes.
	ctx, cancel := context.WithTimeout(e.sched.superCtx, time.Second)
	a := &githubOAuthAttempt{ctx: ctx, cancel: cancel, done: make(chan struct{}), result: protocol.GitHubOAuthResult{State: "starting", SessionID: "deadline-attempt"}}
	e.sched.githubOAuthMu.Lock()
	e.sched.githubOAuth = map[domain.MemberID]*githubOAuthAttempt{e.member.ID: a}
	e.sched.githubOAuthWG.Add(1)
	e.sched.githubOAuthMu.Unlock()
	go e.sched.runGitHubOAuth(e.member.ID, a)
	command := nextGitHubOAuthExec(t, rt)
	publishGitHubDeviceCode(t, command)
	result := waitGitHubOAuthAttempt(t, e, e.member.ID)
	if result.State != "expired" || result.UserCode != "" {
		t.Fatalf("deadline result = %+v", result)
	}
	requireGitHubOAuthStopped(t, command)
}

func TestGitHubOAuthCancelsWhileNativeStartIsPending(t *testing.T) {
	t.Parallel()
	e, rt, _ := newGitHubOAuthTestEnv(t)
	rt.startGate = make(chan struct{})
	result := startGitHubOAuthTest(t, e, e.member.ID)
	cancelled, err := e.sched.CancelGitHubOAuth(t.Context(), e.member.ID, result.SessionID)
	if err != nil || cancelled.State != "cancelled" {
		t.Fatalf("cancel starting = %+v, %v", cancelled, err)
	}
	select {
	case <-rt.started:
		t.Fatal("cancelled start published a command")
	default:
	}
}

func TestGitHubOAuthStatusIsReadOnlyAndUsesCurrentAccount(t *testing.T) {
	t.Parallel()
	e, _, auth := newGitHubOAuthTestEnv(t)
	result, err := e.sched.GitHubOAuthStatus(t.Context(), e.member.ID, "")
	if err != nil || result.State != "disconnected" || e.sched.lookupTerminal(e.member.ID) != nil {
		t.Fatalf("empty visit = %+v, %v", result, err)
	}
	if len(e.rt.execRuns()) != 0 {
		t.Fatal("empty Settings visit executed commands")
	}
	if _, err = e.sched.EnsureTerminal(t.Context(), e.member.ID); err != nil {
		t.Fatal(err)
	}
	auth.Store(ghStatusLoggedIn)
	result, err = e.sched.GitHubOAuthStatus(t.Context(), e.member.ID, "")
	if err != nil || result.State != "connected" || result.Login != "octocat" || result.Connection != nil {
		t.Fatalf("native connection = %+v, %v", result, err)
	}
	if has, keyErr := e.cfg.Homes.HasSigningKey(e.member.ID); keyErr != nil || has {
		t.Fatalf("status generated key: %v, %v", has, keyErr)
	}
	auth.Store(strings.ReplaceAll(ghStatusLoggedIn, "octocat", "other-account"))
	result, err = e.sched.GitHubOAuthStatus(t.Context(), e.member.ID, "")
	if err != nil || result.Login != "other-account" {
		t.Fatalf("account switch = %+v, %v", result, err)
	}
	auth.Store(`{"hosts":{"github.com":[{"state":"error","active":true,"login":"other-account","error":"HTTP 401: Bad credentials"}]}}`)
	result, err = e.sched.GitHubOAuthStatus(t.Context(), e.member.ID, "")
	if err != nil || result.State != "disconnected" || !strings.Contains(result.Error, "Bad credentials") {
		t.Fatalf("revoked auth = %+v, %v", result, err)
	}
}

func TestGitHubOAuthStatusRecognizesManagedAccounts(t *testing.T) {
	t.Parallel()
	e, _, auth := newGitHubOAuthTestEnv(t)
	if _, err := e.sched.EnsureTerminal(t.Context(), e.member.ID); err != nil {
		t.Fatal(err)
	}
	for _, login := range []string{"mona-cat_octo", "octo_admin", strings.Repeat("a", 30) + "_12345678"} {
		auth.Store(strings.ReplaceAll(ghStatusLoggedIn, "octocat", login))
		result, err := e.sched.GitHubOAuthStatus(t.Context(), e.member.ID, "")
		if err != nil || result.State != "connected" || result.Login != login {
			t.Fatalf("managed native status = %+v, %v", result, err)
		}
	}
	for _, login := range []string{"mona_ab", "mona_123456789", "mona__octo", "mona_octo/other", "mona_octo?token=x", `mona_octo\n`, strings.Repeat("a", 31) + "_12345678"} {
		auth.Store(strings.ReplaceAll(ghStatusLoggedIn, "octocat", login))
		result, err := e.sched.GitHubOAuthStatus(t.Context(), e.member.ID, "")
		if err == nil && result.State == "connected" {
			t.Fatalf("malformed native login accepted: %+v", result)
		}
	}
}

func TestGitHubOAuthStartCompletesExistingLogin(t *testing.T) {
	t.Parallel()
	requireSSHKeygen(t)
	e, rt, auth := newGitHubOAuthTestEnv(t)
	auth.Store(ghStatusLoggedIn)
	startGitHubOAuthTest(t, e, e.member.ID)
	result := waitGitHubOAuthAttempt(t, e, e.member.ID)
	if result.State != "connected" || result.Connection == nil {
		t.Fatalf("existing login = %+v", result)
	}
	select {
	case <-rt.started:
		t.Fatal("already authorized account prompted for OAuth again")
	default:
	}
}

func TestGitHubOAuthMissingScopeReauthorizesThroughOfficialLogin(t *testing.T) {
	t.Parallel()
	requireSSHKeygen(t)
	e, rt, auth := newGitHubOAuthTestEnv(t)
	auth.Store(strings.ReplaceAll(ghStatusLoggedIn, ", admin:ssh_signing_key", ""))
	if _, err := e.sched.EnsureTerminal(t.Context(), e.member.ID); err != nil {
		t.Fatal(err)
	}
	status, err := e.sched.GitHubOAuthStatus(t.Context(), e.member.ID, "")
	if err != nil || status.State != "failed" || status.Connection != nil || !strings.Contains(status.Error, "connect GitHub again") {
		t.Fatalf("missing scope status = %+v, %v", status, err)
	}
	if has, keyErr := e.cfg.Homes.HasSigningKey(e.member.ID); keyErr != nil || has {
		t.Fatalf("status generated key: %v, %v", has, keyErr)
	}
	startGitHubOAuthTest(t, e, e.member.ID)
	command := nextGitHubOAuthExec(t, rt)
	publishGitHubDeviceCode(t, command)
	if has, err := e.cfg.Homes.HasSigningKey(e.member.ID); err != nil || has {
		t.Fatalf("key written before authorization: %v, %v", has, err)
	}
	auth.Store(ghStatusLoggedIn)
	command.finish(0)
	if result := waitGitHubOAuthAttempt(t, e, e.member.ID); result.State != "connected" {
		t.Fatalf("scope reconnect = %+v", result)
	}
}

func TestGitHubOAuthRefusesUnsafePreflight(t *testing.T) {
	for _, test := range []struct {
		name, version, status, environment, want string
		versionCode                              int
	}{
		{name: "missing gh", version: ghNotFound, versionCode: 127, want: "gh is not on PATH"},
		{name: "old gh", version: ghVersionUbuntu, want: "oldest gh"},
		{name: "malformed auth", version: ghVersionCurrent, status: `{"hosts":`, want: "malformed account"},
		{name: "malformed login", version: ghVersionCurrent, status: strings.ReplaceAll(ghStatusLoggedIn, "octocat", "ghp_secret_not_a_login"), want: "malformed account"},
		{name: "env token", version: ghVersionCurrent, environment: "GH_TOKEN=ghp_this_is_a_sensitive_environment_credential", want: "environment refused"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			e, rt, _ := newGitHubOAuthTestEnv(t)
			e.rt.execHandler = func(_ runtime.ID, argv []string) (int, string, error) {
				switch {
				case slices.Contains(argv, githubOAuthEnvironmentCheck):
					if test.environment != "" {
						return 1, test.environment, nil
					}
					return 0, "", nil
				case slices.Contains(argv, "--version"):
					return test.versionCode, test.version, nil
				case slices.Contains(argv, "status"):
					return 0, test.status, nil
				default:
					return 0, ghWhere("/usr/bin/gh"), nil
				}
			}
			startGitHubOAuthTest(t, e, e.member.ID)
			result := waitGitHubOAuthAttempt(t, e, e.member.ID)
			if result.State != "failed" || !strings.Contains(result.Error, test.want) || strings.Contains(result.Error, "ghp_") {
				t.Fatalf("preflight = %+v", result)
			}
			select {
			case <-rt.started:
				t.Fatal("unsafe preflight started authorization")
			default:
			}
		})
	}
}

func TestGitHubOAuthProviderFailuresAndBoundedOutput(t *testing.T) {
	for _, test := range []struct {
		name, output, state, want string
		exit                      int
	}{
		{name: "denied", output: "failed to authenticate via web browser: access_denied\n", exit: 1, state: "failed", want: "access_denied"},
		{name: "expired", output: "failed to authenticate via web browser: expired_token\n", exit: 1, state: "expired", want: "expired_token"},
		{name: "wrong verification host", output: "! First copy your one-time code: ABCD-1234\nOpen this URL to continue in your web browser: https://evil.example/login/device\n", state: "failed", want: "unexpected verification URL"},
		{name: "secret device code", output: "! First copy your one-time code: device_code_secret_not_a_user_code\n", state: "failed", want: "invalid user code"},
		{name: "success without device flow", output: "logged in\n", state: "failed", want: "without a valid device code"},
		{name: "oversized line", output: strings.Repeat("x", githubOAuthOutputLimit+100) + "\n", state: "failed", want: "read native authorization output"},
		{name: "too much output", output: strings.Repeat("warning\n", githubOAuthOutputLimit/4), state: "failed", want: "safety limit"},
		{name: "redacted native failure", output: "bad credential ghp_sensitive_value_not_for_output Authorization: Bearer github_pat_sensitive_value_not_for_output\n", exit: 1, state: "failed", want: "[redacted]"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			e, rt, _ := newGitHubOAuthTestEnv(t)
			startGitHubOAuthTest(t, e, e.member.ID)
			command := nextGitHubOAuthExec(t, rt)
			// A writer must unblock even after parsing stops at its size limit.
			_, _ = io.WriteString(command.stderrWriter, test.output)
			command.finish(test.exit)
			result := waitGitHubOAuthAttempt(t, e, e.member.ID)
			if result.State != test.state || !strings.Contains(result.Error, test.want) || strings.Contains(result.Error, "sensitive_value") || result.UserCode != "" {
				t.Fatalf("provider failure = %+v", result)
			}
			requireGitHubOAuthStopped(t, command)
		})
	}
}

func TestGitHubOAuthSafeErrorPreservesDiagnosticsAndRedactsCredentials(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, input, want string
	}{
		{
			name:  "native start reason",
			input: "github: start native authorization: managed exec creation refused",
			want:  "github: start native authorization: managed exec creation refused",
		},
		{
			name:  "native stop reason",
			input: "github: stop native authorization: runtime ownership unavailable",
			want:  "github: stop native authorization: runtime ownership unavailable",
		},
		{
			name:  "provider reason",
			input: "failed authorization: access_denied",
			want:  "failed authorization: access_denied",
		},
		{
			name:  "short bearer header",
			input: "Authorization: Bearer short-secret",
			want:  "Authorization: [redacted]",
		},
		{
			name:  "short basic header",
			input: "authorization: Basic dXNlcjpwYXNz",
			want:  "authorization: [redacted]",
		},
		{
			name:  "traced header",
			input: "> aUtHoRiZaTiOn:\tToKeN short-secret\nHTTP 401: Bad credentials",
			want:  "> aUtHoRiZaTiOn:\t[redacted]\nHTTP 401: Bad credentials",
		},
		{
			name:  "other HTTP scheme",
			input: "Authorization: Digest username=\"private\", response=\"short-secret\"\nrequest rejected",
			want:  "Authorization: [redacted]\nrequest rejected",
		},
		{
			name:  "quoted header field",
			input: `{"Authorization": "Bearer short-secret", "error": "Bad credentials"}`,
			want:  `{"Authorization": [redacted], "error": "Bad credentials"}`,
		},
		{
			name:  "inline bearer header",
			input: "HTTP 401: Authorization: Bearer short-secret; Bad credentials",
			want:  "HTTP 401: Authorization: Bearer [redacted]; Bad credentials",
		},
		{
			name:  "inline basic header",
			input: "HTTP 401: Authorization=Basic dXNlcjpwYXNz; Bad credentials",
			want:  "HTTP 401: Authorization=Basic [redacted]; Bad credentials",
		},
		{
			name:  "inline token header",
			input: "HTTP 401: authorization: token short-secret; Bad credentials",
			want:  "HTTP 401: authorization: token [redacted]; Bad credentials",
		},
		{
			name:  "token fields",
			input: "access_token=short; oauth_token: short, device_code=short; client_secret=short; GH_TOKEN=short; GITHUB_TOKEN=short",
			want:  "access_token=[redacted]; oauth_token: [redacted], device_code=[redacted]; client_secret=[redacted]; GH_TOKEN=[redacted]; GITHUB_TOKEN=[redacted]",
		},
		{
			name:  "quoted token fields",
			input: `{"access_token": "short secret", "error": "denied"} oauth_token='short secret'`,
			want:  `{"access_token": [redacted], "error": "denied"} oauth_token=[redacted]`,
		},
		{
			name:  "native token formats",
			input: "credentials ghp_short gho_short ghu_short ghs_short ghr_short github_pat_short refused",
			want:  "credentials [redacted] [redacted] [redacted] [redacted] [redacted] [redacted] refused",
		},
		{
			name:  "legacy opaque token",
			input: "credential " + strings.Repeat("a", 40) + " refused",
			want:  "credential [redacted] refused",
		},
		{
			name:  "unicode and controls",
			input: "\x00échec:\t認証 refusée\x1b\nretry\xff",
			want:  "échec:\t認証 refusée\nretry",
		},
		{
			name:  "controls inside credentials",
			input: "HTTP 401: Authori\x00zation: Bearer short-secret; ghp_\x1bshort",
			want:  "HTTP 401: Authorization: Bearer [redacted]; [redacted]",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := githubOAuthSafeError(test.input)
			if got != test.want {
				t.Errorf("safe error = %q, want %q", got, test.want)
			}
		})
	}
}

func TestGitHubOAuthSafeErrorBoundsUnicodeAndExpandedRedactions(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, input string
	}{
		{name: "truncated unicode", input: strings.Repeat("界", githubOAuthOutputLimit)},
		{name: "expanded fields", input: strings.Repeat("GH_TOKEN=x;", githubOAuthOutputLimit/11)},
		{name: "expanded fields and unicode", input: strings.Repeat("GH_TOKEN=x;界", githubOAuthOutputLimit/14)},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := githubOAuthSafeError(test.input)
			if len(got) > githubOAuthOutputLimit || !utf8.ValidString(got) {
				t.Errorf("safe error exceeded bounds or contained invalid UTF-8: bytes=%d valid=%v", len(got), utf8.ValidString(got))
			}
			if strings.Contains(got, "GH_TOKEN=x") {
				t.Error("bounded output retained a credential field value")
			}
		})
	}
}

func TestGitHubOAuthStopFailureDoesNotClaimCancellationOrRetry(t *testing.T) {
	t.Parallel()
	e, rt, _ := newGitHubOAuthTestEnv(t)
	first := startGitHubOAuthTest(t, e, e.member.ID)
	command := nextGitHubOAuthExec(t, rt)
	command.mu.Lock()
	command.stopErr = errors.New("native command ownership unavailable")
	command.mu.Unlock()
	result, err := e.sched.CancelGitHubOAuth(t.Context(), e.member.ID, first.SessionID)
	if err != nil || result.State != "failed" || !strings.Contains(result.Error, "ownership unavailable") {
		t.Fatalf("stop failure = %+v, %v", result, err)
	}
	if _, err := e.sched.StartGitHubOAuth(t.Context(), e.member.ID); err == nil {
		t.Fatal("unsafe new attempt after failed stop")
	}
	requireGitHubOAuthStopped(t, command)
	command.finish(143)
}

func TestGitHubOAuthMembersOwnIndependentCommands(t *testing.T) {
	t.Parallel()
	e, rt, _ := newGitHubOAuthTestEnv(t)
	other := &domain.Member{DisplayName: "Other", PublicKey: testPublicKey(t), Role: domain.RoleAdmin}
	if err := e.db.CreateMember(t.Context(), other); err != nil {
		t.Fatal(err)
	}
	first := startGitHubOAuthTest(t, e, e.member.ID)
	firstCommand := nextGitHubOAuthExec(t, rt)
	second := startGitHubOAuthTest(t, e, other.ID)
	secondCommand := nextGitHubOAuthExec(t, rt)
	if first.SessionID == second.SessionID || firstCommand.identity.ContainerID == secondCommand.identity.ContainerID {
		t.Fatal("members shared an OAuth attempt or credential Environment")
	}
	if _, err := e.sched.CancelGitHubOAuth(t.Context(), e.member.ID, first.SessionID); err != nil {
		t.Fatal(err)
	}
	state, _ := secondCommand.Status(t.Context())
	if !state.Running {
		t.Fatal("one member's cancellation stopped another's login")
	}
	if _, err := e.sched.CancelGitHubOAuth(t.Context(), other.ID, second.SessionID); err != nil {
		t.Fatal(err)
	}
	requireGitHubOAuthStopped(t, firstCommand)
	requireGitHubOAuthStopped(t, secondCommand)
}

func TestGitHubOAuthNativeSetupFailureIsNotConnected(t *testing.T) {
	t.Parallel()
	requireSSHKeygen(t)
	e, rt, auth := newGitHubOAuthTestEnv(t)
	native := e.rt.execHandler
	var failSetup atomic.Bool
	failSetup.Store(true)
	e.rt.execHandler = func(id runtime.ID, argv []string) (int, string, error) {
		if slices.Contains(argv, "setup-git") && failSetup.Load() {
			return 1, "cannot write native git credential helper: oauth_token=gho_setup_secret", nil
		}
		return native(id, argv)
	}
	startGitHubOAuthTest(t, e, e.member.ID)
	command := nextGitHubOAuthExec(t, rt)
	publishGitHubDeviceCode(t, command)
	auth.Store(ghStatusLoggedIn)
	command.finish(0)
	result := waitGitHubOAuthAttempt(t, e, e.member.ID)
	if result.State != "failed" || result.Connection != nil || !strings.Contains(result.Error, "cannot write native git credential helper") {
		t.Fatalf("failed native setup = %+v", result)
	}
	requireGitHubOAuthStopped(t, command)
	if strings.Contains(result.Error, "gho_setup_secret") {
		t.Fatalf("setup failure exposed credentials: %+v", result)
	}
	before := len(e.rt.execRuns())
	fresh, err := e.sched.GitHubOAuthStatus(t.Context(), e.member.ID, "")
	if err != nil || fresh.State != "failed" || fresh.Error != result.Error || fresh.Connection != nil || fresh.Login != "octocat" {
		t.Fatalf("fresh status lost setup failure = %+v, %v", fresh, err)
	}
	for _, run := range e.rt.execRuns()[before:] {
		if slices.Contains(run.argv, "setup-git") {
			t.Fatal("fresh status retried native setup")
		}
	}
	failSetup.Store(false)
	startGitHubOAuthTest(t, e, e.member.ID)
	recovered := waitGitHubOAuthAttempt(t, e, e.member.ID)
	if recovered.State != "connected" || recovered.Connection == nil || recovered.Error != "" {
		t.Fatalf("setup retry = %+v", recovered)
	}
	select {
	case <-rt.started:
		t.Fatal("setup retry prompted for OAuth despite saved login")
	default:
	}
	fresh, err = e.sched.GitHubOAuthStatus(t.Context(), e.member.ID, "")
	if err != nil || fresh.State != "connected" || fresh.Connection == nil {
		t.Fatalf("fresh status after setup retry = %+v, %v", fresh, err)
	}
}

func TestGitHubOAuthNativeStartFailureDoesNotHang(t *testing.T) {
	t.Parallel()
	e, rt, _ := newGitHubOAuthTestEnv(t)
	rt.startErr = errors.New("managed exec creation refused")
	startGitHubOAuthTest(t, e, e.member.ID)
	result := waitGitHubOAuthAttempt(t, e, e.member.ID)
	if result.State != "failed" || !strings.Contains(result.Error, "managed exec creation refused") {
		t.Fatalf("start failure = %+v", result)
	}
}

func TestGitHubOAuthNativeStartFailureRedactsCredentials(t *testing.T) {
	t.Parallel()
	e, rt, _ := newGitHubOAuthTestEnv(t)
	rt.startErr = errors.New("managed exec creation refused; Authorization: Bearer short-secret; access_token=another-secret")
	startGitHubOAuthTest(t, e, e.member.ID)
	result := waitGitHubOAuthAttempt(t, e, e.member.ID)
	if result.State != "failed" || !strings.Contains(result.Error, "managed exec creation refused") || !strings.Contains(result.Error, "[redacted]") {
		t.Fatalf("start failure = %+v", result)
	}
	if strings.Contains(result.Error, "short-secret") || strings.Contains(result.Error, "another-secret") {
		t.Fatal("native start failure exposed credentials")
	}
	select {
	case <-rt.started:
		t.Fatal("failed start published a command")
	default:
	}
}

func TestGitHubOAuthDrainsBothOutputStreams(t *testing.T) {
	t.Parallel()
	e, rt, _ := newGitHubOAuthTestEnv(t)
	started := startGitHubOAuthTest(t, e, e.member.ID)
	command := nextGitHubOAuthExec(t, rt)
	written := make(chan error, 2)
	go func() {
		_, err := io.WriteString(command.stdoutWriter, strings.Repeat("native diagnostic\n", 100))
		written <- err
	}()
	go func() {
		_, err := io.WriteString(command.stderrWriter, "! First copy your one-time code: ABCD-1234\nOpen this URL to continue in your web browser: https://github.com/login/device\n")
		written <- err
	}()
	for range 2 {
		select {
		case err := <-written:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("native command blocked on an undrained output stream")
		}
	}
	if _, err := e.sched.CancelGitHubOAuth(t.Context(), e.member.ID, started.SessionID); err != nil {
		t.Fatal(err)
	}
	requireGitHubOAuthStopped(t, command)
}

type githubOAuthFinishingRuntime struct {
	*githubOAuthTestRuntime
	finishing chan struct{}
}

func (r *githubOAuthFinishingRuntime) Exec(ctx context.Context, id runtime.ID, argv []string, home string) (int, string, string, error) {
	if slices.Contains(argv, "setup-git") {
		close(r.finishing)
		<-ctx.Done()
		return 0, "", "", ctx.Err()
	}
	return r.githubOAuthTestRuntime.Exec(ctx, id, argv, home)
}

func TestGitHubOAuthTerminalStopInterruptsConnectionCompletion(t *testing.T) {
	t.Parallel()
	requireSSHKeygen(t)
	e, rt, auth := newGitHubOAuthTestEnv(t)
	completion := &githubOAuthFinishingRuntime{githubOAuthTestRuntime: rt, finishing: make(chan struct{})}
	e.sched.cfg.Runtime = completion
	auth.Store(ghStatusLoggedIn)
	startGitHubOAuthTest(t, e, e.member.ID)
	select {
	case <-completion.finishing:
	case <-time.After(5 * time.Second):
		t.Fatal("connection completion did not begin")
	}
	// Stop must cancel the setup context before taking the terminal lock
	// already held by ConnectGitHub, rather than deadlocking behind it.
	stopped := make(chan error, 1)
	go func() { stopped <- e.sched.StopTerminal(t.Context(), e.member.ID) }()
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("terminal stop deadlocked behind OAuth completion")
	}
	if result := waitGitHubOAuthAttempt(t, e, e.member.ID); result.State != "cancelled" {
		t.Fatalf("completion after stop = %+v", result)
	}
}

func TestGitHubOAuthStartRepairsExitedTerminalCleanup(t *testing.T) {
	t.Parallel()
	e, rt, _ := newGitHubOAuthTestEnv(t)
	rt.failDestroy.Store(true)
	startGitHubOAuthTest(t, e, e.member.ID)
	oldCommand := nextGitHubOAuthExec(t, rt)
	publishGitHubDeviceCode(t, oldCommand)
	old := e.sched.lookupLiveTerminal(e.member.ID)
	container, err := e.rt.get(old.containerID)
	if err != nil {
		t.Fatal(err)
	}
	container.exitNow(0)
	if result := waitGitHubOAuthAttempt(t, e, e.member.ID); result.State != "cancelled" {
		t.Fatalf("exited Environment authorization = %+v", result)
	}
	requireGitHubOAuthStopped(t, oldCommand)
	lock := e.sched.terminalLock(e.member.ID)
	lock.Lock()
	e.sched.mu.Lock()
	retained := e.sched.terminals[e.member.ID] == old && old.cleanupPending
	e.sched.mu.Unlock()
	lock.Unlock()
	if !retained {
		t.Fatal("failed cleanup did not retain the exited supervision")
	}

	rt.failDestroy.Store(false)
	// OAuth start alone must repair the old cleanup, not cancel itself while
	// ensuring the replacement Environment.
	startGitHubOAuthTest(t, e, e.member.ID)
	command := nextGitHubOAuthExec(t, rt)
	publishGitHubDeviceCode(t, command)
	if command.identity.ContainerID == old.containerID {
		t.Fatal("OAuth reused the exited Environment")
	}
	status, err := e.sched.GitHubOAuthStatus(t.Context(), e.member.ID, "")
	if err != nil || status.State != "pending" {
		t.Fatalf("replacement authorization = %+v, %v", status, err)
	}
	if err := e.sched.StopTerminal(t.Context(), e.member.ID); err != nil {
		t.Fatal(err)
	}
	if result := waitGitHubOAuthAttempt(t, e, e.member.ID); result.State != "cancelled" {
		t.Fatalf("explicit replacement stop = %+v", result)
	}
	requireGitHubOAuthStopped(t, command)
}
