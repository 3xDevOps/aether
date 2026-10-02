//go:build linux

package scheduler

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/rootfs"
	"github.com/3xDevOps/Aether/internal/runtime"
)

// inodeKey identifies one filesystem object across every hardlink to it.
type inodeKey struct{ dev, ino uint64 }

// applyRunOwnership hands writable host surfaces (a run checkout when
// present, the member's persistent home, and only the login paths an
// account share mounts from the owner's home) to the resolved non-root
// container user before the container is created. Root containers (user == "")
// need no pass: the v1 default stance is a root agent and a root server.
//
// The pass is hardlink-safe: run checkouts are local hardlink clones
// (Wave 1 contract §6.2), so checkout object files share inodes with the
// workspace bare repo, and chowning them through any pathname would mutate
// the shared object every other run reads - including objects an earlier
// run's git moved out of .git/objects (e.g. during repack). Every regular
// file in the bare repo is therefore indexed by device+inode first, and no
// checkout link to a protected inode is ever chowned or chmodded,
// regardless of its current pathname. Everything else - directories
// (including object directories, so the run can add new objects),
// unprotected files, and member homes - is chowned normally. The member's
// live containers use one uid:gid mapping, so this pass cannot flip ownership
// back and forth. A shared login is not held against its owner: an owner's
// container with another uid takes the login back here, and a recipient's
// live run on it loses access.
func (s *Scheduler) applyRunOwnership(ws *domain.Workspace, run *domain.Run, mounts []runtime.Mount, user string) error {
	if user == "" {
		return nil
	}
	uid, gid, err := parseNumericUser(user)
	if err != nil {
		return err
	}
	if run.Worktree != "" {
		protected, err := protectedInodes(filepath.Join(s.cfg.ReposDir, string(ws.ID)+".git"))
		if err != nil {
			return err
		}
		if err := chownTree(run.Worktree, uid, gid, protected); err != nil {
			return err
		}
	}
	for _, m := range mounts {
		if m.ReadOnly {
			continue
		}
		if m.Subpath != "" {
			if err := chownSubpath(m.HostPath, m.Subpath, uid, gid); err != nil {
				return err
			}
			continue
		}
		if err := chownTree(m.HostPath, uid, gid, nil); err != nil {
			return err
		}
	}
	return nil
}

// chownSubpath chowns only what a subpath mount exposes: base/sub, and
// everything beneath it when it is a directory. base belongs to another
// member, whose containers control every entry inside it, so each component
// of sub is pinned without following a symlink.
func chownSubpath(base, sub string, uid, gid int) error {
	home, err := os.OpenRoot(base)
	if err != nil {
		return fmt.Errorf("scheduler: chown %s in %s: %w", sub, base, err)
	}
	defer func() { _ = home.Close() }()
	parent, err := rootfs.OpenRoot(home, path.Dir(sub))
	if err != nil {
		return fmt.Errorf("scheduler: chown %s in %s: %w", sub, base, err)
	}
	defer func() { _ = parent.Close() }()
	leaf := path.Base(sub)
	info, err := parent.Lstat(leaf)
	if err != nil {
		return fmt.Errorf("scheduler: chown %s in %s: %w", sub, base, err)
	}
	switch {
	case info.Mode().IsRegular() && info.Sys().(*syscall.Stat_t).Nlink > 1:
		err = errors.New("has another hard link, which the chown would also hand over")
	case info.Mode().IsRegular():
		err = parent.Lchown(leaf, uid, gid)
	case info.IsDir():
		var dir *os.Root
		dir, err = rootfs.OpenRoot(parent, leaf)
		if err == nil {
			err = chownRoot(dir, uid, gid, nil)
			_ = dir.Close()
		}
	default:
		err = errors.New("neither a regular file nor a directory")
	}
	if err != nil {
		return fmt.Errorf("scheduler: chown %s in %s: %w", sub, base, err)
	}
	return nil
}

// parseNumericUser splits a normalized "uid:gid" (harness.ResolveUser
// output). IDs are capped below 0xFFFFFFFF, the kernel's "no change"
// chown sentinel.
func parseNumericUser(user string) (uid, gid int, err error) {
	u, g, _ := strings.Cut(user, ":")
	parse := func(s string) (int, error) {
		n, parseErr := strconv.ParseUint(s, 10, 32)
		if parseErr != nil || n > 0xFFFFFFFE {
			return 0, fmt.Errorf("scheduler: run user %q: invalid id %q", user, s)
		}
		return int(n), nil
	}
	if uid, err = parse(u); err != nil {
		return 0, 0, err
	}
	if gid, err = parse(g); err != nil {
		return 0, 0, err
	}
	return uid, gid, nil
}

// protectedInodes indexes every regular file in the workspace bare repo by
// device+inode. A missing repo directory yields an empty set: no checkout
// hardlink can point into a repo that does not exist.
func protectedInodes(repoDir string) (map[inodeKey]struct{}, error) {
	protected := make(map[inodeKey]struct{})
	err := filepath.WalkDir(repoDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		st := info.Sys().(*syscall.Stat_t)
		protected[inodeKey{st.Dev, st.Ino}] = struct{}{}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scheduler: index bare repo inodes: %w", err)
	}
	return protected, nil
}

// chownTree chowns dir and everything under it to uid:gid, skipping any
// regular file whose inode is protected (a hardlink into the bare repo).
// The traversal is fd-based via os.Root: a concurrently running container
// sharing a member home cannot swap a directory for a symlink mid-walk and
// redirect the chown onto host files outside dir.
// Symlinks themselves are chowned, never followed.
func chownTree(dir string, uid, gid int, protected map[inodeKey]struct{}) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return fmt.Errorf("scheduler: chown %s: %w", dir, err)
	}
	defer func() { _ = root.Close() }()
	if err := chownRoot(root, uid, gid, protected); err != nil {
		return fmt.Errorf("scheduler: chown %s: %w", dir, err)
	}
	return nil
}

func chownRoot(root *os.Root, uid, gid int, protected map[inodeKey]struct{}) error {
	return fs.WalkDir(root.FS(), ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() && len(protected) > 0 {
			info, ierr := root.Lstat(name)
			if ierr != nil {
				return ierr
			}
			st := info.Sys().(*syscall.Stat_t)
			if _, ok := protected[inodeKey{st.Dev, st.Ino}]; ok {
				return nil
			}
		}
		return root.Lchown(name, uid, gid)
	})
}
