//go:build linux

package scheduler

import (
	"io/fs"
	"syscall"
)

// fileIdentity changes when a file is replaced, written, or chmodded: each
// write or chmod sets ctime to the current time, and no system call sets it
// back.
type fileIdentity struct {
	dev, ino     uint64
	size         int64
	mtime, ctime syscall.Timespec
	mode         uint32
}

func identityOf(info fs.FileInfo) fileIdentity {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fileIdentity{}
	}
	return fileIdentity{dev: st.Dev, ino: st.Ino, size: st.Size, mtime: st.Mtim, ctime: st.Ctim, mode: st.Mode}
}
