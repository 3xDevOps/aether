package scheduler

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"

	"github.com/3xDevOps/Aether/internal/coordtransport"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/harness"
	"github.com/3xDevOps/Aether/internal/runtime"
)

// AssistantCwd is where the assistant container mounts its instructions
// directory read-only; the adapter runs there.
const AssistantCwd = "/assistant"

// AssistantInstallPath holds each agent's install at <agent>: read-only in the
// assistant container, writable only in the container that installs it.
const AssistantInstallPath = "/opt/aether-assistant"

// assistantPolicyPaths are where Claude Code and Codex read system-wide
// managed settings. The assistant container mounts an empty server directory
// over each, read-only, so neither the image nor the agent supplies one.
var assistantPolicyPaths = []string{"/etc/claude-code", "/etc/codex"}

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

// AssistantInstallDir is agent's install under the host directory mounted at
// AssistantInstallPath. Each agent has its own, so switching agents keeps
// both installs.
func AssistantInstallDir(root string, member domain.MemberID, agent string) string {
	return filepath.Join(root, string(member), "install", agent)
}

func assistantPolicyDir(root string, member domain.MemberID) string {
	return filepath.Join(root, string(member), "policy")
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

// assistantAgents are the agents the assistant runs, each with its own bin
// directory in the assistant container's PATH.
var assistantAgents = []string{"claude", "codex"}

// assistantPins holds the dependencies an adapter declares by range to the
// version the assistant runs: codex-acp 2.1.1 accepts any @openai/codex
// ^0.159.1.
var assistantPins = map[string]map[string]string{"codex": {"@openai/codex": "0.159.3"}}

func (s *Scheduler) assistantLock(member domain.MemberID) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.assistantLocks == nil {
		s.assistantLocks = make(map[domain.MemberID]*sync.Mutex)
	}
	lock := s.assistantLocks[member]
	if lock == nil {
		lock = &sync.Mutex{}
		s.assistantLocks[member] = lock
	}
	return lock
}

// assistantContainerPlan is the member's assistant container: the standard
// image, the assistant home, and read-only the instructions, every agent's
// install and empty managed-settings directories. It mounts nothing from the
// member's home, which their runs write.
func (s *Scheduler) assistantContainerPlan(ctx context.Context, member domain.MemberID) (runtime.Spec, error) {
	if s.cfg.AssistantDir == "" {
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
	install := filepath.Join(s.cfg.AssistantDir, string(member), "install")
	policy := assistantPolicyDir(s.cfg.AssistantDir, member)
	for _, dir := range []string{hostHome, instructions, sock, install, policy} {
		if err = os.MkdirAll(dir, 0o755); err != nil {
			return runtime.Spec{}, fmt.Errorf("scheduler: create the assistant's directories: %w", err)
		}
	}
	mounts := []runtime.Mount{
		{HostPath: hostHome, ContainerPath: home},
		{HostPath: instructions, ContainerPath: AssistantCwd, ReadOnly: true},
		{HostPath: install, ContainerPath: AssistantInstallPath, ReadOnly: true},
	}
	for _, p := range assistantPolicyPaths {
		mounts = append(mounts, runtime.Mount{HostPath: policy, ContainerPath: p, ReadOnly: true})
	}
	if err = runtime.ValidateMounts(mounts, runtime.MountPolicy{OwnedRoots: []string{s.cfg.AssistantDir}}); err != nil {
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
	bins := make([]string, 0, len(assistantAgents)+1)
	for _, agent := range assistantAgents {
		bins = append(bins, path.Join(AssistantInstallPath, agent, "node_modules", ".bin"))
	}
	spec := runtime.Spec{
		Name:             "aether-assistant-" + string(member),
		Image:            image,
		Env:              map[string]string{"HOME": home, "PATH": strings.Join(append(bins, assistantPath), ":")},
		WorkingDir:       AssistantCwd,
		Command:          assistantIdle,
		Mounts:           mounts,
		User:             user,
		CreationKey:      assistantCreationKey(member),
		CPULimit:         s.cfg.RunCPULimit,
		MemoryLimitBytes: s.cfg.RunMemoryBytes,
		PidsLimit:        s.cfg.RunPidsLimit,
	}
	spec.Fingerprint = assistantFingerprint(spec)
	return spec, nil
}

func assistantFingerprint(spec runtime.Spec) string {
	spec.Name, spec.CreationKey, spec.Fingerprint = "", "", ""
	data, err := json.Marshal(spec)
	if err != nil {
		panic(fmt.Sprintf("scheduler: fingerprint assistant spec: %v", err))
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// ensureAssistantContainer starts the member's assistant container, keeping
// the existing one only when it was created from the same plan. Callers hold
// assistantLock.
func (s *Scheduler) ensureAssistantContainer(ctx context.Context, member domain.MemberID) (runtime.ID, error) {
	spec, err := s.assistantContainerPlan(ctx, member)
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
// deletes everything in its home but the assistant's sign-ins: sessions,
// transcripts, history, todos, file history, shell snapshots and logs.
func (s *Scheduler) DiscardAssistantState(ctx context.Context, member domain.MemberID) error {
	lock := s.assistantLock(member)
	lock.Lock()
	defer lock.Unlock()
	if err := s.removeAssistantContainer(ctx, member); err != nil {
		return err
	}
	return s.sweepAssistantHome(ctx, AssistantHomeDir(s.cfg.AssistantDir, member), false)
}

// RemoveAssistant destroys the member's assistant and install containers and
// deletes their whole assistant directory.
func (s *Scheduler) RemoveAssistant(ctx context.Context, member domain.MemberID) error {
	lock := s.assistantLock(member)
	lock.Lock()
	defer lock.Unlock()
	if err := errors.Join(s.removeAssistantContainer(ctx, member), s.removeByCreationKey(ctx, assistantInstallCreationKey(member))); err != nil {
		return err
	}
	if err := s.cfg.RemoveAll(ctx, filepath.Join(s.cfg.AssistantDir, string(member))); err != nil {
		return fmt.Errorf("scheduler: remove the member's assistant files: %w", err)
	}
	return nil
}

// installAssistant installs profile's pinned adapter, with the dependencies
// assistantPins holds, into the member's AssistantInstallDir unless that
// package.json is installed there, calling installing first. The install runs
// in a container of its own, the only one that mounts the directory
// writable, and starts from an empty directory. Callers hold assistantLock.
func (s *Scheduler) installAssistant(ctx context.Context, member domain.MemberID, profile harness.Profile, installing func()) error {
	deps := map[string]string{profile.ACPInstall.Package: profile.ACPInstall.Version}
	maps.Copy(deps, assistantPins[profile.Name])
	manifest, err := json.Marshal(map[string]any{"private": true, "dependencies": deps})
	if err != nil {
		return fmt.Errorf("scheduler: encode the assistant's package.json: %w", err)
	}
	stamp := assistantInstalled(s.cfg.AssistantDir, member, profile.Name)
	got, err := os.ReadFile(stamp)
	if err == nil && bytes.Equal(got, manifest) {
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
	if err = os.WriteFile(filepath.Join(dir, "package.json"), manifest, 0o644); err != nil {
		return fmt.Errorf("scheduler: write the assistant's package.json: %w", err)
	}
	user, err := s.resolveContainerUser(ctx, s.cfg.StandardImage, harness.Profile{})
	if err != nil {
		return fmt.Errorf("scheduler: resolve the assistant's user: %w", err)
	}
	target := path.Join(AssistantInstallPath, profile.Name)
	mounts := []runtime.Mount{{HostPath: dir, ContainerPath: target}}
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
		code, stdout, stderr, err = s.cfg.Runtime.Exec(ctx, cid, []string{"npm", "install", "--no-audit", "--no-fund", "--prefix", target}, "/tmp")
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
	if err = os.WriteFile(stamp, manifest, 0o644); err != nil {
		return fmt.Errorf("scheduler: record the assistant's install: %w", err)
	}
	return nil
}
