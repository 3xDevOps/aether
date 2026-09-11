//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package gitengine

import (
	"io/fs"
	"os"
)

func chownCheckoutTemp(_ *os.File, _ *os.Root, _ string, _ fs.FileInfo) error {
	return nil
}

func chownCheckoutDir(_ *os.Root, _ fs.FileInfo) error {
	return nil
}
