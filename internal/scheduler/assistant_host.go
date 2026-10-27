package scheduler

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/harness"
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
	if !ok || profile.ACPInstall == nil || profile.InstallScript == "" || len(profile.TUIArgs) == 0 {
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

// StartAssistantExec starts spec in the member's assistant container,
// starting the container and installing agent there first when needed, and
// calls installing before an install. The exec runs in AssistantCwd.
func (s *Scheduler) StartAssistantExec(ctx context.Context, member domain.MemberID, agent string, spec runtime.ExecSpec, installing func()) (runtime.ManagedExec, error) {
	managed, ok := s.cfg.Runtime.(runtime.ManagedExecRuntime)
	if !ok {
		return nil, fmt.Errorf("%w: the runtime cannot run a managed exec", runtime.ErrExecUnavailable)
	}
	profile, ok := harness.Lookup(agent)
	if !ok {
		return nil, fmt.Errorf("agent %q cannot run the assistant", agent)
	}
	cid, err := s.ensureAssistantContainer(ctx, member, profile)
	if err != nil {
		return nil, err
	}
	if err := s.installAssistant(ctx, cid, profile, installing); err != nil {
		return nil, err
	}
	spec.WorkingDir = AssistantCwd
	return managed.StartExecPipe(ctx, cid, spec)
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
