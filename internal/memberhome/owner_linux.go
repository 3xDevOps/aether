//go:build linux

package memberhome

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"syscall"

	"github.com/3xDevOps/Aether/internal/rootfs"
)

// homeOwner returns the owner a server-created entry should inherit.
func homeOwner(root *os.Root) (int, int, bool, error) {
	info, err := root.Stat(".")
	if err != nil {
		return 0, 0, false, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(st.Uid) == os.Getuid() {
		return 0, 0, false, nil
	}
	return int(st.Uid), int(st.Gid), true, nil
}

func chownFileLikeHome(owner *os.Root, file *os.File) error {
	uid, gid, ok, err := homeOwner(owner)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	return file.Chown(uid, gid)
}

func chownLikeHomeAt(owner, target *os.Root, name string) error {
	uid, gid, ok, err := homeOwner(owner)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	if err := target.Lchown(name, uid, gid); err != nil {
		return fmt.Errorf("chown %s: %w", name, err)
	}
	return nil
}

// chownLikeHome gives each name the uid:gid the home directory itself carries.
// Names are resolved through pinned parent descriptors, so a replacement
// symlink cannot redirect the ownership change.
func chownLikeHome(root *os.Root, names ...string) error {
	for _, name := range names {
		parent := root
		var owned *os.Root
		parentName := path.Dir(name)
		if parentName != "." {
			var err error
			owned, err = rootfs.OpenRoot(root, parentName)
			if err != nil {
				return err
			}
			parent = owned
		}
		err := chownLikeHomeAt(root, parent, path.Base(name))
		if owned != nil {
			_ = owned.Close()
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func hasMultipleLinks(info fs.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && st.Nlink > 1
}
