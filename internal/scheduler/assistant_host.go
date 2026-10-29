package scheduler

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/harness"
	"github.com/3xDevOps/Aether/internal/ptyhost"
	"github.com/3xDevOps/Aether/internal/runtime"
)

// AssistantLaunch is how the assistant starts agent's ACP server: the
// shipped profile's adapter, session mode and agent environment. Claude Code
// refuses its bypass mode as root without the profile's IS_SANDBOX.
type AssistantLaunch struct {
	Argv []string
	Mode string
	Env  []string
}

// AssistantAdapter resolves agent from the shipped registry only: neither a
// member's definition nor a server-wide override decides what runs beside
// the assistant's tool socket.
func (s *Scheduler) AssistantAdapter(agent string) (AssistantLaunch, error) {
	profile, ok := harness.Lookup(agent)
	if !ok || profile.ACPInstall == nil {
		return AssistantLaunch{}, fmt.Errorf("agent %q cannot run the assistant", agent)
	}
	if profile.ACPMode == "" {
		return AssistantLaunch{}, fmt.Errorf("agent %q has no mode that acts without asking", agent)
	}
	env := map[string]string{}
	if err := harness.MergeEnv(env, profile.PermissionEnv); err != nil {
		return AssistantLaunch{}, fmt.Errorf("scheduler: apply %s's permission setting: %w", profile.Name, err)
	}
	maps.Copy(env, profile.Env)
	maps.Copy(env, assistantEnv)
	launch := AssistantLaunch{Argv: profile.ACPArgs, Mode: profile.ACPMode}
	for _, key := range slices.Sorted(maps.Keys(env)) {
		launch.Env = append(launch.Env, key+"="+env[key])
	}
	return launch, nil
}

// assistantEnv turns off what would keep notes across conversations, which
// a reset could not discard. Codex keeps none in a fresh home.
var assistantEnv = map[string]string{"CLAUDE_CODE_DISABLE_AUTO_MEMORY": "1"}

// StartAssistantExec starts spec in a new assistant container for the
// member, installing agent's adapter first when needed and calling
// installing before an install. Each start replaces the container and
// prepares the home, so nothing the agent wrote outside its sign-in and
// sessions reaches the next one; a sign-in file holding anything else is
// cleaned and the start refused. The exec runs in AssistantCwd.
func (s *Scheduler) StartAssistantExec(ctx context.Context, member domain.MemberID, agent string, spec runtime.ExecSpec, installing func()) (runtime.ManagedExec, error) {
	managed, ok := s.cfg.Runtime.(runtime.ManagedExecRuntime)
	if !ok {
		return nil, fmt.Errorf("%w: the runtime cannot run a managed exec", runtime.ErrExecUnavailable)
	}
	cid, err := s.freshAssistantContainer(ctx, member, agent, installing, "")
	if err != nil {
		return nil, err
	}
	spec.WorkingDir = AssistantCwd
	return managed.StartExecPipe(ctx, cid, spec)
}

// assistantLoginArgv is each agent's sign-in, run with the adapter's own
// CLI.
var assistantLoginArgv = map[string][]string{
	"claude": {"claude-agent-acp", "--cli", "auth", "login"},
	"codex":  {"codex", "login", "--device-auth"},
}

// StartAssistantLogin starts agent's sign-in for the member's assistant on
// a PTY session, unless it runs already, and returns the session to attach
// to. It replaces the assistant container like a start, so callers stop the
// assistant's session first and serialize with starting it.
func (s *Scheduler) StartAssistantLogin(ctx context.Context, member domain.MemberID, agent string, cols, rows uint) (ptyhost.SessionKey, error) {
	argv, ok := assistantLoginArgv[agent]
	if !ok {
		return "", fmt.Errorf("agent %q cannot run the assistant", agent)
	}
	key := ptyhost.AssistantLoginSession(member, agent)
	lock := s.assistantLoginLock(member)
	lock.Lock()
	defer lock.Unlock()
	cid, err := s.freshAssistantContainer(ctx, member, agent, func() {}, agent)
	if err != nil || cid == "" {
		return key, err
	}
	att, err := s.cfg.Runtime.ExecTTY(ctx, cid, argv, AssistantCwd, cols, rows)
	if err != nil {
		return "", fmt.Errorf("scheduler: start the %s sign-in: %w", agent, err)
	}
	if err := s.cfg.PTY.StartSession(ctx, key, att); err != nil {
		_ = att.Close()
		return "", fmt.Errorf("scheduler: start the %s sign-in: %w", agent, err)
	}
	return key, nil
}

// EndAssistantLogin removes the transcript of agent's sign-in for the
// member once it exits: it shows the sign-in URL and codes.
func (s *Scheduler) EndAssistantLogin(ctx context.Context, member domain.MemberID, agent string) error {
	lock := s.assistantLoginLock(member)
	lock.Lock()
	defer lock.Unlock()
	return s.endAssistantLogin(ctx, member, agent)
}

func (s *Scheduler) endAssistantLogin(ctx context.Context, member domain.MemberID, agent string) error {
	key := ptyhost.AssistantLoginSession(member, agent)
	if slices.Contains(s.cfg.PTY.ActiveSessions(string(key)), key) {
		return nil
	}
	if err := s.cfg.PTY.RemoveSessionTranscript(ctx, key); err != nil {
		return fmt.Errorf("scheduler: remove the %s sign-in transcript: %w", agent, err)
	}
	return nil
}

// removeAssistantLoginTranscripts removes the transcripts of the member's
// sign-ins that no longer run.
func (s *Scheduler) removeAssistantLoginTranscripts(ctx context.Context, member domain.MemberID) error {
	for _, agent := range assistantAgents {
		if err := s.endAssistantLogin(ctx, member, agent); err != nil {
			return err
		}
	}
	return nil
}

// freshAssistantContainer removes the member's assistant container,
// installs agent's adapter, prepares the assistant home and starts a new
// container. A new container would end a running sign-in, so it refuses
// while one runs; for signIn, the agent being signed in, a running sign-in of
// that agent returns no container and no error. A start, but not a sign-in,
// is refused once a sign-in file held keys Aether does not keep.
func (s *Scheduler) freshAssistantContainer(ctx context.Context, member domain.MemberID, agent string, installing func(), signIn string) (runtime.ID, error) {
	profile, ok := harness.Lookup(agent)
	if !ok || profile.ACPInstall == nil {
		return "", fmt.Errorf("agent %q cannot run the assistant", agent)
	}
	lock := s.assistantLock(member)
	lock.Lock()
	defer lock.Unlock()
	// A member removed while this waited would get their assistant back.
	if _, err := s.cfg.Store.GetMember(ctx, member); err != nil {
		return "", fmt.Errorf("scheduler: the assistant's member: %w", err)
	}
	running := s.cfg.PTY.ActiveSessions(string(ptyhost.AssistantLoginSession(member, "")))
	switch {
	case signIn != "" && slices.Contains(running, ptyhost.AssistantLoginSession(member, signIn)):
		return "", nil
	case len(running) > 0:
		return "", fmt.Errorf("scheduler: a sign-in for the assistant is running (%s); finish it first", running[0])
	}
	release, err := s.tryReserveCapacity(ctx)
	if err != nil {
		return "", err
	}
	defer release()
	if err = s.removeAssistantContainer(ctx, member); err != nil {
		return "", err
	}
	if err = s.installAssistant(ctx, member, profile, installing); err != nil {
		return "", err
	}
	removed, err := s.prepareAssistantHome(ctx, member)
	if err != nil {
		return "", err
	}
	if err = s.removeAssistantLoginTranscripts(ctx, member); err != nil {
		return "", err
	}
	// The container mounts every agent's sign-in, so a sign-in still waiting
	// for its record is recorded before anything runs that could rewrite it.
	if err = s.recordPendingSignIns(ctx, member, agent); err != nil {
		return "", err
	}
	if signIn != "" {
		// What the sign-in writes is what gets recorded, never a file the
		// agent left.
		if err = s.forgetAssistantSignIn(member, signIn); err != nil {
			return "", err
		}
		if err = s.markAssistantSignIn(member, signIn); err != nil {
			return "", err
		}
		return s.createAssistantContainer(ctx, member)
	}
	if len(removed) > 0 {
		return "", fmt.Errorf("scheduler: the assistant's sign-in held what Aether does not keep, and it was removed: %s. Retry starts with the rest; if the agent then reports no sign-in, sign in for the assistant again", strings.Join(removed, "; "))
	}
	if err = s.verifyAssistantSignIn(ctx, member, agent); err != nil {
		return "", err
	}
	return s.createAssistantContainer(ctx, member)
}

// StopAssistantExec stops an adapter an earlier session or server process
// started. Only an exec the runtime proves gone counts as stopped.
func (s *Scheduler) StopAssistantExec(ctx context.Context, identity runtime.ExecIdentity) error {
	managed, ok := s.cfg.Runtime.(runtime.ManagedExecRuntime)
	if !ok {
		return fmt.Errorf("%w: the runtime cannot recover a managed exec", runtime.ErrExecUnavailable)
	}
	exec, err := managed.RecoverExec(ctx, identity)
	if errors.Is(err, runtime.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("recover the assistant's adapter (exec %s): %w", identity.ExecID, err)
	}
	defer func() { _ = exec.Detach() }()
	if _, err = exec.Stop(ctx, acpStopGrace); err != nil {
		if state, statusErr := exec.Status(ctx); statusErr == nil && !state.Running {
			return nil
		}
		return fmt.Errorf("stop the assistant's adapter (exec %s): %w", identity.ExecID, err)
	}
	return nil
}
