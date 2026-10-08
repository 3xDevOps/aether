//go:build linux

package memberhome

import (
	"os"

	"golang.org/x/sys/unix"
)

// renameCacheEntry moves only the leaf between pinned parents. No component
// of the member-controlled home path is re-resolved by a path-based remover.
func renameCacheEntry(source *os.Root, name string, target *os.Root, destination string) error {
	from, err := source.Open(".")
	if err != nil {
		return err
	}
	defer func() { _ = from.Close() }()
	to, err := target.Open(".")
	if err != nil {
		return err
	}
	defer func() { _ = to.Close() }()
	return unix.Renameat2(int(from.Fd()), name, int(to.Fd()), destination, unix.RENAME_NOREPLACE)
}
