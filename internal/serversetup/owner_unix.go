//go:build unix

package serversetup

import (
	"os"
	"syscall"
)

func fileOwner(info os.FileInfo) (uid, gid int) {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return int(st.Uid), int(st.Gid)
	}
	return -1, -1
}
