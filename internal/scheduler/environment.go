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

// BuildEnvironmentPlan mounts the member's home. A run on another member's
// shared account also mounts the harness's login paths from the owner's home
// and, when the member has no installation, the owner's read-only - nothing
// else of the owner's.
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
			install, err := s.installMounts(member.ID, run.AccountMemberID, profile, home)
			if err != nil {
				return nil, err
			}
			if len(install) > 0 {
				plan.Mounts = append(plan.Mounts, install...)
				// After the image's directories too: the launcher's own
				// executables win, and the owner's fill the gaps.
				env["PATH"] += ":" + path.Join(home, accountBin)
				plan.Path = env["PATH"]
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
	for rel, keys := range profile.BorrowedState {
		if err := s.cfg.Homes.MarkBorrowedState(launcher, rel, keys); err != nil {
			return nil, fmt.Errorf("scheduler: %w", err)
		}
	}
	return mounts, nil
}

// accountBin and accountLib are where a launch that borrows the account
// owner's installation mounts the owner's ~/.local/bin and ~/.local/lib in
// the launcher's home: beside each other, so a launcher linked by relative
// path into ../lib resolves, and apart from the launcher's own ~/.local.
const (
	accountBin = ".aether/account/bin"
	accountLib = ".aether/account/lib"
)

// installMounts borrows account's installation read-only, only when
// launcher's home has none of its own.
func (s *Scheduler) installMounts(launcher, account domain.MemberID, profile harness.Profile, home string) ([]runtime.Mount, error) {
	if len(profile.TUIArgs) == 0 {
		return nil, nil
	}
	installation, err := s.cfg.Homes.Installation(launcher, account, profile.TUIArgs[0], profile.InstallPaths)
	if err != nil {
		return nil, fmt.Errorf("scheduler: find %s: %w", profile.TUIArgs[0], err)
	}
	if installation != account {
		return nil, nil
	}
	ownerHome, err := s.cfg.Homes.Path(account)
	if err != nil {
		return nil, fmt.Errorf("scheduler: resolve account home: %w", err)
	}
	targets := map[string]string{".local/bin": accountBin, ".local/lib": accountLib}
	var mounts []runtime.Mount
	for _, rel := range profile.BorrowRoots() {
		dir, pathErr := s.cfg.Homes.LoginPathIsDir(account, rel)
		if errors.Is(pathErr, fs.ErrNotExist) {
			continue
		}
		if pathErr != nil {
			return nil, fmt.Errorf("scheduler: borrow the account's %s installation: %w", profile.Name, pathErr)
		}
		if !dir {
			return nil, fmt.Errorf("scheduler: borrow the account's %s installation: ~/%s in %q is not a directory", profile.Name, rel, account)
		}
		target, ok := targets[rel]
		if !ok {
			target = rel
		}
		if prepareErr := s.cfg.Homes.PrepareLoginMountpoint(launcher, target, true); prepareErr != nil {
			return nil, fmt.Errorf("scheduler: %w", prepareErr)
		}
		mounts = append(mounts, runtime.Mount{
			HostPath:      ownerHome,
			Subpath:       rel,
			ContainerPath: path.Join(home, target),
			ReadOnly:      true,
		})
	}
	return mounts, nil
}

// accountLogin is one of a profile's login paths that holds a login in an
// account owner's home: a directory or a non-empty file.
type accountLogin struct {
	rel string
	dir bool
}

var errNotLoggedIn = errors.New("not logged in")

type unshareableLoginError struct{ error }

// accountLogins changes nothing in account's home. When every declared path is
// missing it refuses, since the run could only start logged out.
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
			return nil, unshareableLoginError{fmt.Errorf("scheduler: %w", pathErr)}
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
	return nil, fmt.Errorf("scheduler: %s is %w to %s: no login at %s in their home; %s logs in to %s in their own environment terminal",
		owner.DisplayName, errNotLoggedIn, profile.Name, strings.Join(shown, ", "), owner.DisplayName, profile.Name)
}

// SharedLaunch is whether a launch on another member's shared account would
// start, and if not, why.
type SharedLaunch int

const (
	SharedLaunchable SharedLaunch = iota
	// SharedLoginMissing: the account owner has no login for the harness at
	// any declared path.
	SharedLoginMissing
	// SharedLoginUnavailable: the owner's login exists but cannot be shared.
	SharedLoginUnavailable
	// SharedOwnDefinitionOnly: the harness resolves to member's own
	// definition, which runs only on member's own account.
	SharedOwnDefinitionOnly
)

// CheckSharedLaunch reports whether member's launch of harnessName on
// account would be refused over the harness or the owner's login, resolving
// the harness exactly as a launch does. For SharedLoginUnavailable, refusal
// is the error that launch returns. It changes no file in either home;
// reading the owner's home creates it, empty, if it does not exist yet. On
// member's own account a launch is always SharedLaunchable here.
func (s *Scheduler) CheckSharedLaunch(ctx context.Context, member, account domain.MemberID, harnessName string) (state SharedLaunch, refusal string, err error) {
	if account == member || s.cfg.Homes == nil {
		return SharedLaunchable, "", nil
	}
	profile, _, err := s.launchProfile(ctx, member, account, harnessName)
	if errors.Is(err, errMemberDefinitionOnly) {
		return SharedOwnDefinitionOnly, "", nil
	}
	if err != nil {
		return SharedLaunchable, "", err
	}
	_, err = s.accountLogins(ctx, account, profile)
	var unshareable unshareableLoginError
	switch {
	case err == nil:
		return SharedLaunchable, "", nil
	case errors.Is(err, errNotLoggedIn):
		return SharedLoginMissing, "", nil
	case errors.As(err, &unshareable):
		return SharedLoginUnavailable, err.Error(), nil
	}
	return SharedLaunchable, "", err
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
