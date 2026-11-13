//go:build !unix

package disk

import (
	"errors"
	"io/fs"
)

// filesystem has no portable answer off unix. The server ships for linux
// only; this exists so the package builds - and reports honestly - wherever
// the tooling runs.
func filesystem(string) (Usage, error) {
	return Usage{}, errors.New("disk: filesystem usage is not supported on this platform")
}

func SameFilesystem(string, string) (bool, error) {
	return false, errors.New("disk: filesystem identity is not supported on this platform")
}

func stored(info fs.FileInfo) uint64 { return uint64(info.Size()) }
