package memberhome

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"strings"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/harness"
)

// Installation returns whose ~/.local/bin/<executable> a launch by launcher
// on account's account runs: launcher's own when their home has it, else
// account's, else "". On another member's account the owner's installation
// stands in for a missing one of the launcher's.
func (m *Manager) Installation(launcher, account domain.MemberID, executable string) (domain.MemberID, error) {
	for _, member := range []domain.MemberID{launcher, account} {
		installed, err := m.executableInstalled(member, executable)
		if err != nil {
			return "", err
		}
		if installed {
			return member, nil
		}
		if account == launcher {
			break
		}
	}
	return "", nil
}

// executableInstalled reports whether ~/.local/bin/<executable> in member's
// home resolves, inside that home, to an executable regular file.
func (m *Manager) executableInstalled(member domain.MemberID, executable string) (bool, error) {
	root, err := m.openHome(member)
	if err != nil {
		return false, err
	}
	defer func() { _ = root.Close() }()

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
				return false, nil
			}
			resolved = resolved[:len(resolved)-1]
			continue
		}
		candidate := filepath.Join(append(resolved, part)...)
		info, err := root.Lstat(candidate)
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("memberhome: find %s in %q: %w", executable, member, err)
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			links++
			if links > 40 {
				return false, nil
			}
			target, err := root.Readlink(candidate)
			if err != nil {
				return false, fmt.Errorf("memberhome: find %s in %q: %w", executable, member, err)
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
					return false, nil
				}
				resolved = nil
			}
			pending = append(strings.Split(target, "/"), pending...)
			continue
		}
		if len(pending) == 0 {
			return info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0, nil
		}
		if !info.IsDir() {
			return false, nil
		}
		resolved = append(resolved, part)
	}
	return false, nil
}
