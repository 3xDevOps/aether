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
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"

	"github.com/3xDevOps/Aether/internal/coord"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/harness"
	"github.com/3xDevOps/Aether/internal/mcpbridge"
	"github.com/3xDevOps/Aether/internal/runtime"
)

// Coordination assets are the two read-only binds that let an agent talk to
// its overlapping peers: the server's own binary, which serves the MCP
// bridge inside the container, and the run's coordination directory, which
// carries the socket the bridge dials.
//
// These two mounts deliberately do not go through runtime.ValidateMounts,
// and routing them through it would be a mistake, not a fix. That validator
// rejects any target at or under /opt/aether and /run/aether outright,
// because those prefixes are reserved for exactly these surfaces: the
// unconditional rejection is what guarantees no credential home, synced
// profile, or any other caller-supplied mount can ever shadow the bridge or
// the coordination socket. Relaxing it into an opt-in would trade that
// guarantee for uniformity nothing here needs.
//
// So the reserved rule protects the targets, and the two mounts are built
// below from server-constructed paths - never from anything a client or an
// agent names - and appended after the caller's mounts have been validated.
// What is left is the source side, which checkCoordinationMounts covers.
//
// The whole thing is fail-closed: a binary that cannot be staged and
// verified is never mounted. The run still launches - coordination is
// advisory, and an agent without the bridge still gets the overlap notice -
// but the failure is recorded on the run's timeline rather than swallowed.

// selfExe is the running server binary. /proc/self/exe rather than
// os.Args[0] because it survives a PATH change, a relative launch, and an
// upgrade that replaced the file underneath the process. Overridden in
// tests.
var selfExe = "/proc/self/exe"

// bridgePrefix names staged binaries. The content hash is the whole
// identity: a container provisioned against one build keeps its own copy
// after the server upgrades, and two servers staging the same build share.
const bridgePrefix = "aether-server-"

// bridgeMode is read-execute for everyone and writable by no one. The
// staged file is also mounted read-only, so this is defence in depth
// against anything on the host reaching it through <data>.
const bridgeMode fs.FileMode = 0o555

// bridgeSubcommand is the hidden subcommand the staged binary serves the
// stdio MCP bridge with (cmd/aether-server).
const bridgeSubcommand = "mcp"

// mcpConfigPath is where the harness config written into a run's
// coordination directory appears inside its container. Only a harness
// whose profile registers MCP is ever pointed at it.
var mcpConfigPath = path.Join(mcpbridge.MountDir, coord.ConfigName)

// Coordinator is the scheduler's view of the conflict-coordination service
// (*coord.Service): it owns each run's socket directory, which the
// scheduler bind-mounts into the container and releases once the container
// is gone.
type Coordinator interface {
	Provision(ctx context.Context, run domain.RunID, config []byte) (string, error)
	Release(run domain.RunID) error
}

// coordination is the attached service plus where staged bridge binaries
// live. A nil coordination is the kill switch: nothing is staged, nothing
// is provisioned, nothing is mounted, and assets an earlier process left
// behind are retained untouched so a container still holding them simply
// finds them inert.
type coordination struct {
	svc    Coordinator
	binDir string

	// stageMu serializes stage() against collectStagedBridges(). Without
	// it a collector whose snapshot predates a concurrent stage could
	// delete the bytes that stage just verified and is about to mount. It
	// also means any temp file a collection sees is a crash leftover, never
	// a copy in flight, so the collector can reclaim those too.
	stageMu sync.Mutex

	// mu guards staged: the digest of this server's own binary once it has
	// been staged. It is retained across collections so an idle server does
	// not re-copy itself for every launch, and so a launch that has staged
	// but not yet written its sidecar cannot have the bytes it is about to
	// mount collected underneath it by a run finishing concurrently.
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

// UseCoordination attaches the conflict-coordination service and the
// directory staged bridge binaries live in (<data>/runtime/bin). It is
// called once during assembly, before any run is launched; leaving it
// unset is how the kill switch keeps coordination out of new containers.
func (s *Scheduler) UseCoordination(svc Coordinator, binDir string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.coordination = &coordination{svc: svc, binDir: binDir}
}

func (s *Scheduler) coordinationSeam() *coordination {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.coordination
}

// coordinationMounts stages the bridge, provisions the run's coordination
// directory, records both in the run's sidecar - all before the container
// exists - and returns the two read-only mounts plus the launch arguments
// registering the bridge with the harness. A failure anywhere leaves the
// run with no coordination and says so on its timeline; it never returns a
// mount it could not verify.
func (s *Scheduler) coordinationMounts(ctx context.Context, entry *supervised, run *domain.Run, profile harness.Profile) ([]runtime.Mount, []string) {
	c := s.coordinationSeam()
	if c == nil {
		return nil, nil
	}
	mounts, mcpArgs, err := s.provisionCoordination(ctx, c, entry, run, profile)
	if err == nil {
		return mounts, mcpArgs
	}
	slog.Warn("scheduler: coordination assets unavailable", "run", run.ID, "error", err)
	s.publishTimeline(ctx, run.WorkspaceID, run.ID, run.MemberID, events.TimelineNote,
		"coordination unavailable for this run: "+err.Error())
	return nil, nil
}

func (s *Scheduler) provisionCoordination(ctx context.Context, c *coordination, entry *supervised, run *domain.Run, profile harness.Profile) (mounts []runtime.Mount, mcpArgs []string, err error) {
	digest, bin, err := c.stage()
	if err != nil {
		return nil, nil, err
	}
	// The harness registration: a profile that can load an MCP config gets
	// one written into its coordination directory, naming the staged bridge,
	// and the flag pointing at it appended to its launch command. A harness
	// without registration is provisioned all the same and degrades to the
	// overlap notice, which is the information a human at that terminal
	// would want anyway.
	var config []byte
	if mcpArgs = profile.MCPArgs(mcpConfigPath); len(mcpArgs) > 0 {
		if config, err = harness.MCPConfig(mcpbridge.ServerName, mcpbridge.BinaryPath, bridgeSubcommand); err != nil {
			return nil, nil, err
		}
	}
	var dir string
	dir, err = c.svc.Provision(ctx, run.ID, config)
	if err != nil {
		return nil, nil, fmt.Errorf("provision coordination directory: %w", err)
	}
	// Past this point the run owns a live socket, so anything that stops it
	// from being mounted has to hand it straight back rather than leave a
	// listener nothing can reach - and any claims already recorded on the
	// entry have to go with it, or the next sidecar write would durably
	// name assets the run does not hold.
	defer func() {
		if err != nil {
			s.mu.Lock()
			entry.bridgeDigest, entry.bridgePath, entry.coordDir = "", "", ""
			s.mu.Unlock()
			if rerr := c.svc.Release(run.ID); rerr != nil {
				slog.Warn("scheduler: release unmounted coordination directory", "run", run.ID, "error", rerr)
			}
		}
	}()
	mounts = []runtime.Mount{
		{HostPath: bin, ContainerPath: mcpbridge.BinaryPath, ReadOnly: true},
		{HostPath: dir, ContainerPath: mcpbridge.MountDir, ReadOnly: true},
	}
	if err = checkCoordinationMounts(mounts); err != nil {
		return nil, nil, err
	}
	s.mu.Lock()
	entry.bridgeDigest, entry.bridgePath, entry.coordDir = digest, bin, dir
	sc := entry.sidecar()
	s.mu.Unlock()
	// The reference has to be durable before the container exists, or a
	// crash in between would leave a container holding assets nothing
	// claims and the next collection would delete them underneath it.
	// writeSidecar fsyncs the file and renames it into place; the directory
	// needs its own fsync for the *name* to survive power loss, or recovery
	// could find the contents on disk under no name at all - which is
	// exactly the unreferenced-digest case the collector acts on.
	if err = s.writeSidecar(sc); err != nil {
		return nil, nil, err
	}
	if err = fsyncDir(s.cfg.StateDir); err != nil {
		return nil, nil, err
	}
	return mounts, mcpArgs, nil
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
		wantDir := m.ContainerPath == mcpbridge.MountDir
		if info.IsDir() != wantDir {
			return fmt.Errorf("coordination mount %q: source %q is the wrong kind of file", m.ContainerPath, source)
		}
		mounts[i].HostPath = source
	}
	return nil
}

// stage installs this server's binary under its own content hash and
// returns the digest and path. A staged copy that already hashes to the
// digest is reused; anything else - missing, truncated, or a file whose
// content no longer matches its name - is replaced atomically.
func (c *coordination) stage() (digest, path string, err error) {
	c.stageMu.Lock()
	defer c.stageMu.Unlock()
	digest, err = hashFile(selfExe)
	if err != nil {
		return "", "", fmt.Errorf("hash server binary: %w", err)
	}
	path = filepath.Join(c.binDir, bridgePrefix+digest)
	if staged, herr := hashFile(path); herr == nil && staged == digest {
		c.markStaged(digest)
		return digest, path, nil
	}
	if err := os.MkdirAll(c.binDir, 0o755); err != nil {
		return "", "", fmt.Errorf("create %s: %w", c.binDir, err)
	}
	if err := installBinary(selfExe, path); err != nil {
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
	if err := c.svc.Release(run); err != nil {
		slog.Warn("scheduler: release coordination directory", "run", run, "error", err)
	}
	if durable == nil {
		s.collectStagedBridges()
	}
}

// collectStagedBridges deletes staged binaries no run references any more.
// The references are the sidecars themselves, so this doubles as recovery:
// a server that just restarted rebuilds the live set from the sidecars that
// survived, and a build referenced by a container it will re-attach to is
// retained exactly because that container's sidecar is still there.
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
		// the whole install.
		orphanedTemp := strings.HasPrefix(e.Name(), "."+bridgePrefix)
		digest, ok := strings.CutPrefix(e.Name(), bridgePrefix)
		if !orphanedTemp && (!ok || referenced[digest]) {
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

// referencedBridges is the set of staged digests the surviving sidecars
// name. A sidecar that cannot be read counts as referencing everything -
// the caller aborts rather than collecting on partial information.
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
		sc, err := s.readSidecar(run)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, err
		}
		if sc.BridgeDigest != "" {
			referenced[sc.BridgeDigest] = true
		}
	}
	return referenced, nil
}
