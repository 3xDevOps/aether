//go:build !windows

package localops

import (
	"context"
	"fmt"
	"os"
	"syscall"
	"time"
)

// openLockFile opens the lock file plainly; POSIX unlinking works on open
// handles, so no share flags are needed.
func openLockFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
}

// lockFileExclusive holds an exclusive advisory lock on f until release.
// The kernel releases it when the holder exits, so a crashed build cannot
// leave the directory locked and a competing builder waits on the kernel
// instead of guessing at a holder's liveness.
func lockFileExclusive(ctx context.Context, f *os.File) (func() error, error) {
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() error { return syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }, nil
		}
		if err != syscall.EWOULDBLOCK && err != syscall.EINTR {
			return nil, fmt.Errorf("localops: lock desktop build dir: %w", err)
		}
		select {
		case <-time.After(500 * time.Millisecond):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}
