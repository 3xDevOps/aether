package memberhome

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
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
// stands in for a missing one of the launcher's, but only when every link it
// follows resolves in the run: the owner's ~/.local/bin and ~/.local/lib are
// mounted together under another directory, so a relative link between them
// works and an absolute one does not; installPaths are mounted at their own
// path, so only an absolute link reaches them. A link anywhere else dangles.
func (m *Manager) Installation(launcher, account domain.MemberID, executable string, installPaths []string) (domain.MemberID, error) {
	hops, err := m.executableInstalled(launcher, executable)
	if err != nil {
		return "", err
	}
	if hops != nil {
		return launcher, nil
	}
	if account == launcher {
		return "", nil
	}
	hops, err = m.executableInstalled(account, executable)
	if err != nil {
		return "", err
	}
	if hops == nil {
		return "", nil
	}
	relocated := true
	for _, hop := range hops {
		switch {
		case hop.absolute:
			if !within(hop.target, installPaths) {
				return "", nil
			}
			relocated = false
		case relocated:
			if !within(hop.target, []string{".local/bin", ".local/lib"}) {
				return "", nil
			}
		default:
			if !within(hop.target, installPaths) {
				return "", nil
			}
		}
	}
	return account, nil
}

// AgentInstalled looks for the ACP server only in the home the CLI comes
// from: the launch sees only that home's ~/.local. An empty acp means the
// agent serves no ACP.
func (m *Manager) AgentInstalled(launcher, account domain.MemberID, cli, acp string, installPaths []string) (installed, acpInstalled bool, err error) {
	owner, err := m.Installation(launcher, account, cli, installPaths)
	if err != nil || owner == "" || acp == "" {
		return owner != "", false, err
	}
	acpOwner, err := m.Installation(launcher, account, acp, installPaths)
	return true, acpOwner == owner, err
}

func within(rel string, roots []string) bool {
	return slices.ContainsFunc(roots, func(root string) bool { return rel == root || strings.HasPrefix(rel, root+"/") })
}

// hop is one symlink met while resolving an installation: the home-relative
// path it points at, and whether it was written as an absolute path.
type hop struct {
	target   string
	absolute bool
}

// executableInstalled resolves ~/.local/bin/<executable> in member's home,
// inside that home, to an executable regular file. It returns the symlinks
// met on the way, an empty but non-nil list for a plain file, or nil when
// there is no such installation.
func (m *Manager) executableInstalled(member domain.MemberID, executable string) ([]hop, error) {
	root, err := m.openExistingHome(member)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	_, hops, err := resolveExecutable(root, executable)
	if err != nil {
		return nil, fmt.Errorf("memberhome: find %s in %q: %w", executable, member, err)
	}
	return hops, nil
}

// ProgramUnit is what a session VM can load ~/.local/bin/<executable> of the
// home at home from: the home-relative file it resolves to, or the npm
// package in ~/.local/lib/node_modules holding that file. It is "" when the
// home has no such executable or the file is outside ~/.local.
func ProgramUnit(home, executable string) (string, error) {
	root, err := os.OpenRoot(home)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("memberhome: find %s in %s: %w", executable, home, err)
	}
	defer func() { _ = root.Close() }()
	file, _, err := resolveExecutable(root, executable)
	if err != nil {
		return "", fmt.Errorf("memberhome: find %s in %s: %w", executable, home, err)
	}
	if rest, ok := strings.CutPrefix(file, ".local/lib/node_modules/"); ok {
		name := strings.SplitN(rest, "/", 3)
		if strings.HasPrefix(name[0], "@") && len(name) > 2 {
			return ".local/lib/node_modules/" + name[0] + "/" + name[1], nil
		}
		if len(name) > 1 {
			return ".local/lib/node_modules/" + name[0], nil
		}
	}
	if !strings.HasPrefix(file, ".local/") {
		return "", nil
	}
	return file, nil
}

// resolveExecutable resolves ~/.local/bin/<executable> in root to an
// executable regular file, its home-relative path, and the symlinks met on
// the way, an empty but non-nil list for a plain file. Both are empty when
// there is no such installation.
func resolveExecutable(root *os.Root, executable string) (string, []hop, error) {
	hops := []hop{}

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
				return "", nil, nil
			}
			resolved = resolved[:len(resolved)-1]
			continue
		}
		candidate := filepath.Join(append(resolved, part)...)
		info, err := root.Lstat(candidate)
		if errors.Is(err, fs.ErrNotExist) {
			return "", nil, nil
		}
		if err != nil {
			return "", nil, err
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			links++
			if links > 40 {
				return "", nil, nil
			}
			target, err := root.Readlink(candidate)
			if err != nil {
				return "", nil, err
			}
			if path.IsAbs(target) {
				hops = append(hops, hop{target: harness.HomeRelative(target), absolute: true})
			} else {
				hops = append(hops, hop{target: path.Join(filepath.ToSlash(filepath.Dir(candidate)), target)})
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
					return "", nil, nil
				}
				resolved = nil
			}
			pending = append(strings.Split(target, "/"), pending...)
			continue
		}
		if len(pending) == 0 {
			if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
				return "", nil, nil
			}
			return filepath.ToSlash(candidate), hops, nil
		}
		if !info.IsDir() {
			return "", nil, nil
		}
		resolved = append(resolved, part)
	}
	return "", nil, nil
}
