//go:build linux

package profile

import (
	"io/fs"
	"os"
	"syscall"
)

func materializeOwner(file *os.File, parent *os.Root, target fs.FileInfo) error {
	if target == nil {
		var err error
		target, err = parent.Stat(".")
		if err != nil {
			return err
		}
	}
	st, ok := target.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	return file.Chown(int(st.Uid), int(st.Gid))
}
