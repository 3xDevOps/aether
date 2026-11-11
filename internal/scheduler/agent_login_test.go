package scheduler

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"github.com/3xDevOps/Aether/internal/acphost"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/harness"
)

const (
	fixtureLogin   = `{"claudeAiOauth":{"accessToken":"fixture-access","refreshToken":"fixture-refresh","expiresAt":%d,"scopes":["user:inference"]}}`
	fixtureNoToken = `{"claudeAiOauth":{"accessToken":"","refreshToken":"","expiresAt":0,"scopes":["user:inference"]}}`
	noTokenProblem = "~/.claude/.credentials.json: Claude OAuth access token is missing or invalid"
	claudeSignIn   = "dashboard Agents page, or aether agent login claude"
	refused        = acpTurnFailedReason + `acphost: session/prompt: {"code":-32000,"message":"Authentication required"}; `
	refusedProblem = "the agent refused: Authentication required"
)

// writeClaudeLogin replaces the member's Claude Code login file, at a
// modification time later than any earlier one.
func writeClaudeLogin(t *testing.T, e *testEnv, body string) {
	t.Helper()
	home, err := e.sched.cfg.Homes.Path(e.member.ID)
	if err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(home, ".claude", ".credentials.json")
	if err = os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
		t.Fatal(err)
	}
	var written time.Time
	if info, statErr := os.Stat(name); statErr == nil {
		written = info.ModTime()
	}
	if err = os.WriteFile(name, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.Chtimes(name, time.Time{}, written.Add(time.Second)); err != nil && !written.IsZero() {
		t.Fatal(err)
	}
}

func signedIn() string {
	return fmt.Sprintf(fixtureLogin, time.Now().Add(time.Hour).UnixMilli())
}

func loginProblem(t *testing.T, e *testEnv) (string, bool) {
	t.Helper()
	profile, _ := harness.Lookup("claude")
	problem, blocks, err := e.sched.LoginProblem(e.member.ID, "claude", profile)
	if err != nil {
		t.Fatal(err)
	}
	return problem, blocks
}

// A launch on an account whose login file cannot sign the agent in is
// refused before anything is created, with the file's problem and the way
// back in; it starts once the member signs in again, or when a workspace
// variable signs the agent in instead.
func TestLaunchRefusesASignedOutLoginAndSaysHowToSignIn(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	launch := func() error {
		_, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "task", "claude", domain.LaunchTUI)
		return err
	}
	writeClaudeLogin(t, e, fixtureNoToken)
	err := launch()
	want := "scheduler: Ada's Claude Code login is signed out: " + noTokenProblem + ". Sign in, then launch again: " + claudeSignIn
	if !errors.Is(err, ErrSignedOut) || err.Error() != want {
		t.Fatalf("Launch = %v, want %q", err, want)
	}
	if runs, listErr := e.db.ListRunsByWorkspace(t.Context(), e.ws.ID); listErr != nil || len(runs) != 0 {
		t.Fatalf("runs after the refused launch = %v, %v, want none", runs, listErr)
	}

	e.ws.Environment.Variables["ANTHROPIC_API_KEY"] = "fixture-key"
	if err = e.db.UpdateWorkspace(t.Context(), e.ws); err != nil {
		t.Fatal(err)
	}
	if err = launch(); err != nil {
		t.Fatalf("Launch with an API key in the workspace = %v", err)
	}
	delete(e.ws.Environment.Variables, "ANTHROPIC_API_KEY")
	if err = e.db.UpdateWorkspace(t.Context(), e.ws); err != nil {
		t.Fatal(err)
	}
	if err = launch(); !errors.Is(err, ErrSignedOut) {
		t.Fatalf("Launch without the API key = %v, want the refusal again", err)
	}

	writeClaudeLogin(t, e, signedIn())
	if err = launch(); err != nil {
		t.Fatalf("Launch after signing in again = %v", err)
	}
}

func TestLoginProblemReadsTheLoginFile(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	if problem, blocks := loginProblem(t, e); problem != "" || blocks {
		t.Fatalf("no login file = %q, %v, want no problem: a missing login is login_found false", problem, blocks)
	}
	writeClaudeLogin(t, e, signedIn())
	if problem, blocks := loginProblem(t, e); problem != "" || blocks {
		t.Fatalf("signed in = %q, %v, want no problem", problem, blocks)
	}
	writeClaudeLogin(t, e, fixtureNoToken)
	if problem, blocks := loginProblem(t, e); problem != noTokenProblem || !blocks {
		t.Fatalf("signed out = %q, %v, want %q blocking a launch", problem, blocks, noTokenProblem)
	}
}

func newClaudeACPEnv(t *testing.T) (*testEnv, *acpRuntime, *domain.Run) {
	t.Helper()
	e, rt := newACPEnv(t, func(cfg *Config) {
		cfg.Harnesses["claude"] = HarnessSpec{TUIArgs: []string{"fake-agent", "{task}"}, ACPArgs: []string{"acp-mock"}}
	})
	rt.fixture.RefuseNext = new(atomic.Int32)
	writeClaudeLogin(t, e, signedIn())
	run, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "", "claude", domain.LaunchACP)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	waitFor(t, "session", func() bool { return e.sched.acp.session(run.ID) != nil })
	e.waitStoreStatus(t, run.ID, domain.RunRunning)
	return e, rt, run
}

func refuse(t *testing.T, e *testEnv, run domain.RunID) {
	t.Helper()
	if _, err := e.sched.Inject(t.Context(), run, e.member.ID, domain.AgentPrompt{Text: "say pong"}, false, nil); err == nil {
		t.Fatal("Inject of a refused prompt succeeded")
	}
}

func waitReason(t *testing.T, e *testEnv, run domain.RunID, want string) {
	t.Helper()
	var got string
	defer func() {
		if t.Failed() {
			t.Logf("last reason: %q", got)
		}
	}()
	waitFor(t, "the reason "+want, func() bool {
		row, err := e.db.GetRun(t.Context(), run)
		if err != nil {
			return false
		}
		got = row.Reason
		return row.Status == domain.RunNeedsAttention && row.Reason == want
	})
}

// The incident: the login file lost its token under a running session. The
// refused turn says whose login is signed out, with the file's problem and
// the way back in, and the account shows it.
func TestRefusedTurnOnASignedOutLoginSaysHowToSignIn(t *testing.T) {
	t.Parallel()
	e, rt, run := newClaudeACPEnv(t)
	writeClaudeLogin(t, e, fixtureNoToken)
	rt.fixture.RefuseNext.Store(1)
	refuse(t, e, run.ID)
	waitReason(t, e, run.ID, refused+"Ada's Claude Code login is signed out. Sign in, then send the message again: "+claudeSignIn)
	waitItems(t, e.sched, run.ID, "the sign-in notice with the file's problem", func(items []acphost.Item) bool {
		return slices.ContainsFunc(items, func(it acphost.Item) bool {
			return it.Kind == acphost.KindNotice && it.Notice.Title == "Agent signed out" &&
				strings.HasSuffix(it.Notice.Description, claudeSignIn+". The login file reads: "+noTokenProblem+".")
		})
	})
	if problem, blocks := loginProblem(t, e); problem != noTokenProblem || !blocks {
		t.Fatalf("LoginProblem = %q, %v, want the file's problem", problem, blocks)
	}
}

// A refusal the login file does not explain is remembered for the account,
// so other sessions and the launch form learn of it. One refusal warns; a
// turn that ends well clears it.
func TestRefusedTurnIsRememberedForTheAccountUntilATurnWorks(t *testing.T) {
	t.Parallel()
	e, rt, run := newClaudeACPEnv(t)
	rt.fixture.RefuseNext.Store(1)
	refuse(t, e, run.ID)
	waitReason(t, e, run.ID, refused+"Aether restarted the agent to reread Ada's login: send the message again. Refused again? Sign in: "+claudeSignIn)
	if problem, blocks := loginProblem(t, e); problem != refusedProblem || blocks {
		t.Fatalf("after one refusal = %q, %v, want %q without blocking a launch", problem, blocks, refusedProblem)
	}
	if _, err := e.sched.Inject(t.Context(), run.ID, e.member.ID, domain.AgentPrompt{Text: "say pong"}, false, nil); err != nil {
		t.Fatalf("message after the restart: %v", err)
	}
	waitItems(t, e.sched, run.ID, "the answered turn", assistantSaid("pong"))
	waitFor(t, "the refusal forgotten", func() bool {
		problem, _ := loginProblem(t, e)
		return problem == ""
	})
}

// A refusal never refuses a launch: the refusing agent may hold a stale copy
// of a sound login, which a new session's container does not share. The
// account shows it until the login file changes.
func TestRefusalsWarnWithoutBlockingLaunchesUntilTheMemberSignsInAgain(t *testing.T) {
	t.Parallel()
	e, rt, run := newClaudeACPEnv(t)
	before := len(rt.all())
	rt.fixture.RefuseNext.Store(1)
	refuse(t, e, run.ID)
	waitFor(t, "the restarted agent", func() bool { return len(rt.all()) == before+1 && e.sched.acp.session(run.ID) != nil })
	rt.fixture.RefuseNext.Store(1)
	refuse(t, e, run.ID)
	waitReason(t, e, run.ID, refused+"a restart did not help. Start a new session, or if Ada's Claude Code login is signed out, sign in: "+claudeSignIn)
	if problem, blocks := loginProblem(t, e); problem != refusedProblem || blocks {
		t.Fatalf("after two refusals = %q, %v, want %q without blocking a launch", problem, blocks, refusedProblem)
	}
	if _, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "", "claude", domain.LaunchACP); err != nil {
		t.Fatalf("Launch of a second session = %v", err)
	}

	writeClaudeLogin(t, e, signedIn())
	if problem, blocks := loginProblem(t, e); problem != "" || blocks {
		t.Fatalf("after signing in again = %q, %v, want no problem", problem, blocks)
	}
}

// A turn that a workspace variable signed in says nothing about the
// account's login, so it does not clear a refusal.
func TestATurnSignedInByTheWorkspaceKeepsTheRefusal(t *testing.T) {
	t.Parallel()
	e, rt, run := newClaudeACPEnv(t)
	before := len(rt.all())
	rt.fixture.RefuseNext.Store(1)
	refuse(t, e, run.ID)
	waitFor(t, "the restarted agent", func() bool { return len(rt.all()) == before+1 && e.sched.acp.session(run.ID) != nil })
	waitReason(t, e, run.ID, refused+"Aether restarted the agent to reread Ada's login: send the message again. Refused again? Sign in: "+claudeSignIn)
	e.ws.Environment.Variables["ANTHROPIC_API_KEY"] = "fixture-key"
	if err := e.db.UpdateWorkspace(t.Context(), e.ws); err != nil {
		t.Fatal(err)
	}
	if _, err := e.sched.Inject(t.Context(), run.ID, e.member.ID, domain.AgentPrompt{Text: "say pong"}, false, nil); err != nil {
		t.Fatalf("message after the restart: %v", err)
	}
	waitItems(t, e.sched, run.ID, "the answered turn", assistantSaid("pong"))
	waitFor(t, "the refusal no longer the run's reason", func() bool {
		row, err := e.db.GetRun(t.Context(), run.ID)
		return err == nil && !strings.HasPrefix(row.Reason, acpTurnFailedReason)
	})
	if problem, _ := loginProblem(t, e); problem != refusedProblem {
		t.Fatalf("LoginProblem after a turn the workspace signed in = %q, want %q", problem, refusedProblem)
	}
}

// What is remembered for the account is the agent's own message, never the
// error around it, which can carry the agent's stderr.
func TestRememberedRefusalKeepsOnlyTheAgentsMessage(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	cause := fmt.Errorf("open the agent conversation: %w; stderr: fixture stderr tail", acp.NewAuthRequired(map[string]string{"detail": "fixture data"}))
	e.sched.loginRefused(e.member.ID, "claude", "", cause)
	if problem, blocks := loginProblem(t, e); problem != refusedProblem || blocks {
		t.Fatalf("LoginProblem = %q, %v, want %q", problem, blocks, refusedProblem)
	}
}

// A Background session's refused turn fails the run with whose login it is
// and where to sign in.
func TestBackgroundRefusalSaysHowToSignIn(t *testing.T) {
	t.Parallel()
	e, rt := newBackgroundEnv(t, enhancedAgent, true)
	rt.fixture.RefusePrompts = true
	run := e.launchBackground(t, enhancedAgent, "say pong")
	got := e.waitStoreStatus(t, run.ID, domain.RunFailed)
	if want := refused + "Ada's OpenCode login may be signed out. Sign in: dashboard Agents page, or aether agent login opencode"; got.Reason != want {
		t.Fatalf("reason %q, want %q", got.Reason, want)
	}
	profile, _ := harness.Lookup(enhancedAgent)
	if problem, _, err := e.sched.LoginProblem(e.member.ID, enhancedAgent, profile); err != nil || problem != refusedProblem {
		t.Fatalf("LoginProblem = %q, %v, want %q", problem, err, refusedProblem)
	}
}

// A refusal after the login file changed restarts the agent again even
// within the window, so a message sent after signing in reaches an agent
// that has read the new login.
func TestRefusalAfterSigningInAgainRestartsTheAgent(t *testing.T) {
	t.Parallel()
	e, rt, run := newClaudeACPEnv(t)
	before := len(rt.all())
	rt.fixture.RefuseNext.Store(1)
	refuse(t, e, run.ID)
	waitFor(t, "the restarted agent", func() bool { return len(rt.all()) == before+1 && e.sched.acp.session(run.ID) != nil })
	waitReason(t, e, run.ID, refused+"Aether restarted the agent to reread Ada's login: send the message again. Refused again? Sign in: "+claudeSignIn)

	writeClaudeLogin(t, e, signedIn())
	rt.fixture.RefuseNext.Store(1)
	refuse(t, e, run.ID)
	waitFor(t, "the agent restarted for the new login", func() bool { return len(rt.all()) == before+2 && e.sched.acp.session(run.ID) != nil })

	// An agent that rewrites its login on every start earns no third restart.
	writeClaudeLogin(t, e, signedIn())
	rt.fixture.RefuseNext.Store(1)
	refuse(t, e, run.ID)
	waitReason(t, e, run.ID, refused+"a restart did not help. Start a new session, or if Ada's Claude Code login is signed out, sign in: "+claudeSignIn)
	if n := len(rt.all()); n != before+2 {
		t.Fatalf("agent processes = %d, want %d: no restart for a third refusal in the window", n, before+2)
	}
}

// An agent that refuses to open its conversation parks the new session with
// whose login is signed out and where to sign in, and the account shows it.
func TestRefusedSessionOpenSaysHowToSignIn(t *testing.T) {
	t.Parallel()
	e, rt := newACPEnv(t, func(cfg *Config) {
		cfg.Harnesses["claude"] = HarnessSpec{TUIArgs: []string{"fake-agent", "{task}"}, ACPArgs: []string{"acp-mock"}}
	})
	rt.fixture.RefuseSessions = true
	writeClaudeLogin(t, e, signedIn())
	run, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "", "claude", domain.LaunchACP)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	waitReason(t, e, run.ID, acpFailedReason+`open the agent conversation: acphost: session/new: {"code":-32000,"message":"Authentication required"}; `+
		"Ada's Claude Code login may be signed out. Sign in: "+claudeSignIn)
	if problem, blocks := loginProblem(t, e); problem != refusedProblem || blocks {
		t.Fatalf("LoginProblem = %q, %v, want %q", problem, blocks, refusedProblem)
	}
}
