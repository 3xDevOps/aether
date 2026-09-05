package scheduler

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/harness"
	"github.com/3xDevOps/Aether/internal/runtime"
)

// EnvironmentPurpose identifies the consumer of an environment plan.
type EnvironmentPurpose string

const (
	EnvironmentPurposeRun      EnvironmentPurpose = "run"
	EnvironmentPurposeTerminal EnvironmentPurpose = "terminal"
)

// EnvironmentPlan is the complete, server-assembled container environment.
// Host paths in Mounts are derived only from configured server roots.
type EnvironmentPlan struct {
	Purpose     EnvironmentPurpose
	Image       string
	Env         map[string]string
	SetupScript string
	User        string
	Home        string
	Path        string
	Mounts      []runtime.Mount
}

// BuildEnvironmentPlan resolves the image, user, environment, and
// server-owned mounts for one member container.
func (s *Scheduler) BuildEnvironmentPlan(ctx context.Context, run *domain.Run, ws *domain.Workspace, member *domain.Member, profile harness.Profile, purpose EnvironmentPurpose) (*EnvironmentPlan, error) {
	switch purpose {
	case EnvironmentPurposeRun, EnvironmentPurposeTerminal:
	default:
		return nil, fmt.Errorf("scheduler: invalid environment purpose %q", purpose)
	}
	if member == nil {
		return nil, errors.New("scheduler: member is required")
	}
	if purpose == EnvironmentPurposeRun && ws == nil {
		return nil, errors.New("scheduler: workspace is required for run environment")
	}
	image := member.Image
	if image == "" {
		image = s.cfg.StandardImage
	}
	if image == "" {
		return nil, errors.New("scheduler: standard image is required")
	}
	if member.Image != "" {
		exists, err := s.cfg.Runtime.ImageExists(ctx, image)
		if err != nil {
			return nil, fmt.Errorf("scheduler: check saved environment image %q: %w", image, err)
		}
		if !exists {
			return nil, fmt.Errorf("scheduler: saved environment image %q is missing from the runtime; run aether env reset to return to the standard image", image)
		}
	}
	user, err := s.resolveContainerUser(ctx, image, profile)
	if err != nil {
		return nil, fmt.Errorf("scheduler: resolve environment user: %w", err)
	}
	home := harness.HomeDir(user)
	if home == "" {
		home = "/root"
	}
	var setupScript string
	if ws != nil {
		setupScript = ws.Environment.SetupPolicy.Script
	}
	var variableCount int
	if ws != nil {
		variableCount = len(ws.Environment.Variables)
	}
	env := make(map[string]string, variableCount+len(profile.EnvPassthrough)+5)
	for _, key := range profile.EnvPassthrough {
		if value, ok := os.LookupEnv(key); ok && value != "" {
			env[key] = value
		}
	}
	if ws != nil {
		for key, value := range ws.Environment.Variables {
			env[key] = value
		}
	}
	env["HOME"] = home
	env["TERM"] = "xterm-256color"
	localBin := filepath.Join(home, ".local", "bin")
	pathValue := env["PATH"]
	if pathValue == "" {
		pathValue = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
	}
	env["PATH"] = localBin + ":" + pathValue
	plan := &EnvironmentPlan{
		Purpose: purpose, Image: image, Env: env,
		SetupScript: setupScript,
		User:        user, Home: home, Path: env["PATH"],
	}
	if s.cfg.Homes != nil {
		homePath, pathErr := s.cfg.Homes.Path(member.ID)
		if pathErr != nil {
			return nil, fmt.Errorf("scheduler: resolve member home: %w", pathErr)
		}
		plan.Mounts = append(plan.Mounts, runtime.Mount{
			HostPath:      homePath,
			ContainerPath: home,
			ReadOnly:      false,
		})
	}
	var roots []string
	if s.cfg.Homes != nil {
		roots = []string{s.cfg.Homes.Root()}
	}
	if validateErr := runtime.ValidateMounts(plan.Mounts, runtime.MountPolicy{
		OwnedRoots:        roots,
		WorktreeHostPath:  worktreePath(run),
		WorktreeMountPath: s.cfg.WorktreeMount,
	}); validateErr != nil {
		return nil, validateErr
	}
	return plan, nil
}

func worktreePath(run *domain.Run) string {
	if run == nil {
		return ""
	}
	return run.Worktree
}
