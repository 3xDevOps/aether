package scheduler

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"strings"

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
	// LoginMember is the account owner whose login paths are mounted, empty
	// when the plan mounts nothing from another member's home.
	LoginMember domain.MemberID
}

// BuildEnvironmentPlan resolves the image, user, environment, and
// server-owned mounts for one member container: the member's image and home.
// A run launched on another member's shared account additionally mounts the
// harness's declared login paths from that account owner's home over the
// same paths in the member's home, and nothing else of the owner's.
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
	// The harness's own launch requirements come after workspace
	// variables: a run whose agent refuses to start is not a preference.
	maps.Copy(env, profile.Env)
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
	var nestings map[string]string
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
		if purpose == EnvironmentPurposeRun && run != nil && run.AccountMemberID != "" && run.AccountMemberID != member.ID {
			logins, err := s.loginMounts(ctx, member.ID, run.AccountMemberID, profile, home)
			if err != nil {
				return nil, err
			}
			if len(logins) > 0 {
				nestings = make(map[string]string, len(logins))
				for _, m := range logins {
					nestings[m.ContainerPath] = home
				}
				plan.Mounts = append(plan.Mounts, logins...)
				plan.LoginMember = run.AccountMemberID
			}
		}
	}
	var roots []string
	if s.cfg.Homes != nil {
		roots = []string{s.cfg.Homes.Root()}
	}
	if validateErr := runtime.ValidateMounts(plan.Mounts, runtime.MountPolicy{
		OwnedRoots:        roots,
		WorktreeHostPath:  worktreePath(run),
		WorktreeMountPath: s.cfg.WorktreeMount,
		AllowedNestings:   nestings,
	}); validateErr != nil {
		return nil, validateErr
	}
	return plan, nil
}

// loginMounts mounts each of profile's login paths that exists in account's
// home over the same path in launcher's home. A profile that declares none
// mounts nothing; one whose declared paths are all missing is refused, since
// the run could only start logged out.
func (s *Scheduler) loginMounts(ctx context.Context, launcher, account domain.MemberID, profile harness.Profile, home string) ([]runtime.Mount, error) {
	paths, err := profile.LoginPaths()
	if err != nil {
		return nil, fmt.Errorf("scheduler: %w", err)
	}
	if len(paths) == 0 {
		return nil, nil
	}
	ownerHome, err := s.cfg.Homes.Path(account)
	if err != nil {
		return nil, fmt.Errorf("scheduler: resolve account home: %w", err)
	}
	var mounts []runtime.Mount
	for _, rel := range paths {
		dir, pathErr := s.cfg.Homes.LoginPathIsDir(account, rel)
		if errors.Is(pathErr, fs.ErrNotExist) {
			continue
		}
		if pathErr != nil {
			return nil, fmt.Errorf("scheduler: %w", pathErr)
		}
		if prepareErr := s.cfg.Homes.PrepareLoginMountpoint(launcher, rel, dir); prepareErr != nil {
			return nil, fmt.Errorf("scheduler: %w", prepareErr)
		}
		mounts = append(mounts, runtime.Mount{
			HostPath:      ownerHome,
			Subpath:       rel,
			ContainerPath: path.Join(home, rel),
		})
	}
	if len(mounts) > 0 {
		return mounts, nil
	}
	owner, err := s.cfg.Store.GetMember(ctx, account)
	if err != nil {
		return nil, fmt.Errorf("scheduler: get account owner: %w", err)
	}
	shown := make([]string, len(paths))
	for i, rel := range paths {
		shown[i] = "~/" + rel
	}
	return nil, fmt.Errorf("scheduler: %s is not logged in to %s: none of %s exists in their home; %s logs in from their own environment terminal (aether terminal)",
		owner.DisplayName, profile.Name, strings.Join(shown, ", "), owner.DisplayName)
}

func worktreePath(run *domain.Run) string {
	if run == nil {
		return ""
	}
	return run.Worktree
}
