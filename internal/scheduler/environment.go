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
	"slices"
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
				plan.Mounts = append(plan.Mounts, logins...)
				plan.LoginMember = run.AccountMemberID
			}
		}
		pins, err := s.pinnedLogins(ctx, member.ID, homePath, home, plan.Mounts)
		if err != nil {
			return nil, err
		}
		plan.Mounts = append(plan.Mounts, pins...)
		nestings = make(map[string]string, len(plan.Mounts)-1)
		for _, m := range plan.Mounts[1:] {
			nestings[m.ContainerPath] = home
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

// loginMounts mounts each of account's logins for profile over the same path
// in launcher's home.
func (s *Scheduler) loginMounts(ctx context.Context, launcher, account domain.MemberID, profile harness.Profile, home string) ([]runtime.Mount, error) {
	logins, err := s.accountLogins(ctx, account, profile)
	if err != nil || len(logins) == 0 {
		return nil, err
	}
	ownerHome, err := s.cfg.Homes.Path(account)
	if err != nil {
		return nil, fmt.Errorf("scheduler: resolve account home: %w", err)
	}
	mounts := make([]runtime.Mount, 0, len(logins))
	for _, login := range logins {
		if prepareErr := s.cfg.Homes.PrepareLoginMountpoint(launcher, login.rel, login.dir); prepareErr != nil {
			return nil, fmt.Errorf("scheduler: %w", prepareErr)
		}
		mounts = append(mounts, runtime.Mount{
			HostPath:      ownerHome,
			Subpath:       login.rel,
			ContainerPath: path.Join(home, login.rel),
		})
	}
	return mounts, nil
}

// accountLogin is one of a profile's login paths that exists in an account
// owner's home.
type accountLogin struct {
	rel string
	dir bool
}

// accountLogins resolves profile's login paths in account's home without
// changing anything in it. A profile that declares none has nothing to share.
// One whose declared paths are all missing is refused, since the run could
// only start logged out, and so is a path that cannot be shared.
func (s *Scheduler) accountLogins(ctx context.Context, account domain.MemberID, profile harness.Profile) ([]accountLogin, error) {
	paths, err := profile.LoginPaths()
	if err != nil {
		return nil, fmt.Errorf("scheduler: %w", err)
	}
	var logins []accountLogin
	for _, rel := range paths {
		dir, pathErr := s.cfg.Homes.LoginPathIsDir(account, rel)
		if errors.Is(pathErr, fs.ErrNotExist) {
			continue
		}
		if pathErr != nil {
			return nil, fmt.Errorf("scheduler: %w", pathErr)
		}
		logins = append(logins, accountLogin{rel: rel, dir: dir})
	}
	if len(logins) > 0 || len(paths) == 0 {
		return logins, nil
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

// LoginMissing reports whether member's launch of harnessName on account
// would be refused over the account owner's login: the owner has no
// definition of a member-defined harness's name, or the harness's login
// paths are missing from the owner's home or cannot be shared. It resolves
// exactly as a launch does, but changes nothing in either home.
func (s *Scheduler) LoginMissing(ctx context.Context, member, account domain.MemberID, harnessName string) (bool, error) {
	if account == member || s.cfg.Homes == nil {
		return false, nil
	}
	profile, _, _, err := s.launchProfile(ctx, member, account, harnessName)
	if errors.Is(err, errNoAccountDefinition) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	_, err = s.accountLogins(ctx, account, profile)
	return err != nil, nil
}

// pinnedLogins mounts each login file of a PinLogin harness in member's own
// home over itself once member shares their account, except where the plan
// already mounts another member's login. A login path that is a directory or
// cannot be shared is left unpinned rather than refused: it cannot be shared
// either, and refusing would keep the member from the terminal that fixes it.
// A grantee still pending approval counts: approval takes effect while this
// member's containers keep running.
func (s *Scheduler) pinnedLogins(ctx context.Context, member domain.MemberID, homePath, home string, planned []runtime.Mount) ([]runtime.Mount, error) {
	grantees, err := s.cfg.Store.ListAccountGrantees(ctx, member)
	if err != nil {
		return nil, fmt.Errorf("scheduler: list account grantees: %w", err)
	}
	if len(grantees) == 0 {
		return nil, nil
	}
	var pins []runtime.Mount
	for _, profile := range harness.Profiles() {
		if !profile.PinLogin {
			continue
		}
		paths, err := profile.LoginPaths()
		if err != nil {
			return nil, fmt.Errorf("scheduler: %w", err)
		}
		for _, rel := range paths {
			target := path.Join(home, rel)
			if slices.ContainsFunc(planned, func(m runtime.Mount) bool { return m.ContainerPath == target }) {
				continue
			}
			dir, pathErr := s.cfg.Homes.LoginPathIsDir(member, rel)
			if errors.Is(pathErr, fs.ErrNotExist) {
				pathErr = s.cfg.Homes.PrepareLoginMountpoint(member, rel, false)
			}
			if pathErr != nil || dir {
				continue
			}
			pins = append(pins, runtime.Mount{HostPath: homePath, Subpath: rel, ContainerPath: target})
		}
	}
	return pins, nil
}

func worktreePath(run *domain.Run) string {
	if run == nil {
		return ""
	}
	return run.Worktree
}
