package memberhome

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/harness"
)

// Installation returns whose ~/.local/bin/<executable> a launch by launcher
// on account's account runs: launcher's own when their home has it, else
// account's, else "". On another member's account the owner's installation
// stands in for a missing one of the launcher's, but only when every link
// it follows stays inside borrowRoots, the directories the run mounts from
// the owner's home; a link into anything else would dangle in the run.
func (m *Manager) Installation(launcher, account domain.MemberID, executable string, borrowRoots []string) (domain.MemberID, error) {
	visited, err := m.executableInstalled(launcher, executable)
	if err != nil {
		return "", err
	}
	if visited != nil {
		return launcher, nil
	}
	if account == launcher {
		return "", nil
	}
	visited, err = m.executableInstalled(account, executable)
	if err != nil {
		return "", err
	}
	if visited == nil {
		return "", nil
	}
	for _, rel := range visited {
		if !slices.ContainsFunc(borrowRoots, func(root string) bool { return rel == root || strings.HasPrefix(rel, root+"/") }) {
			return "", nil
		}
	}
	return account, nil
}

// executableInstalled resolves ~/.local/bin/<executable> in member's home,
// inside that home, to an executable regular file. It returns the
// home-relative path each symlink on the way points at and the final file,
// or nil when there is no such installation.
func (m *Manager) executableInstalled(member domain.MemberID, executable string) ([]string, error) {
	root, err := m.openHome(member)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	var visited []string

	// Vendor installers use absolute container-home symlinks. Resolve each
	// component explicitly; os.Root confines even concurrent symlink swaps
	// to the member's home instead of following a link on the server host.
	pending := []string{".local", "bin", executable}
	resolved := []string{}
	links := 0
	for len(pending) > 0 {
		part := pending[0]
		pending = pending[1:]
		switch part {
		case "", ".":
			continue
		case "..":
			if len(resolved) == 0 {
				return nil, nil
			}
			resolved = resolved[:len(resolved)-1]
			continue
		}
		candidate := filepath.Join(append(resolved, part)...)
		info, err := root.Lstat(candidate)
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		if err != nil {
			return nil, fmt.Errorf("memberhome: find %s in %q: %w", executable, member, err)
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			links++
			if links > 40 {
				return nil, nil
			}
			target, err := root.Readlink(candidate)
			if err != nil {
				return nil, fmt.Errorf("memberhome: find %s in %q: %w", executable, member, err)
			}
			// Where the link points, as the run would resolve it: that path
			// is what has to be mounted for the link to work there.
			if path.IsAbs(target) {
				visited = append(visited, harness.HomeRelative(target))
			} else {
				visited = append(visited, path.Join(filepath.ToSlash(filepath.Dir(candidate)), target))
			}
			if path.IsAbs(target) {
				// Strip only the home prefix: cleaning before resolving a
				// symlink followed by ".." would change its meaning.
				switch {
				case strings.HasPrefix(target, harness.HomeDir("")+"/"):
					target = strings.TrimPrefix(target, harness.HomeDir("")+"/")
				case strings.HasPrefix(target, harness.HomeDir("1000")+"/"):
					target = strings.TrimPrefix(target, harness.HomeDir("1000")+"/")
				default:
					return nil, nil
				}
				resolved = nil
			}
			pending = append(strings.Split(target, "/"), pending...)
			continue
		}
		if len(pending) == 0 {
			if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
				return nil, nil
			}
			return append(visited, filepath.ToSlash(candidate)), nil
		}
		if !info.IsDir() {
			return nil, nil
		}
		resolved = append(resolved, part)
	}
	return nil, nil
}
