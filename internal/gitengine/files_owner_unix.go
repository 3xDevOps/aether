//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package gitengine

import (
	"io/fs"
	"os"
	"syscall"
)

func chownCheckoutTemp(file *os.File, root *os.Root, parent string, existing fs.FileInfo) error {
	owner := existing
	if owner == nil {
		var err error
		owner, err = root.Stat(parent)
		if err != nil {
			return err
		}
	}
	st, ok := owner.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	return file.Chown(int(st.Uid), int(st.Gid))
}

func chownCheckoutDir(root *os.Root, owner fs.FileInfo) error {
	st, ok := owner.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	return root.Chown(".", int(st.Uid), int(st.Gid))
}
