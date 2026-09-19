package scheduler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/3xDevOps/Aether/internal/coordtransport"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/harness"
	"github.com/3xDevOps/Aether/internal/runtime"
)

// Coordination assets are server-constructed, read-only bind mounts. Every
// container gets the verified CLI, while coordinated runs additionally get
// the run socket and lifecycle hook assets.
const (
	bridgePrefix                   = "aether-server-"
	legacyBridgePrefix             = "aether-bridge-"
	bridgeMode         fs.FileMode = 0o555
)

var bridgePrefixes = []string{bridgePrefix, legacyBridgePrefix}

// Coordinator is the scheduler's view of the conflict-coordination service
// (*coord.Service): it owns each run's socket directory, which the scheduler
// bind-mounts into coordinated containers and releases once the container is
// gone.
type Coordinator interface {
	Provision(ctx context.Context, run domain.RunID, files map[string][]byte) (string, error)
	WriteCoAuthors(run domain.RunID, trailers []string) error
	Release(run domain.RunID) error
}

// coordination is the attached service plus where staged bridge binaries
// live. Staging remains available when the coordinator is disabled so every
// container gets the version-matched CLI; enabled controls run identity.
type coordination struct {
	svc     Coordinator
	binDir  string
	selfExe string
	enabled bool

	// stageMu serializes stage() against collectStagedBridges().
	stageMu sync.Mutex
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

// UseCoordination attaches the coordinator and staged-binary directory. The
// optional enabled argument is false for the conflict-coordination kill
// switch; binary staging remains active either way.
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

// RequireCoordination is the admission seam for services that need a real
// run identity. It never returns a disabled or unconfigured coordinator.
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

const verificationBridgeRefDirName = "verification-bridges"

type verificationBridgeRef struct {
	CreationKey  string `json:"creation_key"`
	BridgeDigest string `json:"bridge_digest"`
	BridgePath   string `json:"bridge_path"`
}

func (s *Scheduler) verificationBridgeRefDir() string {
	return filepath.Join(s.cfg.StateDir, verificationBridgeRefDirName)
}

func verificationBridgeRefName(creationKey string) string {
	sum := sha256.Sum256([]byte(creationKey))
	return "aether-verification-" + hex.EncodeToString(sum[:]) + ".json"
}

func (s *Scheduler) verificationBridgeRefPath(creationKey string) string {
	return filepath.Join(s.verificationBridgeRefDir(), verificationBridgeRefName(creationKey))
}

func validateVerificationBridgeRef(ref verificationBridgeRef) error {
	if ref.CreationKey == "" || ref.BridgeDigest == "" || ref.BridgePath == "" {
		return errors.New("scheduler: incomplete verification bridge reference")
	}
	if len(ref.BridgeDigest) != sha256.Size*2 {
		return errors.New("scheduler: invalid verification bridge digest")
	}
	if _, err := hex.DecodeString(ref.BridgeDigest); err != nil {
		return fmt.Errorf("scheduler: invalid verification bridge digest: %w", err)
	}
	return nil
}

// writeVerificationBridgeRef durably records the staged CLI before a caller
// is allowed to create the runtime. The creation key is hashed into the file
// name so authenticated-but-opaque keys can never escape StateDir.
func (s *Scheduler) writeVerificationBridgeRef(ref verificationBridgeRef) error {
	if err := validateVerificationBridgeRef(ref); err != nil {
		return err
	}
	dir := s.verificationBridgeRefDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("scheduler: create verification bridge ref dir: %w", err)
	}
	data, err := json.Marshal(ref)
	if err != nil {
		return fmt.Errorf("scheduler: encode verification bridge ref: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".aether-verification-*")
	if err != nil {
		return fmt.Errorf("scheduler: write verification bridge ref: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("scheduler: write verification bridge ref: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("scheduler: sync verification bridge ref: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("scheduler: close verification bridge ref: %w", err)
	}
	if err := os.Rename(tmpName, s.verificationBridgeRefPath(ref.CreationKey)); err != nil {
		return fmt.Errorf("scheduler: install verification bridge ref: %w", err)
	}
	cleanup = false
	if err := fsyncDir(dir); err != nil {
		return err
	}
	if err := fsyncDir(s.cfg.StateDir); err != nil {
		return err
	}
	return nil
}

func (s *Scheduler) readVerificationBridgeRef(creationKey string) (verificationBridgeRef, error) {
	data, err := os.ReadFile(s.verificationBridgeRefPath(creationKey))
	if err != nil {
		return verificationBridgeRef{}, err
	}
	var ref verificationBridgeRef
	if err := json.Unmarshal(data, &ref); err != nil {
		return verificationBridgeRef{}, fmt.Errorf("scheduler: decode verification bridge ref: %w", err)
	}
	if ref.CreationKey != creationKey {
		return verificationBridgeRef{}, errors.New("scheduler: verification bridge ref creation key mismatch")
	}
	if err := validateVerificationBridgeRef(ref); err != nil {
		return verificationBridgeRef{}, err
	}
	return ref, nil
}

// PrepareVerificationRuntime adds only the version-matched, read-only CLI
// mount. It deliberately does not provision a run socket or lifecycle files.
// The durable reference is installed while staging is serialized with the
// collector, before the caller can invoke Runtime.Create.
func (s *Scheduler) PrepareVerificationRuntime(ctx context.Context, spec *runtime.Spec) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if spec == nil {
		return errors.New("scheduler: verification runtime spec is nil")
	}
	if spec.CreationKey == "" {
		return errors.New("scheduler: verification runtime creation key is required")
	}
	c := s.coordinationSeam()
	if c == nil {
		return errors.New("scheduler: verification coordination is unavailable")
	}
	c.stageMu.Lock()
	defer c.stageMu.Unlock()
	digest, bin, err := c.stageLocked()
	if err != nil {
		return fmt.Errorf("stage verification coordination CLI: %w", err)
	}
	for _, mount := range spec.Mounts {
		switch mount.ContainerPath {
		case coordtransport.CLIPath, coordtransport.BinaryPath, coordtransport.MountDir:
			return fmt.Errorf("scheduler: verification spec already uses reserved coordination mount %q", mount.ContainerPath)
		}
	}
	mounts := append([]runtime.Mount(nil), spec.Mounts...)
	mounts = append(mounts, runtime.Mount{
		HostPath: bin, ContainerPath: coordtransport.CLIPath, ReadOnly: true,
	})
	if err := checkCoordinationMounts(mounts[len(mounts)-1:]); err != nil {
		return err
	}
	env := maps.Clone(spec.Env)
	if env == nil {
		env = make(map[string]string)
	}
	ensureCoordinationCLIPath(env)
	if err := s.writeVerificationBridgeRef(verificationBridgeRef{
		CreationKey: spec.CreationKey, BridgeDigest: digest,
		BridgePath: mounts[len(mounts)-1].HostPath,
	}); err != nil {
		return err
	}
	spec.Mounts = mounts
	spec.Env = env
	return nil
}

// ReleaseVerificationRuntime clears the durable staged-CLI reference. The
// integration engine calls this only after it has proved the runtime absent;
// this method intentionally performs no runtime lookup or destruction.
func (s *Scheduler) ReleaseVerificationRuntime(ctx context.Context, creationKey string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if creationKey == "" {
		return errors.New("scheduler: verification runtime creation key is required")
	}
	c := s.coordinationSeam()
	locked := c != nil
	if locked {
		c.stageMu.Lock()
		defer func() {
			if locked {
				c.stageMu.Unlock()
			}
		}()
	}
	if _, err := s.readVerificationBridgeRef(creationKey); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if err := os.Remove(s.verificationBridgeRefPath(creationKey)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("scheduler: remove verification bridge ref: %w", err)
	}
	if err := fsyncDir(s.verificationBridgeRefDir()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := fsyncDir(s.cfg.StateDir); err != nil {
		return err
	}
	if c != nil {
		// Release is durable before collecting, and staging remains serialized
		// until the reference unlink and both directory fsyncs complete.
		c.stageMu.Unlock()
		locked = false
		s.collectStagedBridges()
	}
	return nil
}

// coordinationMounts stages the CLI for every configured container. A run
// gets the socket and lifecycle assets only when coordination is enabled.
func (s *Scheduler) coordinationMounts(ctx context.Context, entry *supervised, run *domain.Run, profile harness.Profile) ([]runtime.Mount, []string, map[string]string, error) {
	c := s.coordinationSeam()
	if c == nil {
		return nil, nil, nil, nil
	}
	if c.enabled && c.svc == nil {
		return nil, nil, nil, errors.New("coordination service is unavailable")
	}
	digest, bin, err := c.stage()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("stage coordination CLI: %w", err)
	}
	cliMount := runtime.Mount{HostPath: bin, ContainerPath: coordtransport.CLIPath, ReadOnly: true}
	if run == nil || !c.enabled {
		mounts := []runtime.Mount{cliMount}
		if err := checkCoordinationMounts(mounts); err != nil {
			return nil, nil, nil, err
		}
		if run != nil && entry != nil {
			s.mu.Lock()
			entry.bridgeDigest, entry.bridgePath = digest, bin
			sc := entry.sidecar()
			s.mu.Unlock()
			if err := s.writeSidecar(sc); err != nil {
				return nil, nil, nil, err
			}
			if err := fsyncDir(s.cfg.StateDir); err != nil {
				return nil, nil, nil, err
			}
		}
		return mounts, nil, nil, nil
	}
	return s.provisionCoordination(ctx, c, entry, run, profile, digest, bin, cliMount)
}

func (s *Scheduler) provisionCoordination(ctx context.Context, c *coordination, entry *supervised, run *domain.Run, profile harness.Profile, digest, bin string, cliMount runtime.Mount) (mounts []runtime.Mount, launchArgs []string, launchEnv map[string]string, err error) {
	files := make(map[string][]byte)
	// Lifecycle status and taskless discovery assets are server-owned and
	// remain available only to coordinated interactive runs. User-supplied
	// MCP configuration is never rewritten by provisioning.
	reporter := harness.ReporterNone
	if run.Mode == domain.LaunchTUI && profile.Reporter != harness.ReporterNone {
		maps.Copy(files, profile.StatusFiles)
		launchArgs = append(launchArgs, profile.StatusLaunchArgs(coordtransport.MountDir)...)
		launchEnv = profile.StatusLaunchEnv(coordtransport.MountDir)
		reporter = profile.Reporter
	}
	if run.Mode == domain.LaunchTUI && run.Task == "" {
		if launchEnv == nil && len(profile.DiscoveryEnv) > 0 {
			launchEnv = make(map[string]string, len(profile.DiscoveryEnv))
		}
		maps.Copy(files, profile.DiscoveryFiles)
		launchArgs = append(launchArgs, profile.DiscoveryLaunchArgs(coordtransport.MountDir)...)
		maps.Copy(launchEnv, profile.DiscoveryLaunchEnv(coordtransport.MountDir))
	}
	dir, err := c.svc.Provision(ctx, run.ID, files)
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
	s.mu.Lock()
	author := entry.gitAuthorEmail
	s.mu.Unlock()
	var trailers []string
	if trailers, err = s.containerCoAuthors(ctx, run, author); err != nil {
		return nil, nil, nil, err
	}
	if err = c.svc.WriteCoAuthors(run.ID, trailers); err != nil {
		return nil, nil, nil, fmt.Errorf("write run co-authors: %w", err)
	}
	mounts = []runtime.Mount{
		{HostPath: bin, ContainerPath: coordtransport.BinaryPath, ReadOnly: true},
		cliMount,
		{HostPath: dir, ContainerPath: coordtransport.MountDir, ReadOnly: true},
	}
	if err = checkCoordinationMounts(mounts); err != nil {
		return nil, nil, nil, err
	}
	s.mu.Lock()
	entry.bridgeDigest, entry.bridgePath, entry.coordDir = digest, bin, dir
	entry.reporter = reporter
	sc := entry.sidecar()
	s.mu.Unlock()
	if err = s.writeSidecar(sc); err != nil {
		return nil, nil, nil, err
	}
	if err = fsyncDir(s.cfg.StateDir); err != nil {
		return nil, nil, nil, err
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
		wantDir := m.ContainerPath == coordtransport.MountDir
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
// copy is reused only after its content and read-only mode are verified.
func (c *coordination) stage() (digest, path string, err error) {
	c.stageMu.Lock()
	defer c.stageMu.Unlock()
	return c.stageLocked()
}

func (c *coordination) stageLocked() (digest, path string, err error) {
	digest, err = hashFile(c.selfExe)
	if err != nil {
		return "", "", fmt.Errorf("hash server binary: %w", err)
	}
	path = filepath.Join(c.binDir, bridgePrefix+digest)
	if staged, herr := hashFile(path); herr == nil && staged == digest {
		if info, serr := os.Lstat(path); serr == nil && info.Mode().IsRegular() && info.Mode().Perm() == bridgeMode.Perm() {
			c.markStaged(digest)
			return digest, path, nil
		}
		if info, serr := os.Lstat(path); serr == nil && info.Mode().IsRegular() {
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
	if staged, herr := hashFile(path); herr != nil || staged != digest {
		_ = os.Remove(path)
		return "", "", fmt.Errorf("staged binary %s does not match its digest", path)
	}
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
	verificationDir := s.verificationBridgeRefDir()
	verificationEntries, err := os.ReadDir(verificationDir)
	if errors.Is(err, os.ErrNotExist) {
		return referenced, nil
	}
	if err != nil {
		return nil, err
	}
	for _, e := range verificationEntries {
		if e.IsDir() || (!strings.HasSuffix(e.Name(), ".json") &&
			!strings.HasPrefix(e.Name(), ".aether-verification-")) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(verificationDir, e.Name()))
		if err != nil {
			return nil, err
		}
		var ref verificationBridgeRef
		if err := json.Unmarshal(data, &ref); err != nil {
			// A partially written reference is an unknown cleanup outcome:
			// retain every staged byte rather than guessing what is live.
			return nil, fmt.Errorf("scheduler: decode verification bridge ref %s: %w", e.Name(), err)
		}
		if err := validateVerificationBridgeRef(ref); err != nil {
			return nil, err
		}
		referenced[ref.BridgeDigest] = true
	}
	return referenced, nil
}
