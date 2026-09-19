package ptyhost

import "os"

func legacyCastFileID(info os.FileInfo) uint64 {
	return uint64(info.ModTime().UnixNano())
}
