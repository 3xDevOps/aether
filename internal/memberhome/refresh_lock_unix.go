//go:build unix

package memberhome

import (
	"errors"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func openLoginForWrite(parent *os.Root, leaf string) (*os.File, error) {
	return parent.OpenFile(leaf, os.O_WRONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
}

// tryLockFile takes flock(2) on f without waiting and reports whether it did.
func tryLockFile(f *os.File) (bool, error) {
	err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if errors.Is(err, unix.EWOULDBLOCK) {
		return false, nil
	}
	return err == nil, err
}
