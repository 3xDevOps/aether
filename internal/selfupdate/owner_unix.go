//go:build !windows

package selfupdate

import (
	"os"
	"syscall"
)

// fileOwner reports the uid that owns the file info describes.
func fileOwner(info os.FileInfo) (uint32, bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return st.Uid, true
}
