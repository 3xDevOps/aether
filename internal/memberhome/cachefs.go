package memberhome

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/rootfs"
)

// openManagedRoot traverses every component without following links. The
// configured roots and their parents are administrator-owned, never mounted
// into member containers; only each pool's data directory is exposed.
func openManagedRoot(name string, create bool) (*os.Root, error) {
	absolute, err := filepath.Abs(name)
	if err != nil {
		return nil, err
	}
	volume := filepath.VolumeName(absolute) + string(filepath.Separator)
	root, err := os.OpenRoot(volume)
	if err != nil {
		return nil, err
	}
	for _, part := range strings.Split(strings.TrimPrefix(absolute, volume), string(filepath.Separator)) {
		if part == "" {
			continue
		}
		next, err := openCacheDirectory(root, part, create)
		_ = root.Close()
		if err != nil {
			return nil, err
		}
		root = next
	}
	return root, nil
}

func openCacheDirectory(parent *os.Root, name string, create bool) (*os.Root, error) {
	root, err := rootfs.OpenRoot(parent, name)
	if !create || !errors.Is(err, fs.ErrNotExist) {
		return root, err
	}
	if err = parent.Mkdir(name, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return nil, err
	}
	root, err = rootfs.OpenRoot(parent, name)
	if err != nil {
		return nil, err
	}
	if err := syncCacheDirectory(parent); err != nil {
		_ = root.Close()
		return nil, err
	}
	return root, nil
}

func cacheDirectoryEntries(root *os.Root) ([]os.DirEntry, error) {
	dir, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer func() { _ = dir.Close() }()
	return dir.ReadDir(-1)
}

func syncCacheDirectory(root *os.Root) error {
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}

func cacheRootMembers(name string) ([]domain.MemberID, error) {
	root, err := openManagedRoot(name, false)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("memberhome: cannot inspect member roots: %w", err)
	}
	defer func() { _ = root.Close() }()
	entries, err := cacheDirectoryEntries(root)
	if err != nil {
		return nil, err
	}
	members := make([]domain.MemberID, 0, len(entries))
	for _, entry := range entries {
		if validateMemberID(entry.Name()) == nil {
			// Include unsafe entries as uncertain owners instead of silently
			// losing a deleted member's runtime/image retry key.
			members = append(members, domain.MemberID(entry.Name()))
		}
	}
	slices.Sort(members)
	return members, nil
}
