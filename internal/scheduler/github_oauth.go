package scheduler

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/github"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/runtime"
)

const (
	githubOAuthLifetime    = 15 * time.Minute
	githubOAuthStopTimeout = 30 * time.Second
	githubOAuthOutputLimit = 16 << 10
	githubDeviceURL        = "https://github.com/login/device"
)

// The shell gives gh EOF without closing the managed attachment (which would
// also discard its output). The native timeout survives a server crash, and the
// container-local lock refuses a competing login until that command has ended.
// gh remains the OAuth client and the sole writer of its native credential.
const githubOAuthCommand = `command -v flock >/dev/null || { printf '%s\n' 'github: the Environment needs flock (util-linux) for safe authorization' >&2; exit 127; }
command -v timeout >/dev/null || { printf '%s\n' 'github: the Environment needs timeout (coreutils) for bounded authorization' >&2; exit 127; }
exec 9>/tmp/aether-github-oauth.lock
flock -n 9 || exit 75
exec timeout -s TERM -k 5 900 gh auth login --hostname github.com --git-protocol https --web --scopes admin:ssh_signing_key </dev/null`

// Refuse environment credentials rather than authenticating an account whose
// token is not the member's persisted gh login. GH_HOST also governs ssh-key
// add/list in the existing connection completion path.
const githubOAuthEnvironmentCheck = `if [ -n "${GH_TOKEN:-}" ] || [ -n "${GITHUB_TOKEN:-}" ]; then
  printf '%s\n' 'github: remove GH_TOKEN and GITHUB_TOKEN from the Environment before connecting; OAuth must use your saved gh login' >&2
  exit 1
fi
if [ -n "${GH_HOST:-}" ] && [ "$GH_HOST" != github.com ]; then
  printf '%s\n' 'github: remove the non-github.com GH_HOST from the Environment before connecting' >&2
  exit 1
fi
if [ -n "${GH_CONFIG_DIR:-}" ] || { [ -n "${XDG_CONFIG_HOME:-}" ] && [ "$XDG_CONFIG_HOME" != "$HOME/.config" ]; }; then
  printf '%s\n' 'github: remove custom GH_CONFIG_DIR or XDG_CONFIG_HOME from the Environment; OAuth must use your member home' >&2
  exit 1
fi`

var (
	githubUserCodePattern    = regexp.MustCompile(`^! First copy your one-time code: ([A-Z0-9]{4}-[A-Z0-9]{4})$`)
	githubSecretPattern      = regexp.MustCompile(`(?i)(?:gh[pousr]_[a-z0-9_]+|github_pat_[a-z0-9_]+|[a-z0-9_+/=-]{32,})`)
	githubSecretFieldPattern = regexp.MustCompile(`(?i)(\b(?:access_token|oauth_token|device_code|client_secret|gh_token|github_token)["']?\s*[:=]\s*)(?:"[^"\r\n]*"?|'[^'\r\n]*'?|[^\s,;]+)`)
	// Authorization is a header/quoted field, not the word in diagnostic prose
	// such as "start native authorization: managed exec creation refused".
	githubAuthorizationFieldPattern  = regexp.MustCompile(`(?im)((?:^[ \t]*(?:[<>][ \t]*)?|["'])authorization["']?[ \t]*[:=][ \t]*)(?:"[^"\r\n]*"?|'[^'\r\n]*'?|[^\r\n]+)`)
	githubAuthorizationSchemePattern = regexp.MustCompile(`(?i)(\bauthorization[ \t]*[:=][ \t]*(?:basic|bearer|token)[ \t]+)[^\s,;"']+`)
)

type githubOAuthAttempt struct {
	ctx           context.Context
	cancel        context.CancelFunc
	done          chan struct{}
	result        protocol.GitHubOAuthResult
	finished      bool
	terminal      *terminalSupervision
	exec          runtime.ManagedExec
	stopOnce      sync.Once
	stopErr       error
	cancelReason  string
	outputErr     error
	outputBytes   int
	log           strings.Builder
	code          string
	deviceURLSeen bool
}

// StartGitHubOAuth reserves one member-owned attempt before doing any slow
// work. The caller may disconnect while the official gh device flow continues.
func (s *Scheduler) StartGitHubOAuth(ctx context.Context, member domain.MemberID) (protocol.GitHubOAuthResult, error) {
	if err := ctx.Err(); err != nil {
		return protocol.GitHubOAuthResult{}, err
	}
	s.githubOAuthMu.Lock()
	defer s.githubOAuthMu.Unlock()
	if s.githubOAuthClosed || s.superCtx.Err() != nil {
		return protocol.GitHubOAuthResult{}, errors.New("github: scheduler is closing")
	}
	if previous := s.githubOAuth[member]; previous != nil {
		if !previous.finished {
			return previous.result, nil
		}
		if previous.stopErr != nil && previous.terminal == s.lookupLiveTerminal(member) {
			return protocol.GitHubOAuthResult{}, errors.New("github: previous login could not be stopped; stop and reopen the Environment before reconnecting")
		}
	}
	attemptCtx, cancel := context.WithTimeout(s.superCtx, githubOAuthLifetime)
	a := &githubOAuthAttempt{
		ctx: attemptCtx, cancel: cancel, done: make(chan struct{}),
		result: protocol.GitHubOAuthResult{
			State: "starting", SessionID: rand.Text(),
			ExpiresAt: time.Now().UTC().Add(githubOAuthLifetime).Format(time.RFC3339),
		},
	}
	if s.githubOAuth == nil {
		s.githubOAuth = make(map[domain.MemberID]*githubOAuthAttempt)
	}
	s.githubOAuth[member] = a
	s.githubOAuthWG.Add(1)
	go s.runGitHubOAuth(member, a)
	return a.result, nil
}

// GitHubOAuthStatus never creates an Environment simply to render Settings.
// A fresh visit, or a completed successful attempt, checks native authentication
// again instead of trusting cached success after logout, revocation, account
// switching or server restart. It never changes Git config or remote keys.
func (s *Scheduler) GitHubOAuthStatus(ctx context.Context, member domain.MemberID, sessionID string) (protocol.GitHubOAuthResult, error) {
	s.githubOAuthMu.Lock()
	a := s.githubOAuth[member]
	if sessionID != "" && (a == nil || a.result.SessionID != sessionID) {
		s.githubOAuthMu.Unlock()
		return protocol.GitHubOAuthResult{}, errors.New("github: OAuth session is not the current attempt for this member")
	}
	if a != nil && (!a.finished || (sessionID != "" && a.result.State != "connected")) {
		result := a.result
		s.githubOAuthMu.Unlock()
		return result, nil
	}
	s.githubOAuthMu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, githubProbeTimeout)
	defer cancel()
	lock := s.terminalLock(member)
	lock.Lock()
	defer lock.Unlock()
	if err := ctx.Err(); err != nil {
		return protocol.GitHubOAuthResult{}, err
	}
	s.githubOAuthMu.Lock()
	a = s.githubOAuth[member]
	if sessionID != "" && (a == nil || a.result.SessionID != sessionID) {
		s.githubOAuthMu.Unlock()
		return protocol.GitHubOAuthResult{}, errors.New("github: OAuth session is not the current attempt for this member")
	}
	if a != nil && !a.finished {
		result := a.result
		s.githubOAuthMu.Unlock()
		return result, nil
	}
	s.githubOAuthMu.Unlock()
	result := protocol.GitHubOAuthResult{State: "disconnected", SessionID: sessionID}
	sup := s.lookupLiveTerminal(member)
	if sup == nil {
		return result, nil
	}
	entry, loggedIn, err := s.inspectGitHubOAuthLogin(ctx, member, sup)
	if err != nil {
		result.State, result.Error = "failed", githubOAuthSafeError(err.Error())
		return result, nil
	}
	if !loggedIn {
		result.Error = githubOAuthSafeError(entry.Error)
		return result, nil
	}
	if !githubSigningScope(entry) {
		result.State, result.Login, result.Error = "failed", entry.Login, "github: the saved login lacks admin:ssh_signing_key permission; connect GitHub again to authorize setup"
		return result, nil
	}
	result.State, result.Login = "connected", entry.Login
	s.githubOAuthMu.Lock()
	if a != nil && a.finished && a.result.Login == entry.Login && a.terminal == sup {
		if a.result.State != "connected" {
			result = a.result
		} else {
			result.Connection = a.result.Connection
		}
	}
	s.githubOAuthMu.Unlock()
	return result, nil
}

func (s *Scheduler) CancelGitHubOAuth(ctx context.Context, member domain.MemberID, sessionID string) (protocol.GitHubOAuthResult, error) {
	s.githubOAuthMu.Lock()
	a := s.githubOAuth[member]
	if sessionID == "" || a == nil || a.result.SessionID != sessionID {
		s.githubOAuthMu.Unlock()
		return protocol.GitHubOAuthResult{}, errors.New("github: OAuth session is not the current attempt for this member")
	}
	if !a.finished {
		a.cancelReason = "github: authorization cancelled"
		a.cancel()
	}
	s.githubOAuthMu.Unlock()
	select {
	case <-a.done:
		s.githubOAuthMu.Lock()
		defer s.githubOAuthMu.Unlock()
		return a.result, nil
	case <-ctx.Done():
		return protocol.GitHubOAuthResult{}, ctx.Err()
	}
}

func (s *Scheduler) runGitHubOAuth(member domain.MemberID, a *githubOAuthAttempt) {
	defer s.githubOAuthWG.Done()
	defer a.cancel()
	conn, err := s.authorizeGitHubOAuth(member, a)
	stopErr := s.stopGitHubOAuthExec(a)
	s.githubOAuthMu.Lock()
	defer s.githubOAuthMu.Unlock()
	switch {
	case stopErr != nil:
		a.result.State, a.result.Error = "failed", githubOAuthSafeError(stopErr.Error())
	case a.outputErr != nil:
		a.result.State, a.result.Error = "failed", githubOAuthSafeError(a.outputErr.Error())
	case a.cancelReason != "":
		a.result.State, a.result.Error = "cancelled", a.cancelReason
	case errors.Is(a.ctx.Err(), context.DeadlineExceeded):
		a.result.State, a.result.Error = "expired", "github: authorization expired after 15 minutes; connect again"
	case a.ctx.Err() != nil:
		a.result.State, a.result.Error = "cancelled", "github: authorization stopped with the scheduler"
	case err != nil:
		a.result.State, a.result.Error = "failed", githubOAuthSafeError(err.Error())
		if strings.Contains(a.result.Error, "expired_token") || strings.Contains(a.result.Error, "code has expired") {
			a.result.State = "expired"
		}
	default:
		a.result = githubOAuthConnected(a.result, conn)
	}
	a.result.UserCode, a.result.VerificationURL = "", ""
	a.finished = true
	close(a.done)
}

func (s *Scheduler) authorizeGitHubOAuth(member domain.MemberID, a *githubOAuthAttempt) (domain.GitHubConnection, error) {
	if err := s.startGitHubOAuthExec(member, a); err != nil {
		return domain.GitHubConnection{}, err
	}
	if a.exec != nil {
		if err := s.waitGitHubOAuth(a); err != nil {
			return domain.GitHubConnection{}, err
		}
	}
	// No terminal lock is held while waiting for the member's approval. Only
	// this short completion owns it, and its context is cancelled by Stop.
	lock := s.terminalLock(member)
	lock.Lock()
	defer lock.Unlock()
	if err := a.ctx.Err(); err != nil {
		return domain.GitHubConnection{}, err
	}
	if s.lookupLiveTerminal(member) != a.terminal {
		return domain.GitHubConnection{}, ErrTerminalNotRunning
	}
	// Remember the account whose setup is being completed, even if setup
	// fails after gh has already saved its login. Fresh status must not turn
	// that incomplete connection into success just because the token exists.
	entry, loggedIn, err := s.inspectGitHubOAuthLogin(a.ctx, member, a.terminal)
	if err != nil {
		return domain.GitHubConnection{}, err
	}
	if !loggedIn {
		return domain.GitHubConnection{}, ErrGitHubNotLoggedIn
	}
	s.githubOAuthMu.Lock()
	a.result.State = "finishing"
	a.result.Login = entry.Login
	a.result.UserCode, a.result.VerificationURL = "", ""
	s.githubOAuthMu.Unlock()
	conn, err := s.connectGitHubLocked(a.ctx, member)
	if err == nil && !github.ValidLogin(conn.Login) {
		return domain.GitHubConnection{}, errors.New("github: native authorization returned a malformed account login")
	}
	return conn, err
}

func (s *Scheduler) startGitHubOAuthExec(member domain.MemberID, a *githubOAuthAttempt) error {
	lock := s.terminalLock(member)
	lock.Lock()
	defer lock.Unlock()
	if err := a.ctx.Err(); err != nil {
		return err
	}
	// Same admission/adoption path as EnsureTerminal, with the lock retained
	// through command publication so Stop cannot miss a just-started login.
	if _, err := s.ensureTerminalAdmittedLocked(a.ctx, member); err != nil {
		return err
	}
	sup := s.lookupLiveTerminal(member)
	if sup == nil {
		return ErrTerminalNotRunning
	}
	s.githubOAuthMu.Lock()
	a.terminal = sup
	s.githubOAuthMu.Unlock()
	probeCtx, cancel := context.WithTimeout(a.ctx, githubProbeTimeout)
	defer cancel()
	entry, loggedIn, err := s.inspectGitHubOAuthLogin(probeCtx, member, sup)
	if err != nil {
		return err
	}
	if loggedIn && githubSigningScope(entry) {
		return nil
	}
	managed, ok := s.cfg.Runtime.(runtime.ManagedExecRuntime)
	if !ok {
		return errors.New("github: this runtime cannot own a noninteractive OAuth command")
	}
	if ctxErr := a.ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	command, err := managed.StartExecPipe(a.ctx, sup.containerID, runtime.ExecSpec{
		Argv: []string{"/bin/sh", "-c", githubOAuthCommand}, WorkingDir: sup.home,
		CreationKey: "github-oauth-" + a.result.SessionID,
		Env:         []string{"GH_PROMPT_DISABLED=1", "NO_COLOR=1", "GH_FORCE_TTY=", "GH_DEBUG=", "GH_HOST=github.com"},
	})
	if err != nil {
		return fmt.Errorf("github: start native authorization: %w", err)
	}
	s.githubOAuthMu.Lock()
	a.exec = command
	s.githubOAuthMu.Unlock()
	return nil
}

func (s *Scheduler) inspectGitHubOAuthLogin(ctx context.Context, member domain.MemberID, sup *terminalSupervision) (ghAuthEntry, bool, error) {
	code, stdout, stderr, err := s.cfg.Runtime.Exec(ctx, sup.containerID, []string{"/bin/sh", "-c", githubOAuthEnvironmentCheck}, sup.home)
	if err != nil {
		return ghAuthEntry{}, false, fmt.Errorf("github: inspect native authentication environment: %w", err)
	}
	if code != 0 {
		return ghAuthEntry{}, false, fmt.Errorf("github: native authentication environment refused (exit %d): %s", code, joinOutput(stdout, stderr))
	}
	cli, err := s.probeGitHubCLI(ctx, sup.containerID, sup.home)
	if err != nil {
		return ghAuthEntry{}, false, err
	}
	if cli.Status != domain.GitHubCLIOK {
		m, memberErr := s.cfg.Store.GetMember(ctx, member)
		if memberErr != nil {
			return ghAuthEntry{}, false, memberErr
		}
		return ghAuthEntry{}, false, s.githubCLIError(ctx, m, sup, cli)
	}
	code, stdout, stderr, err = s.cfg.Runtime.Exec(ctx, sup.containerID, []string{"gh", "auth", "status", "--hostname", "github.com", "--json", "hosts"}, sup.home)
	if err != nil {
		return ghAuthEntry{}, false, fmt.Errorf("github: check native login: %w", err)
	}
	if code != 0 {
		return ghAuthEntry{}, false, fmt.Errorf("github: gh auth status exited %d: %s", code, joinOutput(stdout, stderr))
	}
	var status ghAuthStatus
	if err := json.Unmarshal([]byte(stdout), &status); err != nil || status.Hosts == nil {
		return ghAuthEntry{}, false, errors.New("github: gh auth status returned malformed account data")
	}
	entry, loggedIn := activeGitHubAccount(status)
	if loggedIn && !github.ValidLogin(entry.Login) {
		return ghAuthEntry{}, false, errors.New("github: gh auth status returned a malformed account login")
	}
	return entry, loggedIn, nil
}

func githubSigningScope(entry ghAuthEntry) bool {
	return slices.ContainsFunc(strings.Split(entry.Scopes, ","), func(scope string) bool {
		return strings.TrimSpace(scope) == signingKeyScope
	})
}

func (s *Scheduler) waitGitHubOAuth(a *githubOAuthAttempt) error {
	attachment := a.exec.Attachment()
	if attachment == nil {
		return errors.New("github: native authorization has no output attachment")
	}
	var readers sync.WaitGroup
	for _, stream := range []io.Reader{attachment.Stdout(), attachment.Stderr()} {
		if stream == nil {
			continue
		}
		readers.Add(1)
		go func(reader io.Reader) {
			defer readers.Done()
			scan := bufio.NewScanner(reader)
			scan.Buffer(make([]byte, 1024), githubOAuthOutputLimit)
			for scan.Scan() {
				s.recordGitHubOAuthOutput(a, scan.Text())
			}
			if err := scan.Err(); err != nil {
				s.githubOAuthMu.Lock()
				if a.ctx.Err() == nil && a.outputErr == nil {
					a.outputErr = fmt.Errorf("github: read native authorization output: %w", err)
					a.cancel()
				}
				s.githubOAuthMu.Unlock()
				// Keep consuming until Stop/Detach closes the streams, even if
				// malformed output exceeded the scanner's bounded line size.
				_, _ = io.Copy(io.Discard, reader)
			}
		}(stream)
	}
	drained := make(chan struct{})
	go func() {
		readers.Wait()
		close(drained)
	}()
	exit, err := a.exec.Wait(a.ctx)
	if err == nil && a.ctx.Err() == nil {
		// Do not drop buffered final diagnostics. A broken transport that
		// never sends EOF must still obey cancellation and the deadline.
		select {
		case <-drained:
		case <-a.ctx.Done():
			err = a.ctx.Err()
		}
	}
	_ = s.stopGitHubOAuthExec(a)
	<-drained
	if err != nil {
		return fmt.Errorf("github: wait for native authorization: %w", err)
	}
	s.githubOAuthMu.Lock()
	defer s.githubOAuthMu.Unlock()
	if exit.Code != 0 {
		if exit.Code == 75 {
			return errors.New("github: a native authorization is already running in this Environment; wait for it to expire or stop and reopen the Environment")
		}
		return fmt.Errorf("github: gh auth login exited %d: %s", exit.Code, strings.TrimSpace(a.log.String()))
	}
	if a.code == "" || !a.deviceURLSeen {
		return errors.New("github: native authorization ended without a valid device code and verification URL")
	}
	return nil
}

func (s *Scheduler) recordGitHubOAuthOutput(a *githubOAuthAttempt, line string) {
	s.githubOAuthMu.Lock()
	defer s.githubOAuthMu.Unlock()
	if a.ctx.Err() != nil {
		return
	}
	a.outputBytes += len(line) + 1
	if a.outputBytes > githubOAuthOutputLimit {
		a.outputErr = errors.New("github: native authorization output exceeded its safety limit")
		a.cancel()
		return
	}
	line = strings.TrimSpace(line)
	if code := githubUserCodePattern.FindStringSubmatch(line); code != nil {
		if a.code != "" && a.code != code[1] {
			a.outputErr = errors.New("github: native authorization returned conflicting user codes")
			a.cancel()
			return
		}
		a.code = code[1]
	} else if strings.Contains(line, "one-time code") {
		a.outputErr = errors.New("github: native authorization returned an invalid user code")
		a.cancel()
		return
	} else if strings.HasPrefix(line, "Open this URL to continue in your web browser:") {
		if line != "Open this URL to continue in your web browser: "+githubDeviceURL {
			a.outputErr = errors.New("github: native authorization returned an unexpected verification URL")
			a.cancel()
			return
		}
		a.deviceURLSeen = true
	} else if line != "" {
		a.log.WriteString(githubOAuthSafeError(line))
		a.log.WriteByte('\n')
	}
	if a.code != "" && a.deviceURLSeen {
		a.result.State, a.result.UserCode, a.result.VerificationURL = "pending", a.code, githubDeviceURL
	}
}

func githubOAuthSafeError(text string) string {
	if len(text) > githubOAuthOutputLimit {
		text = text[:githubOAuthOutputLimit]
	}
	// Normalize before matching so removing controls cannot join fragments
	// into a credential after redaction has already run.
	text = strings.Map(func(r rune) rune {
		if r < 32 && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, strings.ToValidUTF8(text, ""))
	text = githubSecretFieldPattern.ReplaceAllString(text, "${1}[redacted]")
	text = githubAuthorizationFieldPattern.ReplaceAllString(text, "${1}[redacted]")
	text = githubAuthorizationSchemePattern.ReplaceAllString(text, "${1}[redacted]")
	text = githubSecretPattern.ReplaceAllString(text, "[redacted]")
	// Replacements can grow short field values beyond the input bound.
	if len(text) > githubOAuthOutputLimit {
		text = strings.ToValidUTF8(text[:githubOAuthOutputLimit], "")
	}
	return strings.TrimSpace(text)
}

func githubOAuthConnected(result protocol.GitHubOAuthResult, conn domain.GitHubConnection) protocol.GitHubOAuthResult {
	result.State, result.Login, result.Error = "connected", conn.Login, ""
	result.UserCode, result.VerificationURL = "", ""
	result.Connection = &protocol.GitHubConnectResult{Login: conn.Login, SigningKey: conn.SigningKey, Fingerprint: conn.Fingerprint}
	return result
}

// Stop is explicit: cancelling Wait or detaching alone leaves a managed
// command alive. No terminal lock or OAuth mutex is needed by this cleanup.
func (s *Scheduler) stopGitHubOAuthExec(a *githubOAuthAttempt) error {
	s.githubOAuthMu.Lock()
	command := a.exec
	s.githubOAuthMu.Unlock()
	if command == nil {
		return nil
	}
	a.stopOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), githubOAuthStopTimeout)
		defer cancel()
		_, err := command.Stop(ctx, 3*time.Second)
		if errors.Is(err, runtime.ErrNotFound) {
			err = nil
		}
		err = errors.Join(err, command.Detach())
		s.githubOAuthMu.Lock()
		if err != nil {
			a.stopErr = fmt.Errorf("github: stop native authorization: %w", err)
		}
		s.githubOAuthMu.Unlock()
	})
	s.githubOAuthMu.Lock()
	defer s.githubOAuthMu.Unlock()
	return a.stopErr
}

// Safe before or under terminalLock. Never wait for a.done here: completion
// may be waiting to acquire that same terminal lock after the human approval.
func (s *Scheduler) stopTerminalGitHubOAuth(member domain.MemberID) {
	s.stopSupervisionGitHubOAuth(member, nil)
}

// A nil supervision means an explicit member-wide stop. Exited-supervision
// cleanup must not cancel a new attempt that is repairing that old Environment.
func (s *Scheduler) stopSupervisionGitHubOAuth(member domain.MemberID, sup *terminalSupervision) {
	s.githubOAuthMu.Lock()
	a := s.githubOAuth[member]
	if a != nil && sup != nil && a.terminal != sup {
		s.githubOAuthMu.Unlock()
		return
	}
	if a != nil && !a.finished {
		a.cancelReason = "github: authorization cancelled because the Environment stopped"
		a.cancel()
	}
	s.githubOAuthMu.Unlock()
	if a != nil {
		_ = s.stopGitHubOAuthExec(a)
	}
}

func (s *Scheduler) closeGitHubOAuth() {
	s.githubOAuthMu.Lock()
	s.githubOAuthClosed = true
	for _, a := range s.githubOAuth {
		if !a.finished {
			a.cancel()
		}
	}
	s.githubOAuthMu.Unlock()
	s.githubOAuthWG.Wait()
}
