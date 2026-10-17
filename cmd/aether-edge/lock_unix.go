//go:build unix

package main

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

const (
	exclusive = unix.LOCK_EX
	shared    = unix.LOCK_SH
)

// lockPath takes an exclusive or shared lock on the file at path without
// waiting, creating it 0600, and returns its release. It reports errLocked
// while another process holds a conflicting lock. flock does not lock
// across NFS.
func lockPath(path string, how int) (func(), error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	if err := unix.Flock(int(f.Fd()), how|unix.LOCK_NB); err != nil {
		f.Close() //nolint:errcheck // the lock error takes precedence
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, fmt.Errorf("%w: %s", errLocked, path)
		}
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	return func() { _ = f.Close() }, nil
}
