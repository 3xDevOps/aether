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
		return errors.New("it is not a regular file, and a file is mounted over it")
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

// maxStateBytes bounds the CLI state file StateWithout reads. Claude Code's
// ~/.claude.json grows with every project it has opened.
const maxStateBytes = 64 << 20

// StateWithout returns rel, a JSON object file in member's home, without
// the keys drop and with each value in set, and whether the file holds
// anything. A missing or empty file gives set alone. A symlink at any component, a
// file of another type or with another hard link, one over maxStateBytes,
// or one that is not a JSON object is an error.
func (m *Manager) StateWithout(member domain.MemberID, rel string, drop []string, set map[string]any) ([]byte, bool, error) {
	home, err := m.openHome(member)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = home.Close() }()
	data, err := readState(home, rel)
	if err != nil {
		return nil, false, fmt.Errorf("memberhome: read %s in %q: %w", rel, member, err)
	}
	state := map[string]json.RawMessage{}
	if len(data) > 0 {
		if err = json.Unmarshal(data, &state); err != nil {
			return nil, false, fmt.Errorf("memberhome: read %s in %q: %w", rel, member, err)
		}
		if state == nil {
			return nil, false, fmt.Errorf("memberhome: read %s in %q: not a JSON object", rel, member)
		}
	}
	for _, key := range drop {
		delete(state, key)
	}
	for key, value := range set {
		if state[key], err = json.Marshal(value); err != nil {
			return nil, false, fmt.Errorf("memberhome: copy %s in %q: %w", rel, member, err)
		}
	}
	out, err := json.Marshal(state)
	if err != nil {
		return nil, false, fmt.Errorf("memberhome: copy %s in %q: %w", rel, member, err)
	}
	return out, len(data) > 0, nil
}

func readState(home *os.Root, rel string) ([]byte, error) {
	parent, leaf, err := openLoginParent(home, rel)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = parent.Close() }()
	f, err := rootfs.Open(parent, leaf)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	switch {
	case err != nil:
		return nil, err
	case hasMultipleLinks(info):
		return nil, errors.New("it has another hard link")
	case info.Size() > maxStateBytes:
		return nil, fmt.Errorf("it is %d bytes, over the %d MiB Aether copies; remove old entries from it (Claude Code's grows with its \"projects\" history)", info.Size(), maxStateBytes>>20)
	}
	return io.ReadAll(io.LimitReader(f, maxStateBytes))
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

// LoginStamp changes when any of the home-relative login paths rels in
// member's home is written, replaced, added or removed.
func (m *Manager) LoginStamp(member domain.MemberID, rels []string) (string, error) {
	home, err := m.openExistingHome(member)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	defer func() { _ = home.Close() }()
	var stamp strings.Builder
	for _, rel := range rels {
		info, err := home.Stat(rel)
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("memberhome: login path %s in %q: %w", rel, member, err)
		}
		fmt.Fprintf(&stamp, "%s %d %d\n", rel, info.Size(), info.ModTime().UnixNano())
	}
	return stamp.String(), nil
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
