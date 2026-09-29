//go:build !windows

package edgeclient

import (
	"os"
	"syscall"
)

// lockFile waits for an exclusive lock on f and returns its release.
func lockFile(f *os.File) (func(), error) {
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return nil, err
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }, nil
}
