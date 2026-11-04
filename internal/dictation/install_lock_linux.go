package dictation

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// lockInstall holds dir's install.lock, so that a concurrent install or
// remove cannot delete the files another one just published.
func lockInstall(dir string) (func(), error) {
	path := filepath.Join(dir, "install.lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("another dictation install or remove holds %s: %w", path, err)
	}
	return func() { _ = f.Close() }, nil
}
