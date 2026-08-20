package scheduler

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/harness"
	"github.com/3xDevOps/Aether/internal/runtime"
	"github.com/3xDevOps/Aether/internal/store"
)

// EnvironmentPurpose identifies the consumer of an environment plan.
type EnvironmentPurpose string

const (
	EnvironmentPurposeRun       EnvironmentPurpose = "run"
	EnvironmentPurposeBootstrap EnvironmentPurpose = "bootstrap"
	EnvironmentPurposeLogin     EnvironmentPurpose = "login"
)

// EnvironmentPlan is the complete, server-assembled container environment.
// Host paths in Mounts are derived only from configured server roots.
type EnvironmentPlan struct {
	Purpose      EnvironmentPurpose
	Image        string
	Env          map[string]string
	SetupScript  string
	User         string
	Home         string
	Path         string
	Mounts       []runtime.Mount
	ToolSnapshot *domain.ToolSnapshot
	ToolHostPath string
}

// BuildEnvironmentPlan resolves one immutable environment. A run's active
// tool head is pinned before the caller creates its container.
func (s *Scheduler) BuildEnvironmentPlan(ctx context.Context, run *domain.Run, ws *domain.Workspace, member *domain.Member, profile harness.Profile, purpose EnvironmentPurpose, stagingPath string) (*EnvironmentPlan, error) {
	if ws == nil || member == nil {
		return nil, errors.New("scheduler: workspace and member are required")
	}
	if purpose != EnvironmentPurposeRun && purpose != EnvironmentPurposeBootstrap && purpose != EnvironmentPurposeLogin {
		return nil, fmt.Errorf("scheduler: invalid environment purpose %q", purpose)
	}
	image := ws.Environment.EffectiveImage(s.cfg.NeutralImage)
	if image == "" {
		return nil, errors.New("scheduler: workspace has no effective image")
	}
	user, err := s.resolveContainerUser(ctx, image, profile)
	if err != nil {
		return nil, fmt.Errorf("scheduler: resolve environment user: %w", err)
	}
	home := harness.HomeDir(user)
	if home == "" {
		home = "/root"
	}
	env := make(map[string]string, len(ws.Environment.Variables)+len(profile.EnvPassthrough)+5)
	for _, key := range profile.EnvPassthrough {
		if value, ok := os.LookupEnv(key); ok && value != "" {
			env[key] = value
		}
	}
	for key, value := range ws.Environment.Variables {
		env[key] = value
	}
	env["HOME"] = home
	env["TERM"] = "xterm-256color"
	toolBin := filepath.Join(home, ".local", "bin")
	pathValue := env["PATH"]
	if pathValue == "" {
		pathValue = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
	}
	env["PATH"] = toolBin + ":" + pathValue
	plan := &EnvironmentPlan{
		Purpose: purpose, Image: image, Env: env,
		SetupScript: ws.Environment.SetupPolicy.Script,
		User:        user, Home: home, Path: env["PATH"],
	}

	if purpose == EnvironmentPurposeBootstrap {
		if stagingPath == "" {
			return nil, errors.New("scheduler: bootstrap staging path is required")
		}
		if s.cfg.Toolenv == nil {
			return nil, errors.New("scheduler: tool environment is not configured")
		}
		plan.ToolHostPath = stagingPath
		plan.Mounts = append(plan.Mounts, runtime.Mount{HostPath: stagingPath, ContainerPath: filepath.Join(home, ".local")})
	} else {
		snapshot, snapErr := s.resolveToolSnapshot(ctx, run, member.ID, ws.ID, purpose == EnvironmentPurposeRun)
		if snapErr != nil && !errors.Is(snapErr, store.ErrNotFound) {
			return nil, fmt.Errorf("scheduler: resolve tool snapshot: %w", snapErr)
		}
		if snapshot != nil {
			if s.cfg.Toolenv == nil {
				return nil, errors.New("scheduler: tool environment is not configured")
			}
			toolPath, pathErr := s.cfg.Toolenv.ResolvePath(ctx, member.ID, ws.ID, snapshot.ID)
			if pathErr != nil {
				return nil, fmt.Errorf("scheduler: resolve tool snapshot path: %w", pathErr)
			}
			plan.ToolSnapshot = snapshot
			plan.ToolHostPath = toolPath
			plan.Mounts = append(plan.Mounts, runtime.Mount{HostPath: toolPath, ContainerPath: filepath.Join(home, ".local"), ReadOnly: true})
		}
	}
	if purpose != EnvironmentPurposeBootstrap && profile.Name != "" {
		mountRun := run
		if mountRun == nil {
			mountRun = &domain.Run{}
		}
		creds, credErr := s.credentialMounts(mountRun, member.ID, profile, home)
		if credErr != nil {
			return nil, credErr
		}
		if mountRun.Worktree != "" {
			profileMounts, profileErr := s.withProfileMounts(ctx, mountRun, profile, home, creds)
			if profileErr != nil {
				return nil, profileErr
			}
			plan.Mounts = append(plan.Mounts, profileMounts...)
		} else {
			plan.Mounts = append(plan.Mounts, creds...)
		}
	}
	roots := []string{}
	if s.cfg.HomesDir != "" {
		roots = append(roots, s.cfg.HomesDir)
	}
	if s.cfg.ProfilesDir != "" {
		roots = append(roots, s.cfg.ProfilesDir)
	}
	if s.cfg.Toolenv != nil {
		roots = append(roots, s.cfg.Toolenv.Root())
	}
	allowedNestings := make(map[string]string)
	for i, childMount := range plan.Mounts {
		child := path.Clean(childMount.ContainerPath)
		for j := 0; j < i; j++ {
			parent := path.Clean(plan.Mounts[j].ContainerPath)
			if child != parent && strings.HasPrefix(child, parent+"/") {
				allowedNestings[child] = parent
				break
			}
		}
	}
	if validateErr := runtime.ValidateMounts(plan.Mounts, runtime.MountPolicy{
		OwnedRoots: roots, WorktreeHostPath: worktreePath(run),
		WorktreeMountPath: s.cfg.WorktreeMount, AllowedNestings: allowedNestings,
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

func (s *Scheduler) resolveToolSnapshot(ctx context.Context, run *domain.Run, member domain.MemberID, workspace domain.WorkspaceID, pin bool) (*domain.ToolSnapshot, error) {
	if s.cfg.Store == nil {
		return nil, store.ErrNotFound
	}
	var snapshot *domain.ToolSnapshot
	var err error
	if run != nil && run.ToolSnapshotID != "" {
		snapshot, err = s.cfg.Store.GetToolSnapshot(ctx, run.ToolSnapshotID)
	} else {
		snapshot, err = s.cfg.Store.GetActiveToolSnapshot(ctx, member, workspace)
		if err == nil && run != nil && pin {
			if updateErr := s.cfg.Store.SetRunToolSnapshot(ctx, run.ID, snapshot.ID); updateErr != nil {
				return nil, updateErr
			}
		}
	}
	return snapshot, err
}

// ToolEnvRoot is retained as a small convenience for server wiring.
func (s *Scheduler) ToolEnvRoot() string {
	if s.cfg.Toolenv == nil {
		return ""
	}
	return strings.TrimSpace(s.cfg.Toolenv.Root())
}
