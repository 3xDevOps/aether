package memberhome

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/rootfs"
)

// LoginPathIsDir reports whether the home-relative login path rel in
// member's home is a directory (true) or a regular file (false). A missing
// path, or an empty file, returns an error matching fs.ErrNotExist: an empty
// file is a mountpoint Aether created, not a login. A symlink at any
// component, a file with another hard link, or a final entry of any other
// type makes the path unshareable. The runtime enforces the symlink rule when
// it mounts the path; this is the early refusal.
func (m *Manager) LoginPathIsDir(member domain.MemberID, rel string) (bool, error) {
	home, err := m.openHome(member)
	if err != nil {
		return false, err
	}
	defer func() { _ = home.Close() }()
	parent, leaf, err := openLoginParent(home, rel)
	if err != nil {
		return false, fmt.Errorf("memberhome: login path %s in %q: %w", rel, member, err)
	}
	defer func() { _ = parent.Close() }()
	info, err := parent.Lstat(leaf)
	if err != nil {
		return false, fmt.Errorf("memberhome: login path %s in %q: %w", rel, member, err)
	}
	switch {
	case info.Mode()&fs.ModeSymlink != 0:
		return false, fmt.Errorf("memberhome: login path %s in %q is a symlink and cannot be shared", rel, member)
	case info.IsDir():
		return true, nil
	case info.Mode().IsRegular() && hasMultipleLinks(info):
		return false, fmt.Errorf("memberhome: login path %s in %q has another hard link and cannot be shared", rel, member)
	case info.Mode().IsRegular() && info.Size() == 0:
		return false, fmt.Errorf("memberhome: login path %s in %q is empty: %w", rel, member, fs.ErrNotExist)
	case info.Mode().IsRegular():
		return false, nil
	}
	return false, fmt.Errorf("memberhome: login path %s in %q is neither a regular file nor a directory and cannot be shared", rel, member)
}

// PrepareLoginMountpoint makes rel in member's home a directory (dir) or a
// regular file, creating missing parents and an empty 0600 file, so the
// runtime never creates the mountpoint as root inside a home a non-root run
// user must write. Nothing is followed through a symlink, and an entry of
// the other type is refused, never replaced.
func (m *Manager) PrepareLoginMountpoint(member domain.MemberID, rel string, dir bool) error {
	home, err := m.openHome(member)
	if err != nil {
		return err
	}
	defer func() { _ = home.Close() }()
	if dir {
		err = ensureDir(home, rel)
	} else {
		err = prepareLoginFile(home, rel)
	}
	if err != nil {
		return fmt.Errorf("memberhome: prepare login mountpoint %s in %q: %w", rel, member, err)
	}
	return nil
}

// prepareLoginFile creates rel as an empty 0600 file with its parents, or
// accepts the regular file already there.
func prepareLoginFile(home *os.Root, rel string) error {
	if err := ensureDir(home, path.Dir(rel)); err != nil {
		return err
	}
	err := writeNew(home, rel, nil, 0o600)
	if !errors.Is(err, fs.ErrExist) {
		return err
	}
	parent, leaf, err := openLoginParent(home, rel)
	if err != nil {
		return err
	}
	defer func() { _ = parent.Close() }()
	info, err := parent.Lstat(leaf)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("an entry that is not a regular file is there, but the account's login is one")
	}
	return nil
}

// openLoginParent pins the parent directory of rel beneath home, refusing a
// symlink at any directory component, and returns it with rel's last name.
func openLoginParent(home *os.Root, rel string) (*os.Root, string, error) {
	if rel == "." || path.Clean(rel) != rel || !filepath.IsLocal(rel) {
		return nil, "", fmt.Errorf("%q is not a path below the home", rel)
	}
	current, err := rootfs.OpenRoot(home, ".")
	if err != nil {
		return nil, "", err
	}
	parts := strings.Split(rel, "/")
	for _, part := range parts[:len(parts)-1] {
		info, err := current.Lstat(part)
		if err != nil {
			_ = current.Close()
			return nil, "", err
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			_ = current.Close()
			return nil, "", fmt.Errorf("%s is a symlink, so the path cannot be shared", part)
		}
		next, err := rootfs.OpenRoot(current, part)
		_ = current.Close()
		if err != nil {
			return nil, "", err
		}
		current = next
	}
	return current, parts[len(parts)-1], nil
}
