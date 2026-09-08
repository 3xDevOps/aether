//go:build linux

package memberhome

import (
	"fmt"
	"os"
	"syscall"
)

// chownLikeHome gives each name the uid:gid the home directory itself
// carries, when that is not the server's own uid. The scheduler chowns a
// member home to the container user for non-root images, and a file the
// server wrote would otherwise stay root-owned inside a container that
// runs as somebody else - enough to make gh auth setup-git fail on
// .gitconfig. Names are relative to root, so a symlink planted in the home
// cannot redirect the chown outside it.
func chownLikeHome(root *os.Root, names ...string) error {
	info, err := root.Stat(".")
	if err != nil {
		return err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(st.Uid) == os.Getuid() {
		return nil
	}
	for _, name := range names {
		if err := root.Lchown(name, int(st.Uid), int(st.Gid)); err != nil {
			return fmt.Errorf("chown %s: %w", name, err)
		}
	}
	return nil
}
