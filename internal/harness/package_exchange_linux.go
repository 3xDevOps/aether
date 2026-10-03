package harness

import (
	"os"

	"golang.org/x/sys/unix"
)

// ExchangePackages atomically swaps installed and staged package directories
// on the same filesystem without removing either path.
func ExchangePackages(installed, staged string) error {
	if err := unix.Renameat2(unix.AT_FDCWD, installed, unix.AT_FDCWD, staged, unix.RENAME_EXCHANGE); err != nil {
		return &os.LinkError{Op: "exchange", Old: installed, New: staged, Err: err}
	}
	return nil
}
