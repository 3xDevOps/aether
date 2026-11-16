//go:build !windows

package localops

import (
	"io/fs"
	"os"
	"syscall"
)

// ownedByUser reports whether this process's user owns the file info
// describes.
func ownedByUser(info fs.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Getuid()
}
