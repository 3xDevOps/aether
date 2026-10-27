package scheduler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/3xDevOps/Aether/internal/coordtransport"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/harness"
	"github.com/3xDevOps/Aether/internal/runtime"
)

// AssistantCwd is where the assistant container mounts its instructions
// directory read-only; the adapter runs there.
const AssistantCwd = "/assistant"

// AssistantInstallPath is where the assistant's adapter is installed: read-only
// in the assistant container, writable only in the container that installs it.
const AssistantInstallPath = "/opt/aether-assistant"

// assistantIdle keeps the container up for execs and exits on SIGTERM.
var assistantIdle = []string{"/bin/sh", "-c", "trap 'exit 0' TERM; while :; do sleep 3600 & wait $!; done"}

// AssistantSocketDir is the host directory mounted read-only at
// coordtransport.AssistantMountDir in the member's assistant container. It
// is not the "sock" directory earlier releases mounted into the member's
// terminal, which a terminal kept running across an upgrade still mounts.
func AssistantSocketDir(root string, member domain.MemberID) string {
	return filepath.Join(root, string(member), "tools")
}

// AssistantHomeDir is the host directory the member's assistant container
// uses as its home. No run or terminal mounts it.
func AssistantHomeDir(root string, member domain.MemberID) string {
	return filepath.Join(root, string(member), "home")
}

// AssistantInstructionsDir is the host directory mounted read-only at
// AssistantCwd.
func AssistantInstructionsDir(root string, member domain.MemberID) string {
	return filepath.Join(root, string(member), "instructions")
}

// AssistantInstallDir is the host directory mounted at AssistantInstallPath
// for agent. Each agent has its own, so switching agents keeps both installs.
func AssistantInstallDir(root string, member domain.MemberID, agent string) string {
	return filepath.Join(root, string(member), "install", agent)
}

// assistantInstalled records the adapter in AssistantInstallDir once its
// install succeeded. Only the server writes it, and no container mounts it.
func assistantInstalled(root string, member domain.MemberID, agent string) string {
	return filepath.Join(root, string(member), "install", agent+".installed")
}

func assistantCreationKey(member domain.MemberID) string {
	return "assistant:" + string(member)
}

func assistantInstallCreationKey(member domain.MemberID) string {
	return "assistant-install:" + string(member)
}

const assistantPath = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

// assistantContainerPlan is the member's assistant container for agent: the
// standard image, the assistant home, the instructions, the installed adapter
// read-only, and from the member's home only agent's login files, each
// mounted alone so a refresh reaches the member's runs too.
func (s *Scheduler) assistantContainerPlan(ctx context.Context, member domain.MemberID, profile harness.Profile) (runtime.Spec, error) {
	if s.cfg.AssistantDir == "" || s.cfg.Homes == nil {
		return runtime.Spec{}, errors.New("scheduler: the assistant needs the server's data directory")
	}
	c := s.coordinationSeam()
	if c == nil {
		return runtime.Spec{}, errors.New("scheduler: the assistant needs the staged server binary")
	}
	image := s.cfg.StandardImage
	if image == "" {
		return runtime.Spec{}, errors.New("scheduler: standard image is required")
	}
	user, err := s.resolveContainerUser(ctx, image, harness.Profile{})
	if err != nil {
		return runtime.Spec{}, fmt.Errorf("scheduler: resolve the assistant's user: %w", err)
	}
	home := harness.HomeDir(user)
	hostHome := AssistantHomeDir(s.cfg.AssistantDir, member)
	instructions := AssistantInstructionsDir(s.cfg.AssistantDir, member)
	sock := AssistantSocketDir(s.cfg.AssistantDir, member)
	install := AssistantInstallDir(s.cfg.AssistantDir, member, profile.Name)
	for _, dir := range []string{hostHome, instructions, sock, install} {
		if err = os.MkdirAll(dir, 0o755); err != nil {
			return runtime.Spec{}, fmt.Errorf("scheduler: create the assistant's directories: %w", err)
		}
	}
	memberHome, err := s.cfg.Homes.Path(member)
	if err != nil {
		return runtime.Spec{}, fmt.Errorf("scheduler: resolve member home: %w", err)
	}
	logins, identities, err := assistantLogins(memberHome, profile)
	if err != nil {
		return runtime.Spec{}, err
	}
	if err = prepareAssistantHome(hostHome, logins, profile.BorrowedState); err != nil {
		return runtime.Spec{}, err
	}
	// A reinstall replaces the directory, which a kept container would still
	// mount.
	installInfo, err := os.Stat(install)
	if err != nil {
		return runtime.Spec{}, fmt.Errorf("scheduler: stat the assistant's install: %w", err)
	}
	installID := identityOf(installInfo)
	identities = append(identities, fmt.Sprintf("install:%d:%d", installID.dev, installID.ino))
	mounts := []runtime.Mount{
		{HostPath: hostHome, ContainerPath: home},
		{HostPath: instructions, ContainerPath: AssistantCwd, ReadOnly: true},
		{HostPath: install, ContainerPath: AssistantInstallPath, ReadOnly: true},
	}
	nestings := make(map[string]string, len(logins))
	for _, rel := range logins {
		target := path.Join(home, rel)
		mounts = append(mounts, runtime.Mount{HostPath: memberHome, Subpath: rel, ContainerPath: target})
		nestings[target] = home
	}
	if err = runtime.ValidateMounts(mounts, runtime.MountPolicy{
		OwnedRoots:      []string{s.cfg.AssistantDir, s.cfg.Homes.Root()},
		AllowedNestings: nestings,
	}); err != nil {
		return runtime.Spec{}, err
	}
	// The current digest is never collected, and a container mounting an
	// older one no longer matches its fingerprint.
	_, bin, err := c.stage()
	if err != nil {
		return runtime.Spec{}, fmt.Errorf("scheduler: stage the assistant's server binary: %w", err)
	}
	coord := []runtime.Mount{
		{HostPath: bin, ContainerPath: coordtransport.CLIPath, ReadOnly: true},
		{HostPath: bin, ContainerPath: coordtransport.BinaryPath, ReadOnly: true},
		{HostPath: sock, ContainerPath: coordtransport.AssistantMountDir, ReadOnly: true},
	}
	if err := checkCoordinationMounts(coord); err != nil {
		return runtime.Spec{}, err
	}
	mounts = append(mounts, coord...)
	if err := s.applyRunOwnership(nil, &domain.Run{}, member, mounts[:1], user); err != nil {
		return runtime.Spec{}, fmt.Errorf("scheduler: hand the assistant home to its user: %w", err)
	}
	env := map[string]string{"HOME": home, "PATH": AssistantInstallPath + "/bin:" + assistantPath}
	spec := runtime.Spec{
		Name:             "aether-assistant-" + string(member),
		Image:            image,
		Env:              env,
		WorkingDir:       AssistantCwd,
		Command:          assistantIdle,
		Mounts:           mounts,
		User:             user,
		CreationKey:      assistantCreationKey(member),
		CPULimit:         s.cfg.RunCPULimit,
		MemoryLimitBytes: s.cfg.RunMemoryBytes,
		PidsLimit:        s.cfg.RunPidsLimit,
	}
	spec.Fingerprint = assistantFingerprint(spec, identities)
	return spec, nil
}

// assistantLogins are profile's login paths that are non-empty regular files
// in memberHome, and an identity of each: a login replaced since the
// container was created must be mounted again. An empty file is a mountpoint
// Aether created, not a login.
func assistantLogins(memberHome string, profile harness.Profile) ([]string, []string, error) {
	paths, err := profile.LoginPaths()
	if err != nil {
		return nil, nil, fmt.Errorf("scheduler: %w", err)
	}
	root, err := os.OpenRoot(memberHome)
	if err != nil {
		return nil, nil, fmt.Errorf("scheduler: open member home: %w", err)
	}
	defer func() { _ = root.Close() }()
	var logins, identities []string
	for _, rel := range paths {
		info, err := root.Lstat(rel)
		if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
			continue
		}
		id := identityOf(info)
		logins = append(logins, rel)
		identities = append(identities, fmt.Sprintf("%s:%d:%d", rel, id.dev, id.ino))
	}
	return logins, identities, nil
}

// prepareAssistantHome creates the login mountpoints and the state that lets
// the agent start signed in. The container writes this home, so every path is
// opened beneath it.
func prepareAssistantHome(hostHome string, logins []string, state map[string]map[string]any) error {
	root, err := os.OpenRoot(hostHome)
	if err != nil {
		return fmt.Errorf("scheduler: open the assistant home: %w", err)
	}
	defer func() { _ = root.Close() }()
	files := make(map[string][]byte, len(logins)+len(state))
	for _, rel := range logins {
		files[rel] = nil
	}
	for rel, keys := range state {
		data, err := json.Marshal(keys)
		if err != nil {
			return fmt.Errorf("scheduler: encode %s: %w", rel, err)
		}
		files[rel] = data
	}
	for rel, data := range files {
		if err := root.MkdirAll(path.Dir(rel), 0o700); err != nil {
			return fmt.Errorf("scheduler: prepare the assistant's %s: %w", rel, err)
		}
		f, err := root.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("scheduler: prepare the assistant's %s: %w", rel, err)
		}
		_, err = f.Write(data)
		if closeErr := f.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			return fmt.Errorf("scheduler: write the assistant's %s: %w", rel, err)
		}
	}
	return nil
}

func assistantFingerprint(spec runtime.Spec, logins []string) string {
	spec.Name, spec.CreationKey, spec.Fingerprint = "", "", ""
	data, err := json.Marshal(struct {
		Spec   runtime.Spec
		Logins []string
	}{spec, logins})
	if err != nil {
		panic(fmt.Sprintf("scheduler: fingerprint assistant spec: %v", err))
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// ensureAssistantContainer starts the member's assistant container for
// profile, keeping the existing one only when it was created from the same
// plan. Callers serialize per member.
func (s *Scheduler) ensureAssistantContainer(ctx context.Context, member domain.MemberID, profile harness.Profile) (runtime.ID, error) {
	spec, err := s.assistantContainerPlan(ctx, member, profile)
	if err != nil {
		return "", err
	}
	cid, err := s.cfg.Runtime.FindByCreationKey(ctx, spec.CreationKey)
	switch {
	case err == nil:
		info, inspectErr := s.cfg.Runtime.Inspect(ctx, cid)
		if inspectErr != nil && !errors.Is(inspectErr, runtime.ErrNotFound) {
			return "", fmt.Errorf("scheduler: inspect the assistant container: %w", inspectErr)
		}
		if inspectErr == nil && info.Fingerprint == spec.Fingerprint {
			running, probeErr := s.terminalContainerRunning(ctx, cid, s.cfg.ExitProbeTimeout)
			if probeErr != nil {
				return "", fmt.Errorf("scheduler: probe the assistant container: %w", probeErr)
			}
			if running {
				return cid, nil
			}
			startErr := s.cfg.Runtime.Start(ctx, cid)
			if startErr == nil {
				return cid, nil
			}
			if ctx.Err() != nil {
				return "", fmt.Errorf("scheduler: start the assistant container: %w", startErr)
			}
		}
		if err = s.destroyTerminalContainer(ctx, cid); err != nil {
			return "", fmt.Errorf("scheduler: replace the assistant container: %w", err)
		}
	case !errors.Is(err, runtime.ErrNotFound):
		return "", fmt.Errorf("scheduler: find the assistant container: %w", err)
	}
	cid, err = s.cfg.Runtime.Create(ctx, spec)
	if err != nil {
		return "", fmt.Errorf("scheduler: create the assistant container: %w", err)
	}
	if err := s.cfg.Runtime.Start(ctx, cid); err != nil {
		return "", errors.Join(fmt.Errorf("scheduler: start the assistant container: %w", err), s.destroyTerminalContainer(ctx, cid))
	}
	return cid, nil
}

func (s *Scheduler) removeAssistantContainer(ctx context.Context, member domain.MemberID) error {
	return s.removeByCreationKey(ctx, assistantCreationKey(member))
}

func (s *Scheduler) removeByCreationKey(ctx context.Context, key string) error {
	cid, err := s.cfg.Runtime.FindByCreationKey(ctx, key)
	if errors.Is(err, runtime.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("scheduler: find container %s: %w", key, err)
	}
	if err := s.destroyTerminalContainer(ctx, cid); err != nil {
		return fmt.Errorf("scheduler: destroy container %s: %w", key, err)
	}
	return nil
}

// DiscardAssistantState destroys the member's assistant container and
// deletes its home: sessions, transcripts, history, todos, file history,
// shell snapshots and logs. The container goes first: deleting a login
// mountpoint under a running container would unmount the login there. The
// home is removed whole, never entry by entry, so no entry the agent made a
// symlink reaches s.cfg.RemoveAll. Callers serialize per member with starting
// it.
func (s *Scheduler) DiscardAssistantState(ctx context.Context, member domain.MemberID) error {
	if err := s.removeAssistantContainer(ctx, member); err != nil {
		return err
	}
	if err := s.cfg.RemoveAll(ctx, AssistantHomeDir(s.cfg.AssistantDir, member)); err != nil {
		return fmt.Errorf("scheduler: discard the assistant home: %w", err)
	}
	return nil
}

// AssistantLoginFound reports whether agent's login is in the member's own
// home by the rule the assistant container mounts it by.
func (s *Scheduler) AssistantLoginFound(member domain.MemberID, agent string) (bool, error) {
	profile, ok := harness.Lookup(agent)
	if !ok || s.cfg.Homes == nil {
		return false, fmt.Errorf("agent %q cannot run the assistant", agent)
	}
	memberHome, err := s.cfg.Homes.Path(member)
	if err != nil {
		return false, fmt.Errorf("scheduler: resolve member home: %w", err)
	}
	logins, _, err := assistantLogins(memberHome, profile)
	return len(logins) > 0, err
}

// RemoveAssistant destroys the member's assistant and install containers and
// deletes their whole assistant directory. Callers serialize per member with
// starting it.
func (s *Scheduler) RemoveAssistant(ctx context.Context, member domain.MemberID) error {
	if err := errors.Join(s.removeAssistantContainer(ctx, member), s.removeByCreationKey(ctx, assistantInstallCreationKey(member))); err != nil {
		return err
	}
	if err := s.cfg.RemoveAll(ctx, filepath.Join(s.cfg.AssistantDir, string(member))); err != nil {
		return fmt.Errorf("scheduler: remove the member's assistant files: %w", err)
	}
	return nil
}

// installAssistant installs profile's pinned adapter into the member's
// AssistantInstallDir unless that version is there, calling installing first.
// The install runs in a container of its own, the only one that mounts the
// directory writable, and starts from an empty directory. Both adapters run
// the agent CLI their package depends on, so the adapter pin moves the CLI
// too.
func (s *Scheduler) installAssistant(ctx context.Context, member domain.MemberID, profile harness.Profile, installing func()) error {
	pkg := profile.ACPInstall.Package + "@" + profile.ACPInstall.Version
	stamp := assistantInstalled(s.cfg.AssistantDir, member, profile.Name)
	got, err := os.ReadFile(stamp)
	if err == nil && string(got) == pkg {
		return nil
	}
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("scheduler: read the assistant's install record: %w", err)
	}
	installing()
	// An install a crashed server left running would write into the
	// directory while it is cleared.
	key := assistantInstallCreationKey(member)
	if err = s.removeByCreationKey(ctx, key); err != nil {
		return fmt.Errorf("scheduler: remove an earlier assistant install: %w", err)
	}
	if err = os.Remove(stamp); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("scheduler: clear the assistant's install record: %w", err)
	}
	dir := AssistantInstallDir(s.cfg.AssistantDir, member, profile.Name)
	if err = s.cfg.RemoveAll(ctx, dir); err != nil {
		return fmt.Errorf("scheduler: clear the assistant's install: %w", err)
	}
	if err = os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("scheduler: create the assistant's install: %w", err)
	}
	user, err := s.resolveContainerUser(ctx, s.cfg.StandardImage, harness.Profile{})
	if err != nil {
		return fmt.Errorf("scheduler: resolve the assistant's user: %w", err)
	}
	mounts := []runtime.Mount{{HostPath: dir, ContainerPath: AssistantInstallPath}}
	if err = s.applyRunOwnership(nil, &domain.Run{}, member, mounts, user); err != nil {
		return fmt.Errorf("scheduler: hand the assistant's install to its user: %w", err)
	}
	cid, err := s.cfg.Runtime.Create(ctx, runtime.Spec{
		Name:             "aether-assistant-install-" + string(member),
		Image:            s.cfg.StandardImage,
		Env:              map[string]string{"HOME": "/tmp", "PATH": assistantPath},
		Command:          assistantIdle,
		Mounts:           mounts,
		User:             user,
		CreationKey:      key,
		CPULimit:         s.cfg.RunCPULimit,
		MemoryLimitBytes: s.cfg.RunMemoryBytes,
		PidsLimit:        s.cfg.RunPidsLimit,
	})
	if err != nil {
		return fmt.Errorf("scheduler: create the assistant's install container: %w", err)
	}
	var code int
	var stdout, stderr string
	if err = s.cfg.Runtime.Start(ctx, cid); err == nil {
		code, stdout, stderr, err = s.cfg.Runtime.Exec(ctx, cid, []string{"npm", "install", "-g", "--prefix", AssistantInstallPath, pkg}, "/tmp")
	}
	// Destroying the container also stops an install a cancelled Exec left
	// running.
	err = errors.Join(err, s.destroyTerminalContainer(ctx, cid))
	if err != nil {
		return fmt.Errorf("scheduler: install %s for the assistant: %w", profile.Label(), err)
	}
	if code != 0 {
		out := strings.TrimSpace(stderr)
		if out == "" {
			out = strings.TrimSpace(stdout)
		}
		if len(out) > 2048 {
			out = out[len(out)-2048:]
		}
		return fmt.Errorf("scheduler: install %s for the assistant exited %d: %s", profile.Label(), code, out)
	}
	if err = os.WriteFile(stamp, []byte(pkg), 0o644); err != nil {
		return fmt.Errorf("scheduler: record the assistant's install: %w", err)
	}
	return nil
}
