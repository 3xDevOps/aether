package memberhome

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"

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

// maxBorrowedStateBytes bounds the JSON file MarkBorrowedState rewrites; a
// CLI's own state file is small, and anything larger is not rewritten.
const maxBorrowedStateBytes = 4 << 20

// MarkBorrowedState makes rel, a JSON object file in member's home, carry
// each value in keys, creating the file with mode 0600 when it is missing.
// A file that cannot be read as a small regular JSON object is left alone
// and the launch goes on: the CLI owns it and recovers from it in its own
// way. Two launches creating the file at once both succeed: the loser of the
// exclusive create re-reads what the winner wrote.
func (m *Manager) MarkBorrowedState(member domain.MemberID, rel string, keys map[string]any) error {
	home, err := m.openHome(member)
	if err != nil {
		return err
	}
	defer func() { _ = home.Close() }()
	parent, leaf, err := openLoginParent(home, rel)
	if err != nil {
		return fmt.Errorf("memberhome: mark %s in %q: %w", rel, member, err)
	}
	defer func() { _ = parent.Close() }()
	for {
		data, err := readRegularFile(parent, leaf, maxBorrowedStateBytes)
		if err != nil {
			return nil
		}
		state := map[string]any{}
		if data != nil {
			if json.Unmarshal(data, &state) != nil || state == nil {
				return nil
			}
		}
		changed := false
		for key, value := range keys {
			if !reflect.DeepEqual(state[key], value) {
				state[key] = value
				changed = true
			}
		}
		if !changed {
			return nil
		}
		out, err := json.Marshal(state)
		if err != nil {
			return fmt.Errorf("memberhome: mark %s in %q: %w", rel, member, err)
		}
		if data == nil {
			err = writeNew(parent, leaf, out, 0o600)
			if errors.Is(err, fs.ErrExist) {
				continue
			}
		} else {
			err = writeInPlace(parent, leaf, out)
		}
		if err != nil {
			return fmt.Errorf("memberhome: mark %s in %q: %w", rel, member, err)
		}
		return nil
	}
}

// writeInPlace rewrites the regular file name under root on its own inode,
// so a container that holds the file open or mounted keeps seeing it.
func writeInPlace(root *os.Root, name string, data []byte) error {
	f, err := root.OpenFile(name, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// LoginFound reports whether any of the home-relative login paths rels
// exists in member's home as a non-empty directory or file. An empty one is
// a mountpoint Aether created, not a login. Only presence is checked: a
// path that is there may still hold an expired login.
func (m *Manager) LoginFound(member domain.MemberID, rels []string) (bool, error) {
	home, err := m.openExistingHome(member)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer func() { _ = home.Close() }()
	for _, rel := range rels {
		info, err := home.Stat(rel)
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
			continue
		}
		if err != nil {
			return false, fmt.Errorf("memberhome: login path %s in %q: %w", rel, member, err)
		}
		if !info.IsDir() {
			if info.Size() > 0 {
				return true, nil
			}
			continue
		}
		if found, err := dirHasEntry(home, rel); err != nil || found {
			return found, err
		}
	}
	return false, nil
}

func dirHasEntry(home *os.Root, rel string) (bool, error) {
	dir, err := home.Open(rel)
	if err != nil {
		return false, fmt.Errorf("memberhome: open login directory %s: %w", rel, err)
	}
	defer func() { _ = dir.Close() }()
	entries, err := dir.ReadDir(1)
	if err != nil && !errors.Is(err, io.EOF) {
		return false, fmt.Errorf("memberhome: read login directory %s: %w", rel, err)
	}
	return len(entries) > 0, nil
}
