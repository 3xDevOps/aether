//go:build unix

package runtime

import (
	"io/fs"
	"syscall"
)

func fileInode(info fs.FileInfo) (uint64, error) {
	return info.Sys().(*syscall.Stat_t).Ino, nil
}
