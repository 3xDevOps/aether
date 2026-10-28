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

	"github.com/3xDevOps/Aether/internal/coordtransport"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/harness"
	"github.com/3xDevOps/Aether/internal/runtime"
)

type subpathAPIChecker interface {
	CheckSubpathAPI(context.Context) error
}

type imageCacheEnvironmentResolver interface {
	ImageCacheEnvironment(context.Context, string) (map[string]string, error)
}

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
	// Worktrees are not Mounts because a mount is chowned wholesale, which a
	// checkout of hard-linked objects must not be.
	Worktrees []runtime.Mount
	// LoginMember is the account owner whose login paths are mounted, empty
	// when the plan mounts nothing from another member's home.
	LoginMember domain.MemberID
	// SecretEnv names the workspace secrets in Env.
	SecretEnv []string
	Egress    bool
}

// BuildEnvironmentPlan mounts the member's home. A run on another member's
// shared account also mounts the harness's login paths from the owner's home
// and, when the member has no installation, the owner's read-only - nothing
// else of the owner's.
// Callers hold Homes.LockCaches across planning and durable runtime ownership
// publication; planning creates/touches cache metadata, not just an inert spec.
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
	var secrets map[string]string
	if ws != nil {
		if ws, secrets, err = s.workspaceEnvironment(ctx, ws); err != nil {
			return nil, err
		}
		setupScript = ws.Environment.SetupPolicy.Script
	}
	env, err := explicitEnvironment(profile, ws, secrets)
	if err != nil {
		return nil, err
	}
	if run != nil {
		if err := harness.MergeEnv(env, profile.ModelLaunchEnv(run.Model)); err != nil {
			return nil, fmt.Errorf("scheduler: apply the run's model over the workspace variables: %w", err)
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
		SecretEnv:   slices.Sorted(maps.Keys(secrets)),
		User:        user, Home: home, Path: env["PATH"],
		Egress: purpose == EnvironmentPurposeRun && ws.Environment.Egress.On,
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
		pool := "runs"
		if purpose == EnvironmentPurposeTerminal {
			pool = "terminal"
		}
		cachePath, cacheErr := s.cfg.Homes.CachePath(member.ID, pool)
		if cacheErr != nil {
			return nil, fmt.Errorf("scheduler: resolve member cache: %w", cacheErr)
		}
		plan.Mounts = append(plan.Mounts, runtime.Mount{HostPath: cachePath, ContainerPath: "/aether-cache"})
		scratch := ""
		switch {
		case purpose == EnvironmentPurposeTerminal:
			scratch = terminalScratchName(member.ID)
		case run != nil && run.ID != "":
			scratch = string(run.ID)
		}
		if scratch != "" {
			scratchPath, scratchErr := s.ensureScratch(scratch)
			if scratchErr != nil {
				return nil, scratchErr
			}
			plan.Mounts = append(plan.Mounts, runtime.Mount{HostPath: scratchPath, ContainerPath: "/tmp"})
		}
		if resolver, ok := s.cfg.Runtime.(imageCacheEnvironmentResolver); ok {
			inherited, err := resolver.ImageCacheEnvironment(ctx, image)
			if err != nil {
				return nil, fmt.Errorf("scheduler: resolve image cache settings: %w", err)
			}
			inheritCacheEnvironment(env, inherited)
		}
		if err := s.cfg.Homes.ProtectLegacyCaches(member.ID, explicitHomeCachePaths(env, home)); err != nil {
			return nil, fmt.Errorf("scheduler: protect explicit home caches: %w", err)
		}
		applyCacheEnvironment(env)
		applyInstallEnvironment(env, home)
	}
	var roots []string
	if s.cfg.Homes != nil {
		roots = []string{s.cfg.Homes.Root(), s.cfg.Homes.CacheRoot(), s.scratchRoot()}
	}
	if s.cfg.Signing != nil {
		agentDir, err := s.cfg.Signing.Serve(member.ID)
		if err != nil {
			return nil, fmt.Errorf("scheduler: serve the commit signing agent: %w", err)
		}
		plan.Mounts = append(plan.Mounts, runtime.Mount{HostPath: agentDir, ContainerPath: coordtransport.SigningMountDir, ReadOnly: true})
		roots = append(roots, agentDir)
		// Wins over a workspace variable: the home's git signs through this
		// agent, and commit.gpgsign fails every commit it cannot sign.
		env["SSH_AUTH_SOCK"] = coordtransport.SigningSocketPath
	}
	var worktrees []runtime.Mount
	if run != nil {
		for _, extra := range run.Extras {
			worktrees = append(worktrees, runtime.Mount{HostPath: extra.Worktree, ContainerPath: extra.Path})
			roots = append(roots, filepath.Dir(extra.Worktree))
		}
	}
	mounts := append(slices.Clone(plan.Mounts), worktrees...)
	if validateErr := runtime.ValidateMounts(mounts, runtime.MountPolicy{
		OwnedRoots:        roots,
		WorktreeHostPath:  worktreePath(run),
		WorktreeMountPath: s.cfg.WorktreeMount,
		AllowedNestings:   nestings,
	}); validateErr != nil {
		return nil, validateErr
	}
	// Capped, or a later append to plan.Mounts overwrites plan.Worktrees.
	plan.Mounts, plan.Worktrees = mounts[:len(plan.Mounts):len(plan.Mounts)], mounts[len(plan.Mounts):]
	return plan, nil
}

// workspaceEnvironment is ws as it is now, with its secrets. The caller read
// ws some time ago; the row is read again with the secret file, under the
// lock workspace.environment.set writes both under, so a variable changing
// between plain and secret is never missing from both. Without a secret
// store there is no pair to read, and ws stands.
func (s *Scheduler) workspaceEnvironment(ctx context.Context, ws *domain.Workspace) (*domain.Workspace, map[string]string, error) {
	if s.cfg.Secrets == nil {
		return ws, nil, nil
	}
	s.cfg.Secrets.RLock()
	defer s.cfg.Secrets.RUnlock()
	current, err := s.cfg.Store.GetWorkspace(ctx, ws.ID)
	if err != nil {
		return nil, nil, fmt.Errorf("scheduler: read workspace environment: %w", err)
	}
	secrets, err := s.cfg.Secrets.Get(ws.ID)
	if err != nil {
		return nil, nil, fmt.Errorf("scheduler: read workspace environment: %w", err)
	}
	return current, secrets, nil
}

var cacheEnvironmentKeys = [...]string{"npm_config_cache", "PIP_CACHE_DIR", "UV_CACHE_DIR", "GOCACHE", "GOMODCACHE", "BUN_INSTALL_CACHE_DIR", "YARN_CACHE_FOLDER", "npm_config_store_dir"}

// inheritedEnvironmentKeys are the image settings that win over Aether's
// defaults: the cache variables and the global-install prefixes.
var inheritedEnvironmentKeys = append(slices.Clone(cacheEnvironmentKeys[:]), "NPM_CONFIG_PREFIX", "npm_config_prefix", "CARGO_INSTALL_ROOT")

func explicitEnvironment(profile harness.Profile, ws *domain.Workspace, secrets map[string]string) (map[string]string, error) {
	env := make(map[string]string)
	for _, key := range profile.EnvPassthrough {
		if value, ok := os.LookupEnv(key); ok && value != "" {
			env[key] = value
		}
	}
	if err := harness.MergeEnv(env, profile.PermissionEnv); err != nil {
		return nil, fmt.Errorf("scheduler: apply %s's permission setting: %w", profile.Name, err)
	}
	if ws != nil {
		if err := harness.MergeEnv(env, ws.Environment.Variables); err != nil {
			return nil, fmt.Errorf("scheduler: apply the workspace variables over %s's permission setting: %w", profile.Name, err)
		}
	}
	if err := harness.MergeEnv(env, secrets); err != nil {
		return nil, fmt.Errorf("scheduler: apply the workspace secrets over %s's permission setting: %w", profile.Name, err)
	}
	maps.Copy(env, profile.Env)
	return env, nil
}

// harnessEnvironment is the harness configuration a run's container was
// launched with, for the run's current model.
func (s *Scheduler) harnessEnvironment(ctx context.Context, run *domain.Run, profile harness.Profile, model string) (map[string]string, error) {
	ws, err := s.cfg.Store.GetWorkspace(ctx, run.WorkspaceID)
	if err != nil {
		return nil, fmt.Errorf("scheduler: read workspace environment: %w", err)
	}
	ws, secrets, err := s.workspaceEnvironment(ctx, ws)
	if err != nil {
		return nil, err
	}
	env, err := explicitEnvironment(profile, ws, secrets)
	if err != nil {
		return nil, err
	}
	if err := harness.MergeEnv(env, profile.ModelLaunchEnv(model)); err != nil {
		return nil, fmt.Errorf("scheduler: apply the run's model over the workspace variables: %w", err)
	}
	return env, nil
}

func inheritCacheEnvironment(env, inherited map[string]string) {
	for _, key := range inheritedEnvironmentKeys {
		if value, present := inherited[key]; present {
			if _, explicit := env[key]; !explicit {
				env[key] = value
			}
		}
	}
}

func explicitHomeCachePaths(env map[string]string, home string) []string {
	var paths []string
	for _, key := range cacheEnvironmentKeys {
		value, explicit := env[key]
		if !explicit {
			continue
		}
		// Relative paths, expansion and traversal depend on tool semantics or
		// links we cannot establish here. Preserve rather than guess.
		if !path.IsAbs(value) || strings.ContainsAny(value, "$~") || slices.Contains(strings.Split(value, "/"), "..") {
			return []string{"."}
		}
		value = path.Clean(value)
		if value == home || value == "/" || strings.HasPrefix(home, value+"/") {
			return []string{"."}
		}
		if strings.HasPrefix(value, home+"/") {
			paths = append(paths, strings.TrimPrefix(value, home+"/"))
		}
	}
	return paths
}

// Re-read durable configuration even for never-launched/pre-upgrade homes and
// edits made while idle. Launch metadata alone cannot cover those choices.
func (s *Scheduler) protectConfiguredLegacyCaches(ctx context.Context, memberID domain.MemberID) error {
	member, err := s.cfg.Store.GetMember(ctx, memberID)
	if err != nil {
		return err
	}
	image := member.Image
	if image == "" {
		image = s.cfg.StandardImage
	}
	resolver, ok := s.cfg.Runtime.(imageCacheEnvironmentResolver)
	if !ok || image == "" {
		return errors.New("scheduler: image cache configuration unavailable")
	}
	inherited, err := resolver.ImageCacheEnvironment(ctx, image)
	if err != nil {
		return err
	}
	workspaces, err := s.cfg.Store.ListWorkspaces(ctx)
	if err != nil {
		return err
	}
	// The blank profile covers terminals and member-defined harnesses, whose
	// definitions cannot override environment settings. Registry profiles use
	// the same configured launch resolution as actual runs.
	profiles := []harness.Profile{{}}
	for _, registered := range harness.Profiles() {
		profile, _, err := s.launchProfile(ctx, memberID, memberID, registered.Name)
		if err != nil {
			return err
		}
		profiles = append(profiles, profile)
	}
	workspaces = append(workspaces, nil)
	var paths []string
	for _, profile := range profiles {
		user, err := s.resolveContainerUser(ctx, image, profile)
		if err != nil {
			return err
		}
		home := harness.HomeDir(user)
		if home == "" {
			home = "/root"
		}
		for _, ws := range workspaces {
			var secrets map[string]string
			if ws != nil {
				if ws, secrets, err = s.workspaceEnvironment(ctx, ws); err != nil {
					return err
				}
			}
			env, err := explicitEnvironment(profile, ws, secrets)
			if err != nil {
				return err
			}
			inheritCacheEnvironment(env, inherited)
			paths = append(paths, explicitHomeCachePaths(env, home)...)
		}
	}
	return s.cfg.Homes.ProtectLegacyCaches(memberID, paths)
}

// Explicit tool settings win; HOME, PATH, XDG and installation paths are not caches.
func applyCacheEnvironment(env map[string]string) {
	env["AETHER_CACHE_DIR"] = "/aether-cache"
	for _, setting := range [...]struct{ key, value string }{
		{"npm_config_cache", "/aether-cache/npm"},
		{"PIP_CACHE_DIR", "/aether-cache/pip"},
		{"UV_CACHE_DIR", "/aether-cache/uv"},
		{"GOCACHE", "/aether-cache/go-build"},
		{"GOMODCACHE", "/aether-cache/go-mod"},
		{"BUN_INSTALL_CACHE_DIR", "/aether-cache/bun"},
		{"YARN_CACHE_FOLDER", "/aether-cache/yarn"},
		{"npm_config_store_dir", "/aether-cache/pnpm"},
	} {
		if _, set := env[setting.key]; !set {
			env[setting.key] = setting.value
		}
	}
}

// Global npm and cargo installs go to the member home, which persists, rather
// than the image prefix, which vanishes with the container.
func applyInstallEnvironment(env map[string]string, home string) {
	prefix := path.Join(home, ".local")
	_, npmLower := env["npm_config_prefix"]
	if _, npmUpper := env["NPM_CONFIG_PREFIX"]; !npmLower && !npmUpper {
		env["NPM_CONFIG_PREFIX"] = prefix
	}
	if _, set := env["CARGO_INSTALL_ROOT"]; !set {
		env["CARGO_INSTALL_ROOT"] = prefix
	}
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

// DefaultMode implements sshd.RunController.
func (s *Scheduler) DefaultMode(ctx context.Context, member, account domain.MemberID, harnessName string) (domain.LaunchMode, error) {
	profile, argvs, err := s.launchProfile(ctx, member, account, harnessName)
	if err != nil {
		return "", err
	}
	installed := len(argvs[domain.LaunchACP]) > 0
	if s.cfg.Homes != nil {
		if installed, err = s.adapterInstalled(member, account, profile, argvs); err != nil {
			return "", err
		}
	}
	if installed {
		return domain.LaunchACP, nil
	}
	return domain.LaunchTUI, nil
}

// FallbackLaunchable reports whether a launch in member's home of harnessName
// in mode on account can stand in for one on requested as a usage fallback:
// the owner's login is there to share, an enhanced launch finds its ACP
// server, and the agent runs from member's own installation, or from the
// image when the launch on requested would too. It never borrows the owner's
// installation, whose executables only a launcher who picked that account
// has agreed to run, and never moves a launch that relied on requested's
// installation to an account where the agent is missing, nor onto a login a
// live container holds under another uid:gid. It reads member homes and the
// scheduler's own state only, never the runtime or the vendor.
func (s *Scheduler) FallbackLaunchable(ctx context.Context, member, requested, account domain.MemberID, harnessName string, mode domain.LaunchMode) (bool, error) {
	state, _, err := s.CheckSharedLaunch(ctx, member, account, harnessName)
	if err != nil || state != SharedLaunchable || s.cfg.Homes == nil {
		return err == nil && state == SharedLaunchable, err
	}
	profile, argvs, err := s.launchProfile(ctx, member, account, harnessName)
	if err != nil {
		return false, err
	}
	if mode == domain.LaunchACP {
		installed, adapterErr := s.adapterInstalled(member, account, profile, argvs)
		if adapterErr != nil || !installed {
			return false, adapterErr
		}
	}
	if len(profile.TUIArgs) > 0 {
		installation, installErr := s.cfg.Homes.Installation(member, account, profile.TUIArgs[0], profile.InstallPaths)
		if installErr != nil || (installation != "" && installation != member) {
			return false, installErr
		}
		if installation == "" {
			asked, askedErr := s.cfg.Homes.Installation(member, requested, profile.TUIArgs[0], profile.InstallPaths)
			if askedErr != nil || asked != "" {
				return false, askedErr
			}
		}
	}
	// A live container that holds the login under another uid:gid would
	// refuse the launch once its run exists. The launch's user is known
	// without asking the runtime only from member's live containers, which
	// share one mapping; with none, the launch itself finds any conflict.
	s.mu.Lock()
	defer s.mu.Unlock()
	s.syncRunUserReservationsLocked()
	for reservation := range s.credentialUsers {
		if reservation.home == member && reservation.user != "" {
			return s.reservationConflictLocked(member, account, reservation.user, "") == nil, nil
		}
	}
	return true, nil
}

// pinnedLogins mounts each harness's PinnedLogins in member's own home over
// itself, except where the plan mounts another member's login there. A
// rename over a pinned login fails, so the CLI rewrites it in place: no
// container replaces the file a run on a shared account mounts over (the
// kernel would detach that mount), nor the login member shares. A login path
// that is a directory or cannot be shared is left unpinned rather than
// refused: it cannot be shared either, and refusing would keep the member
// from the terminal that fixes it.
func (s *Scheduler) pinnedLogins(ctx context.Context, member domain.MemberID, homePath, home string, planned []runtime.Mount) ([]runtime.Mount, error) {
	// An engine that cannot mount a subpath cannot mount a shared login
	// either, so there is nothing to keep mounted.
	if checker, ok := s.cfg.Runtime.(subpathAPIChecker); ok {
		err := checker.CheckSubpathAPI(ctx)
		if errors.Is(err, runtime.ErrSubpathAPI) {
			return nil, nil
		}
		if err != nil {
			return nil, fmt.Errorf("scheduler: %w", err)
		}
	}
	var pins []runtime.Mount
	for _, profile := range harness.Profiles() {
		for _, rel := range profile.PinnedLogins {
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
