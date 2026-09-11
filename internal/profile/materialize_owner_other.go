//go:build !linux

package profile

import (
	"io/fs"
	"os"
)

func materializeOwner(_ *os.File, _ *os.Root, _ fs.FileInfo) error { return nil }
