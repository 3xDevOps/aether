package scheduler

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/harness"
	"github.com/3xDevOps/Aether/internal/quota"
)

// ErrSignedOut marks a launch refused because the account's login for the
// agent cannot sign it in.
var ErrSignedOut = errors.New("login is signed out")

type loginKey struct {
	account domain.MemberID
	agent   string
}

// loginRefusal is an agent's refusal of a turn for want of a sign-in. stamp
// is the account's login files then: the refusal stands until they change or
// a turn on the account ends well. A refusal in another run on the same
// files confirms it: one run's container can hold a stale copy of a sound
// login, two do not.
type loginRefusal struct {
	problem   string
	stamp     string
	run       domain.RunID
	confirmed bool
}

// LoginProblem is why the login of agent, which profile describes, in
// account's home cannot sign the agent in: what its file holds, or the agent's own refusal of a turn. It is
// empty when the server knows of none, which a missing login is not. blocks
// is whether a launch is refused over it.
func (s *Scheduler) LoginProblem(account domain.MemberID, agent string, profile harness.Profile) (problem string, blocks bool, err error) {
	problem, err = s.loginFileProblem(account, agent, profile)
	if err != nil || problem != "" {
		return problem, problem != "", err
	}
	paths, _ := profile.LoginPaths()
	stamp, err := s.loginStamp(account, paths)
	if err != nil {
		return "", false, err
	}
	key := loginKey{account, agent}
	s.mu.Lock()
	defer s.mu.Unlock()
	refusal, refused := s.loginRefusals[key]
	if !refused {
		return "", false, nil
	}
	if refusal.stamp != stamp {
		delete(s.loginRefusals, key)
		return "", false, nil
	}
	return refusal.problem, refusal.confirmed, nil
}

// loginFileProblem is LoginProblem from the login file alone.
func (s *Scheduler) loginFileProblem(account domain.MemberID, agent string, profile harness.Profile) (string, error) {
	file := quota.LoginFile(agent)
	paths, err := profile.LoginPaths()
	if s.cfg.Homes == nil || err != nil || !slices.Contains(paths, file) {
		return "", nil
	}
	data, err := s.cfg.Homes.ReadCredential(account, file, maxSignInBytes)
	if err != nil {
		return "", err
	}
	if problem := quota.LoginProblem(agent, data, time.Now()); problem != "" {
		return "~/" + file + ": " + problem, nil
	}
	return "", nil
}

func (s *Scheduler) loginStamp(account domain.MemberID, paths []string) (string, error) {
	if s.cfg.Homes == nil || len(paths) == 0 {
		return "", nil
	}
	return s.cfg.Homes.LoginStamp(account, paths)
}

// loginRefused records that agent refused a turn of run on account for want
// of a sign-in, while the account's login files read stamp.
func (s *Scheduler) loginRefused(run domain.RunID, account domain.MemberID, agent, stamp string, cause error) {
	key := loginKey{account, agent}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loginRefusals == nil {
		s.loginRefusals = make(map[loginKey]loginRefusal)
	}
	earlier, again := s.loginRefusals[key]
	again = again && earlier.stamp == stamp
	s.loginRefusals[key] = loginRefusal{
		problem:   "the agent refused: " + cause.Error(),
		stamp:     stamp,
		run:       run,
		confirmed: again && (earlier.confirmed || earlier.run != run),
	}
}

// signInSteps is where the owner of an account signs agent in again. Only a
// shipped agent has a login command.
func signInSteps(agent string, profile harness.Profile) string {
	if profile.LoginCommand == "" {
		return "aether terminal, then " + agent + "'s own sign-in"
	}
	return "dashboard Agents page, or aether agent login " + agent
}

// environmentSignsIn reports whether a variable the workspace sets signs
// agent in without the account's login: Claude Code's in every mode, Codex's
// CODEX_API_KEY in Background runs only (docs/harnesses.md).
func (s *Scheduler) environmentSignsIn(ctx context.Context, workspace domain.WorkspaceID, agent string, profile harness.Profile, mode domain.LaunchMode) (bool, error) {
	var keys []string
	switch {
	case agent == "claude":
		keys = []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN", "CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX"}
	case agent == "codex" && mode == domain.LaunchHeadless:
		keys = []string{"CODEX_API_KEY"}
	default:
		return false, nil
	}
	ws, err := s.cfg.Store.GetWorkspace(ctx, workspace)
	if err != nil {
		return false, fmt.Errorf("read workspace environment: %w", err)
	}
	ws, secrets, err := s.workspaceEnvironment(ctx, ws)
	if err != nil {
		return false, err
	}
	env, err := explicitEnvironment(profile, ws, secrets)
	if err != nil {
		return false, err
	}
	return slices.ContainsFunc(keys, func(key string) bool { return env[key] != "" }), nil
}

// checkLogin refuses a launch of agent on account whose login cannot sign it
// in, unless the workspace's environment does.
func (s *Scheduler) checkLogin(ctx context.Context, workspace domain.WorkspaceID, account domain.MemberID, agent string, profile harness.Profile, mode domain.LaunchMode) error {
	// A login the server cannot read is provisioning's refusal to make.
	problem, blocks, err := s.LoginProblem(account, agent, profile)
	if err != nil || !blocks {
		return nil
	}
	if signedIn, envErr := s.environmentSignsIn(ctx, workspace, agent, profile, mode); envErr != nil || signedIn {
		return envErr
	}
	return fmt.Errorf("scheduler: %s's %s %w: %s. Sign in, then launch again: %s",
		s.memberName(ctx, account), cmp.Or(profile.Label(), agent), ErrSignedOut, problem, signInSteps(agent, profile))
}

// runLogin is the login a run's agent signs in with. known is false when it
// cannot be resolved or a workspace variable signs the agent in instead.
type runLogin struct {
	known   bool
	run     domain.RunID
	account domain.MemberID
	owner   string
	// agent is the name the run launched and label what to call it; a
	// definition without an executable resolves to a profile that has
	// neither.
	agent   string
	label   string
	profile harness.Profile
	// fileProblem is loginFileProblem's answer and stamp the login files'.
	fileProblem string
	stamp       string
}

func (s *Scheduler) runLogin(ctx context.Context, runID domain.RunID) runLogin {
	run, err := s.cfg.Store.GetRun(ctx, runID)
	if err != nil {
		return runLogin{}
	}
	login := runLogin{run: runID, account: run.AccountMember(), agent: run.Harness}
	if login.profile, _, err = s.launchProfile(ctx, run.MemberID, login.account, run.Harness); err != nil {
		return runLogin{}
	}
	mode := run.Mode
	if run.ACP {
		mode = domain.LaunchACP
	}
	if byEnv, envErr := s.environmentSignsIn(ctx, run.WorkspaceID, login.agent, login.profile, mode); envErr != nil || byEnv {
		return runLogin{}
	}
	if login.fileProblem, err = s.loginFileProblem(login.account, login.agent, login.profile); err != nil {
		return runLogin{}
	}
	paths, _ := login.profile.LoginPaths()
	if login.stamp, err = s.loginStamp(login.account, paths); err != nil {
		return runLogin{}
	}
	login.known, login.owner, login.label = true, s.memberName(ctx, login.account), cmp.Or(login.profile.Label(), run.Harness)
	return login
}

// What Aether does about an agent's refusal for want of a sign-in.
type refusedHow int

const (
	// refusedRestarting: Aether is restarting the agent, which cures a
	// stale copy of a sound login and not a signed-out one.
	refusedRestarting refusedHow = iota
	// refusedAfterRestart: the restarted agent refused too.
	refusedAfterRestart
	// refusedOpening: a starting agent refused to open its conversation.
	refusedOpening
)

// loginRefusedAdvice records that an agent refused for want of login's
// sign-in and returns what follows the refusal in the run's reason, whose
// length cap keeps its end: whose login it is and where they sign in again.
func (s *Scheduler) loginRefusedAdvice(login runLogin, cause error, how refusedHow) string {
	if !login.known {
		switch how {
		case refusedRestarting:
			return "Aether restarted the agent so it reads the account's login again: send the message again"
		case refusedAfterRestart:
			return "restarting the agent did not restore its sign-in, so this container's copy of the login is stale or unreadable: start a new session"
		}
		return "sign the agent in from this session's Terminal tab, then retry"
	}
	s.loginRefused(login.run, login.account, login.agent, login.stamp, cause)
	steps := signInSteps(login.agent, login.profile)
	switch {
	case how == refusedOpening:
		return fmt.Sprintf("%s's %s login is signed out. Sign in, then retry: %s", login.owner, login.label, steps)
	case login.fileProblem != "":
		return fmt.Sprintf("%s's %s login is signed out. Sign in, then send the message again: %s", login.owner, login.label, steps)
	case how == refusedRestarting:
		return fmt.Sprintf("Aether restarted the agent to reread %s's login: send the message again. Refused again? Sign in: %s", login.owner, steps)
	}
	return fmt.Sprintf("a restart did not help. Start a new session, or if %s's %s login is signed out, sign in: %s", login.owner, login.label, steps)
}

// turnWorked forgets a refusal on the run's account once a turn there ends
// well.
func (s *Scheduler) turnWorked(ctx context.Context, runID domain.RunID) {
	s.mu.Lock()
	none := len(s.loginRefusals) == 0
	s.mu.Unlock()
	if none {
		return
	}
	run, err := s.cfg.Store.GetRun(ctx, runID)
	if err != nil {
		return
	}
	s.mu.Lock()
	delete(s.loginRefusals, loginKey{run.AccountMember(), run.Harness})
	s.mu.Unlock()
}
