package scheduler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/3xDevOps/Aether/internal/coordtransport"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/harness"
	"github.com/3xDevOps/Aether/internal/runtime"
)

// Coordination assets are server-constructed, read-only bind mounts. Every
// container gets the verified CLI; runs additionally get their identity socket.
const (
	bridgePrefix                   = "aether-server-"
	legacyBridgePrefix             = "aether-bridge-"
	bridgeMode         fs.FileMode = 0o555
)

var bridgePrefixes = []string{bridgePrefix, legacyBridgePrefix}

// Coordinator owns each run's authenticated socket directory, which the
// scheduler bind-mounts into run containers and releases after container removal.
type Coordinator interface {
	Provision(ctx context.Context, run domain.RunID, files map[string][]byte) (string, error)
	WriteCoAuthors(run domain.RunID, trailers []string) error
	WriteFiles(run domain.RunID, files map[string][]byte) error
	Release(run domain.RunID) error
	WakeIdle(run domain.RunID)
	EnhancedSessionOpened(ctx context.Context, run domain.RunID)
	ProvisionalReport(ctx context.Context, run domain.RunID, since time.Time, message string) error
	// RemindReport prompts an idle session the server drives as a mail
	// wake would, under the same admission.
	RemindReport(ctx context.Context, run domain.RunID, prompt string) error
}

// coordination attaches the run transport and staged binaries. enabled controls
// conflict/mission admission and lifecycle hooks, not run identity or discovery.
type coordination struct {
	svc     Coordinator
	binDir  string
	selfExe string
	enabled bool

	// stageMu serializes stage() against collectStagedBridges() and guards
	// verified.
	stageMu sync.Mutex
	// verified is the identity the staged copy had when its bytes last
	// matched this server's digest.
	verified fileIdentity
	// mu guards staged: the digest of this server's own binary once staged.
	mu     sync.Mutex
	staged string
}

func (c *coordination) markStaged(digest string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.staged = digest
}

func (c *coordination) currentDigest() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.staged
}

// UseCoordination attaches the run transport and staged-binary directory. The
// optional enabled argument controls conflict policy and lifecycle reporting;
// run socket provisioning and discovery remain available either way.
func (s *Scheduler) UseCoordination(svc Coordinator, binDir string, enabled ...bool) {
	active := true
	if len(enabled) > 0 {
		active = enabled[0]
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.coordination = &coordination{
		svc: svc, binDir: binDir, selfExe: s.cfg.ServerBinary, enabled: active,
	}
}

// RequireCoordination is mission admission, not a transport availability check.
// It never returns a disabled or unconfigured conflict coordinator.
func (s *Scheduler) RequireCoordination() (Coordinator, error) {
	c := s.coordinationSeam()
	if c == nil || !c.enabled || c.svc == nil {
		return nil, errors.New("scheduler: coordination is unavailable")
	}
	return c.svc, nil
}

func (s *Scheduler) coordinationSeam() *coordination {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.coordination
}

// coordinationMounts stages the CLI for every configured container and provisions
// authenticated transport for runs independently of conflict policy.
func (s *Scheduler) coordinationMounts(ctx context.Context, entry *supervised, run *domain.Run, profile harness.Profile, native harness.NativeLaunch) ([]runtime.Mount, []string, map[string]string, error) {
	c := s.coordinationSeam()
	if c == nil {
		return nil, nil, nil, nil
	}
	if c.svc == nil {
		return nil, nil, nil, errors.New("coordination service is unavailable")
	}
	digest, bin, err := c.stage()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("stage coordination CLI: %w", err)
	}
	cliMount := runtime.Mount{HostPath: bin, ContainerPath: coordtransport.CLIPath, ReadOnly: true}
	if run == nil {
		mounts := []runtime.Mount{cliMount}
		if err := checkCoordinationMounts(mounts); err != nil {
			return nil, nil, nil, err
		}
		return mounts, nil, nil, nil
	}
	return s.provisionCoordination(ctx, c, entry, run, profile, native, digest, bin, cliMount)
}

type coordinationLaunch struct {
	files    map[string][]byte
	args     []string
	env      map[string]string
	reporter harness.Reporter
}

// newCoordinationLaunch wires lifecycle reporting and, for a launch that
// does not speak ACP, instructions into context the agent keeps through
// compaction.
func newCoordinationLaunch(enabled bool, run *domain.Run, profile harness.Profile, native harness.NativeLaunch, instructions string) (coordinationLaunch, error) {
	l := coordinationLaunch{files: make(map[string][]byte), reporter: harness.ReporterNone}
	maps.Copy(l.files, native.Files)
	// Lifecycle reporting follows conflict policy; instructions only need the
	// run transport. User-supplied MCP configuration is never rewritten.
	if enabled && run.Mode == domain.LaunchTUI && profile.Capabilities.Status != harness.ReporterNone && !native.ReplacesStatus {
		maps.Copy(l.files, profile.StatusFiles)
		l.args = append(l.args, profile.StatusLaunchArgs(coordtransport.MountDir)...)
		l.env = profile.StatusLaunchEnv(coordtransport.MountDir)
		l.reporter = profile.Capabilities.Status
	}
	if native.ReplacesStatus {
		l.reporter = native.Reporter
	}
	if native.IdleWake {
		if l.env == nil {
			l.env = make(map[string]string, 1)
		}
		l.env[coordtransport.WakeEnv] = "1"
	}
	if !run.ACP && instructions != "" {
		l.args = append(l.args, profile.InstructionLaunchArgs(coordtransport.MountDir, instructions)...)
		if profile.InstructionsFile != "" {
			l.files[profile.InstructionsFile] = []byte(instructions + "\n")
		}
		env := map[string]string{coordtransport.RoleCardEnv: "1"}
		maps.Copy(env, profile.InstructionLaunchEnv(coordtransport.MountDir))
		if l.env == nil {
			l.env = make(map[string]string, len(env))
		}
		if err := harness.MergeEnv(l.env, env); err != nil {
			return coordinationLaunch{}, err
		}
	}
	return l, nil
}

func (s *Scheduler) provisionCoordination(ctx context.Context, c *coordination, entry *supervised, run *domain.Run, profile harness.Profile, native harness.NativeLaunch, digest, bin string, cliMount runtime.Mount) (mounts []runtime.Mount, launchArgs []string, launchEnv map[string]string, err error) {
	instructions, err := s.launchInstructions(ctx, run, profile, entry.missionAssigned, native)
	if err != nil {
		return nil, nil, nil, err
	}
	launch, err := newCoordinationLaunch(c.enabled, run, profile, native, instructions)
	if err != nil {
		return nil, nil, nil, err
	}
	reporter := launch.reporter
	launchArgs, launchEnv = launch.args, launch.env
	dir, err := c.svc.Provision(ctx, run.ID, launch.files)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("provision coordination directory: %w", err)
	}
	defer func() {
		if err != nil {
			s.mu.Lock()
			entry.bridgeDigest, entry.bridgePath, entry.coordDir = "", "", ""
			entry.reporter = harness.ReporterNone
			s.mu.Unlock()
			if rerr := c.svc.Release(run.ID); rerr != nil {
				slog.Warn("scheduler: release unmounted coordination directory", "run", run.ID, "error", rerr)
			}
		}
	}()
	mounts = []runtime.Mount{
		{HostPath: bin, ContainerPath: coordtransport.BinaryPath, ReadOnly: true},
		cliMount,
		{HostPath: dir, ContainerPath: coordtransport.MountDir, ReadOnly: true},
	}
	if run.Permission != domain.PermissionRoot {
		mounts = append(mounts, runtime.Mount{HostPath: bin, ContainerPath: coordtransport.SudoPath, ReadOnly: true})
	}
	for _, id := range slices.Sorted(maps.Keys(native.DiscoveryFiles)) {
		mounts = append(mounts, runtime.Mount{
			HostPath:      filepath.Join(dir, native.DiscoveryFiles[id]),
			ContainerPath: filepath.Join(harness.OpenCodeNativeDiscoveryRoot, id+"-"+string(run.ID), "index.js"),
			ReadOnly:      true,
		})
	}
	if err = checkCoordinationMounts(mounts); err != nil {
		return nil, nil, nil, err
	}
	s.mu.Lock()
	entry.bridgeDigest, entry.bridgePath, entry.coordDir = digest, bin, dir
	entry.reporter = reporter
	sc := entry.sidecar()
	s.mu.Unlock()
	// A handoff that committed before coordDir was published skipped its
	// refresh, so the first list is written only after publishing it.
	if err = s.refreshCoAuthorsErr(ctx, run.ID); err != nil {
		return nil, nil, nil, err
	}
	if err = s.writeSidecar(sc); err != nil {
		return nil, nil, nil, err
	}
	if err = fsyncDir(s.cfg.StateDir); err != nil {
		return nil, nil, nil, err
	}
	if !run.ACP && c.enabled {
		// Without a launch route, a task carries the instructions; a
		// taskless launch gets them only from its SessionStart hook and the
		// aether MCP server's instructions.
		channel := "SessionStart hook and MCP server instructions"
		switch {
		case instructions == "" && run.Task != "":
			channel = "first message"
		case instructions == "":
		case profile.InstructionsConfig != "":
			channel = "developer instructions"
		case len(profile.InstructionArgs) > 0:
			channel = "system prompt"
		default:
			channel = "instructions file"
		}
		s.publishTimeline(ctx, run.WorkspaceID, run.ID, "", events.TimelineNote, "run instructions: "+channel)
	}
	return mounts, launchArgs, launchEnv, nil
}
func ensureCoordinationCLIPath(env map[string]string) {
	if env == nil {
		return
	}
	for _, entry := range strings.Split(env["PATH"], ":") {
		if entry == filepath.Dir(coordtransport.CLIPath) {
			return
		}
	}
	env["PATH"] = filepath.Dir(coordtransport.CLIPath) + ":" + env["PATH"]
}

// terminalCoordinationMount stages the CLI for a member terminal without
// provisioning a run socket. The reference is durable before container
// creation, so a restart or collector cannot reclaim a mounted binary.
func (s *Scheduler) terminalCoordinationMount(member domain.MemberID) ([]runtime.Mount, error) {
	c := s.coordinationSeam()
	if c == nil {
		return nil, nil
	}
	digest, bin, err := c.stage()
	if err != nil {
		return nil, fmt.Errorf("stage terminal coordination CLI: %w", err)
	}
	mounts := []runtime.Mount{{
		HostPath: bin, ContainerPath: coordtransport.CLIPath, ReadOnly: true,
	}}
	if err := checkCoordinationMounts(mounts); err != nil {
		return nil, err
	}
	if err := s.writeTerminalSidecar(sidecar{
		TerminalMember: string(member), BridgeDigest: digest, BridgePath: bin,
	}); err != nil {
		return nil, err
	}
	return mounts, nil
}

func (s *Scheduler) updateTerminalCoordination(member domain.MemberID, container runtime.ID) error {
	sc, err := s.readTerminalSidecar(member)
	if err != nil {
		return err
	}
	sc.ContainerID = string(container)
	return s.writeTerminalSidecar(sc)
}

func (s *Scheduler) releaseTerminalCoordination(member domain.MemberID) {
	s.removeTerminalSidecar(member)
	s.collectStagedBridges()
}

// checkCoordinationMounts is the source-side half of mount validation for
// the two Aether surfaces: both sources must exist, resolve without
// surprises, and be the kind of object the container expects, and each is
// pinned to what it resolved to so no symlink can be swapped between the
// check and container creation.
//
// It is the whole check these two need. The target side is two constants
// that runtime.ValidateMounts reserves against every caller mount (see the
// note at the top of this file), and the sources are built by the server
// under <data>, never named by a client or an agent.
func checkCoordinationMounts(mounts []runtime.Mount) error {
	for i, m := range mounts {
		source, err := filepath.EvalSymlinks(m.HostPath)
		if err != nil {
			return fmt.Errorf("coordination mount %q: %w", m.ContainerPath, err)
		}
		info, err := os.Lstat(source)
		if err != nil {
			return fmt.Errorf("coordination mount %q: %w", m.ContainerPath, err)
		}
		wantDir := m.ContainerPath == coordtransport.MountDir || m.ContainerPath == coordtransport.AssistantMountDir
		if wantDir {
			if !info.IsDir() {
				return fmt.Errorf("coordination mount %q: source %q is the wrong kind of file", m.ContainerPath, source)
			}
		} else if !info.Mode().IsRegular() {
			return fmt.Errorf("coordination mount %q: source %q is the wrong kind of file", m.ContainerPath, source)
		}
		mounts[i].HostPath = source
	}
	return nil
}

// stage serializes content-addressed staging against collection. A staged
// copy is reused only after its content and read-only mode are verified, or
// while its identity is the one it had when they last were.
func (c *coordination) stage() (digest, path string, err error) {
	c.stageMu.Lock()
	defer c.stageMu.Unlock()
	return c.stageLocked()
}

func (c *coordination) stageLocked() (digest, path string, err error) {
	// Outside tests selfExe is /proc/self/exe, whose bytes cannot change
	// while this process runs, so a digest once staged is still its digest.
	if digest = c.currentDigest(); digest == "" {
		if digest, err = hashFile(c.selfExe); err != nil {
			return "", "", fmt.Errorf("hash server binary: %w", err)
		}
	}
	path = filepath.Join(c.binDir, bridgePrefix+digest)
	info, serr := os.Lstat(path)
	if serr == nil && c.verified != (fileIdentity{}) && identityOf(info) == c.verified {
		return digest, path, nil
	}
	if serr == nil && info.Mode().IsRegular() {
		if staged, herr := hashFile(path); herr == nil && staged == digest {
			if info.Mode().Perm() == bridgeMode.Perm() {
				c.verified = identityOf(info)
				c.markStaged(digest)
				return digest, path, nil
			}
			if err := os.Chmod(path, bridgeMode); err == nil && fsyncDir(filepath.Dir(path)) == nil {
				c.markStaged(digest)
				return digest, path, nil
			}
		}
	}
	if err := os.MkdirAll(c.binDir, 0o755); err != nil {
		return "", "", fmt.Errorf("create %s: %w", c.binDir, err)
	}
	if err := installBinary(c.selfExe, path); err != nil {
		return "", "", err
	}
	// Never mount what was not verified after it landed: a short copy or a
	// filesystem that lied about the write is caught here, not by an agent.
	info, serr = os.Lstat(path)
	if staged, herr := hashFile(path); serr != nil || herr != nil || staged != digest {
		_ = os.Remove(path)
		return "", "", fmt.Errorf("staged binary %s does not match its digest", path)
	}
	c.verified = identityOf(info)
	c.markStaged(digest)
	return digest, path, nil
}

// installBinary copies src to dst atomically and durably: a temp file in
// the destination directory, fsynced and made read-only before it is
// renamed into place, then the directory fsynced so the name survives a
// crash.
func installBinary(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open %s: %w", src, err)
	}
	defer in.Close() //nolint:errcheck // read-only handle

	dir := filepath.Dir(dst)
	tmp, err := os.CreateTemp(dir, "."+bridgePrefix+"*")
	if err != nil {
		return fmt.Errorf("stage binary into %s: %w", dir, err)
	}
	_, werr := io.Copy(tmp, in)
	if werr == nil {
		werr = tmp.Chmod(bridgeMode)
	}
	if werr == nil {
		werr = tmp.Sync()
	}
	cerr := tmp.Close()
	if werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Rename(tmp.Name(), dst)
	}
	if werr != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("stage binary %s: %w", dst, werr)
	}
	return fsyncDir(dir)
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close() //nolint:errcheck // read-only handle
	sum := sha256.New()
	if _, err := io.Copy(sum, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}

func fsyncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open %s: %w", dir, err)
	}
	err = d.Sync()
	if cerr := d.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("fsync %s: %w", dir, err)
	}
	return nil
}

// releaseCoordination runs after a run's sidecar has been removed, which is
// only ever after its container is gone. Clearing the reference is the
// sidecar's own atomic rename-or-unlink; this makes that durable and only
// then collects binaries no surviving sidecar claims.
func (s *Scheduler) releaseCoordination(run domain.RunID) {
	c := s.coordinationSeam()
	if c == nil {
		return
	}
	// The reference is cleared by the sidecar's own unlink; making that
	// durable is what licenses the collection below, so a failed fsync
	// costs a retained binary rather than one deleted out from under a
	// container that still references it.
	durable := fsyncDir(s.cfg.StateDir)
	if durable != nil {
		slog.Warn("scheduler: fsync run state dir", "run", run, "error", durable)
	}
	if c.svc != nil {
		if err := c.svc.Release(run); err != nil {
			slog.Warn("scheduler: release coordination directory", "run", run, "error", err)
		}
	}
	if durable == nil {
		s.collectStagedBridges()
	}
}

// collectStagedBridges deletes staged binaries no durable reference names.
// Run and terminal sidecars, plus verification creation-key references, are
// retained until their owners explicitly release them after cleanup.
func (s *Scheduler) collectStagedBridges() {
	c := s.coordinationSeam()
	if c == nil {
		return
	}
	// Serialized against stage() so the snapshot below cannot go stale
	// under a concurrent stage - the window between a launch verifying a
	// file and marking it staged is exactly when a deletion would pull the
	// bytes out from under the mount it is about to become.
	c.stageMu.Lock()
	defer c.stageMu.Unlock()
	entries, err := os.ReadDir(c.binDir)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		slog.Warn("scheduler: read staged bridge dir", "error", err)
		return
	}
	referenced, err := s.referencedBridges()
	if err != nil {
		slog.Warn("scheduler: rebuild staged bridge references", "error", err)
		return
	}
	if current := c.currentDigest(); current != "" {
		referenced[current] = true
	}
	removed := false
	for _, e := range entries {
		// A dot-prefixed temp file is an install that crashed mid-copy: a
		// live one cannot be seen here because stage() holds stageMu for
		// the whole install. Recognize both the current and legacy prefixes
		// so an upgrade cannot strand either kind of staged asset.
		orphanedTemp := false
		staged := false
		digest := ""
		for _, prefix := range bridgePrefixes {
			if strings.HasPrefix(e.Name(), "."+prefix) {
				orphanedTemp = true
				break
			}
			if candidate, ok := strings.CutPrefix(e.Name(), prefix); ok {
				digest = candidate
				staged = true
				break
			}
		}
		if !orphanedTemp && (!staged || referenced[digest]) {
			continue
		}
		if err := os.Remove(filepath.Join(c.binDir, e.Name())); err != nil {
			slog.Warn("scheduler: remove staged bridge", "file", e.Name(), "error", err)
			continue
		}
		removed = true
	}
	if removed {
		if err := fsyncDir(c.binDir); err != nil {
			slog.Warn("scheduler: fsync staged bridge dir", "error", err)
		}
	}
}

// referencedBridges is the set of staged digests the surviving run and
// member-terminal sidecars name. A sidecar that cannot be read counts as
// referencing everything - the caller aborts rather than collecting on
// partial information.
func (s *Scheduler) referencedBridges() (map[string]bool, error) {
	entries, err := os.ReadDir(s.cfg.StateDir)
	if err != nil {
		return nil, err
	}
	referenced := make(map[string]bool)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		run := domain.RunID(strings.TrimSuffix(e.Name(), ".json"))
		sc, readErr := s.readSidecar(run)
		if readErr != nil {
			if errors.Is(readErr, os.ErrNotExist) {
				continue
			}
			return nil, readErr
		}
		if sc.BridgeDigest != "" {
			referenced[sc.BridgeDigest] = true
		}
	}
	terminalDir := s.terminalSidecarDir()
	terminalEntries, err := os.ReadDir(terminalDir)
	if !errors.Is(err, os.ErrNotExist) {
		if err != nil {
			return nil, err
		}
		for _, e := range terminalEntries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			member := domain.MemberID(strings.TrimSuffix(e.Name(), ".json"))
			sc, readErr := s.readTerminalSidecar(member)
			if readErr != nil {
				if errors.Is(readErr, os.ErrNotExist) {
					continue
				}
				return nil, readErr
			}
			if sc.BridgeDigest != "" {
				referenced[sc.BridgeDigest] = true
			}
		}
	}
	return referenced, nil
}
