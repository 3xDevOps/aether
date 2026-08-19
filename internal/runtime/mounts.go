package runtime

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Reserved container path prefixes: Aether-owned runtime surfaces that
// caller-controlled mounts must never shadow.
const (
	reservedRunPath = "/run/aether"
	reservedOptPath = "/opt/aether"
)

// dockerSocketPaths are the host spellings of the Docker control socket.
// A mount source must never be the socket or a directory containing it.
// Variable (not const) so tests can substitute a temp path.
var dockerSocketPaths = []string{"/var/run/docker.sock", "/run/docker.sock"}

// MountPolicy is the caller's side of mount validation: which host roots
// sources may live under, the run's worktree bind to collide against, and
// the approved target nestings.
type MountPolicy struct {
	// OwnedRoots are the Aether-owned host directories every mount source
	// must resolve under (e.g. <data>/homes). Empty rejects all mounts.
	OwnedRoots []string
	// WorktreeHostPath and WorktreeMountPath are the run's checkout bind
	// (Spec.WorktreeHostPath/WorktreeMountPath); empty when the spec has
	// no worktree.
	WorktreeHostPath  string
	WorktreeMountPath string
	// AllowedNestings maps an approved child container path to its parent
	// container path: the single registry-credential-under-profile
	// exception. The caller vouches for the pair's provenance (both paths
	// come from the harness registry, never from user input); the child
	// must appear after its parent in the mount list.
	AllowedNestings map[string]string
}

// ValidateMounts checks every mount against policy and reports the first
// violation. It rejects sources that do not resolve (after symlinks) under
// an Aether-owned root, sources that are or contain the Docker control
// socket under any alias, sources that are neither directories nor regular
// files, duplicate targets, nested targets except the approved
// child-after-parent pairs, targets under the reserved /run/aether and
// /opt/aether prefixes, root targets, collisions with the worktree bind on
// either side, and read-only sources containing another mount's source
// (Docker's per-bind read-only flag is not recursive and no recursive
// fallback is used).
//
// Validation canonicalizes: on success each mount's HostPath is rewritten
// in place to its fully resolved source, so the bind Docker later
// receives is exactly the path that was checked (no symlink swap between
// validation and container creation).
//
// Security note: Docker exposes no per-bind nosuid/nodev controls, and
// construction deliberately uses only mount.Mount.ReadOnly and rprivate
// propagation. Deployments where container root is not trusted with the
// host must place <data> on a filesystem mounted with nosuid,nodev so a
// root agent cannot plant setuid binaries through a writable bind.
func ValidateMounts(mounts []Mount, policy MountPolicy) error {
	if len(mounts) == 0 {
		return nil
	}
	roots := make([]string, 0, len(policy.OwnedRoots))
	for _, r := range policy.OwnedRoots {
		roots = append(roots, resolveBestEffort(r))
	}
	worktreeHost := ""
	if policy.WorktreeHostPath != "" {
		worktreeHost = resolveBestEffort(policy.WorktreeHostPath)
	}
	worktreeTarget := ""
	if policy.WorktreeMountPath != "" {
		worktreeTarget = path.Clean(policy.WorktreeMountPath)
	}

	targets := make([]string, len(mounts))
	sources := make([]string, len(mounts))
	for i, m := range mounts {
		target := path.Clean(m.ContainerPath)
		if !path.IsAbs(target) {
			return fmt.Errorf("runtime: mount %d: target %q must be absolute", i, m.ContainerPath)
		}
		if target == "/" {
			return fmt.Errorf("runtime: mount %d: target must not be the container root", i)
		}
		for _, reserved := range []string{reservedRunPath, reservedOptPath} {
			if target == reserved || underSlash(target, reserved) {
				return fmt.Errorf("runtime: mount %d: target %q is reserved for aether", i, target)
			}
		}
		if worktreeTarget != "" && (target == worktreeTarget ||
			underSlash(target, worktreeTarget) || underSlash(worktreeTarget, target)) {
			return fmt.Errorf("runtime: mount %d: target %q collides with the worktree mount %q", i, target, worktreeTarget)
		}

		if !filepath.IsAbs(m.HostPath) {
			return fmt.Errorf("runtime: mount %d: source %q must be absolute", i, m.HostPath)
		}
		source, err := filepath.EvalSymlinks(m.HostPath)
		if err != nil {
			return fmt.Errorf("runtime: mount %d: source %q: %w", i, m.HostPath, err)
		}
		owned := false
		for _, root := range roots {
			if withinHost(source, root) {
				owned = true
				break
			}
		}
		if !owned {
			return fmt.Errorf("runtime: mount %d: source %q resolves to %q, outside every aether-owned root", i, m.HostPath, source)
		}
		for _, sock := range dockerSocketPaths {
			resolvedSock := resolveBestEffort(sock)
			if source == resolvedSock || source == filepath.Clean(sock) ||
				withinHost(resolvedSock, source) || withinHost(filepath.Clean(sock), source) {
				return fmt.Errorf("runtime: mount %d: source %q exposes the docker socket", i, m.HostPath)
			}
		}
		info, err := os.Lstat(source)
		if err != nil {
			return fmt.Errorf("runtime: mount %d: source %q: %w", i, m.HostPath, err)
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return fmt.Errorf("runtime: mount %d: source %q is neither a directory nor a regular file", i, m.HostPath)
		}
		if worktreeHost != "" && (source == worktreeHost ||
			withinHost(source, worktreeHost) || withinHost(worktreeHost, source)) {
			return fmt.Errorf("runtime: mount %d: source %q collides with the worktree checkout %q", i, m.HostPath, policy.WorktreeHostPath)
		}

		for j := range i {
			if targets[j] == target {
				return fmt.Errorf("runtime: mounts %d and %d: duplicate target %q", j, i, target)
			}
			if underSlash(target, targets[j]) {
				if policy.AllowedNestings[target] != targets[j] {
					return fmt.Errorf("runtime: mount %d: target %q nests under mount %d target %q without approval", i, target, j, targets[j])
				}
				if info, err := os.Lstat(sources[j]); err != nil || !info.IsDir() {
					return fmt.Errorf("runtime: mount %d: target %q nests under mount %d whose source %q is not a directory", i, target, j, mounts[j].HostPath)
				}
			}
			if underSlash(targets[j], target) {
				return fmt.Errorf("runtime: mount %d: target %q contains mount %d target %q; a nested mount must be ordered after its parent", i, target, j, targets[j])
			}
			if mounts[j].ReadOnly && withinHost(source, sources[j]) && source != sources[j] {
				return fmt.Errorf("runtime: mount %d: source %q nests inside read-only mount %d source %q", i, m.HostPath, j, mounts[j].HostPath)
			}
			if m.ReadOnly && withinHost(sources[j], source) && source != sources[j] {
				return fmt.Errorf("runtime: mount %d: read-only source %q contains mount %d source %q", i, m.HostPath, j, mounts[j].HostPath)
			}
		}
		targets[i], sources[i] = target, source
		mounts[i].HostPath = source
	}
	return nil
}

// resolveBestEffort resolves symlinks where the path exists and falls back
// to lexical cleaning where it does not.
func resolveBestEffort(p string) string {
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}
	return filepath.Clean(p)
}

// underSlash reports whether child is strictly beneath parent, both being
// cleaned absolute slash-separated container paths.
func underSlash(child, parent string) bool {
	return strings.HasPrefix(child, parent+"/")
}

// withinHost reports whether child equals parent or lies beneath it, both
// being resolved host paths.
func withinHost(child, parent string) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}
