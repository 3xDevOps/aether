//go:build windows

package localops

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

// openLockFile opens the lock file with full share flags: without
// FILE_SHARE_DELETE, Windows refuses to delete the file while any handle
// is open, even one whose byte range was unlocked.
func openLockFile(path string) (*os.File, error) {
	p16, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(p16, windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_ALWAYS, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(h), path), nil
}

// lockFileExclusive holds an exclusive advisory lock on the first byte of f
// until release. The kernel releases it when the holder exits, so a crashed
// build cannot leave the directory locked and a competing builder waits on
// the kernel instead of guessing at a holder's liveness.
func lockFileExclusive(ctx context.Context, f *os.File) (func() error, error) {
	ol := new(windows.Overlapped)
	handle := windows.Handle(f.Fd())
	for {
		err := windows.LockFileEx(handle,
			windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
			0, 1, 0, ol)
		if err == nil {
			return func() error {
				return windows.UnlockFileEx(handle, 0, 1, 0, ol)
			}, nil
		}
		if !errors.Is(err, windows.ERROR_LOCK_VIOLATION) && !errors.Is(err, windows.ERROR_IO_PENDING) {
			return nil, fmt.Errorf("localops: lock desktop build dir: %w", err)
		}
		select {
		case <-time.After(500 * time.Millisecond):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}
