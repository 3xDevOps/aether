//go:build !windows

package ptyhost

import (
	"os"
	"syscall"
)

func legacyCastFileID(info os.FileInfo) uint64 {
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && stat.Ino != 0 {
		return stat.Ino
	}
	return uint64(info.ModTime().UnixNano())
}
