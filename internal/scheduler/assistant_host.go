package scheduler

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"path"
	"path/filepath"
	"slices"

	"github.com/3xDevOps/Aether/internal/coordtransport"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/harness"
	"github.com/3xDevOps/Aether/internal/runtime"
)

// ErrAssistantNeedsRestart means the member's terminal was created without
// the assistant's mounts, which only creation can add.
var ErrAssistantNeedsRestart = errors.New("scheduler: the environment was created before the assistant existed; restart it to add the assistant's mounts")

// AssistantSocketDir is the host directory mounted read-only at
// coordtransport.AssistantMountDir in the member's terminal.
func AssistantSocketDir(root string, member domain.MemberID) string {
	return filepath.Join(root, string(member), "sock")
}

// AssistantLaunch is how the assistant starts agent's ACP server: the same
// command, session mode and agent environment an Enhanced run uses. Claude
// Code refuses its bypass mode as root without the profile's IS_SANDBOX.
type AssistantLaunch struct {
	Argv []string
	Mode string
	Env  []string
}

func (s *Scheduler) AssistantAdapter(ctx context.Context, member domain.MemberID, agent string) (AssistantLaunch, error) {
	profile, argvs, err := s.launchProfile(ctx, member, member, agent)
	if err != nil {
		return AssistantLaunch{}, err
	}
	argv := argvs[domain.LaunchACP]
	if len(argv) == 0 {
		return AssistantLaunch{}, fmt.Errorf("agent %q has no ACP command", agent)
	}
	if profile.ACPMode == "" {
		return AssistantLaunch{}, fmt.Errorf("agent %q has no mode that acts without asking", agent)
	}
	env := map[string]string{}
	if err := harness.MergeEnv(env, profile.PermissionEnv); err != nil {
		return AssistantLaunch{}, fmt.Errorf("scheduler: apply %s's permission setting: %w", profile.Name, err)
	}
	maps.Copy(env, profile.Env)
	launch := AssistantLaunch{Argv: argv, Mode: profile.ACPMode}
	for _, key := range slices.Sorted(maps.Keys(env)) {
		launch.Env = append(launch.Env, key+"="+env[key])
	}
	return launch, nil
}

// AssistantTerminalStale reports whether the member's running terminal lacks
// the assistant's mounts. A terminal that is not running is not stale: the
// next one is created with them.
func (s *Scheduler) AssistantTerminalStale(ctx context.Context, member domain.MemberID) (bool, error) {
	sup := s.lookupLiveTerminal(member)
	if sup == nil {
		return false, nil
	}
	info, err := s.cfg.Runtime.Inspect(ctx, sup.containerID)
	if err != nil {
		return false, fmt.Errorf("inspect the environment terminal: %w", err)
	}
	return !slices.Contains(info.MountTargets, coordtransport.AssistantMountDir) ||
		!slices.Contains(info.MountTargets, coordtransport.BinaryPath), nil
}

// StartAssistantExec starts spec in the member's environment terminal,
// starting the terminal if needed, with spec.WorkingDir resolved against the
// member's $HOME; it returns the resolved directory. The terminal lock is not
// held across the exec.
func (s *Scheduler) StartAssistantExec(ctx context.Context, member domain.MemberID, spec runtime.ExecSpec) (runtime.ManagedExec, string, error) {
	managed, ok := s.cfg.Runtime.(runtime.ManagedExecRuntime)
	if !ok {
		return nil, "", fmt.Errorf("%w: the runtime cannot run a managed exec", runtime.ErrExecUnavailable)
	}
	if _, err := s.EnsureTerminal(ctx, member); err != nil {
		return nil, "", err
	}
	sup := s.lookupLiveTerminal(member)
	if sup == nil {
		return nil, "", ErrTerminalNotRunning
	}
	if stale, err := s.AssistantTerminalStale(ctx, member); err != nil {
		return nil, "", err
	} else if stale {
		return nil, "", ErrAssistantNeedsRestart
	}
	spec.WorkingDir = path.Join(sup.home, spec.WorkingDir)
	exec, err := managed.StartExecPipe(ctx, sup.containerID, spec)
	return exec, spec.WorkingDir, err
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
