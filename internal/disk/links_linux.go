//go:build linux

package disk

import (
	"io/fs"
	"syscall"
)

// inodeKey identifies one filesystem object across every hardlink to it.
type inodeKey struct{ dev, ino uint64 }

// seen is the set of filesystem objects one measurement has already
// charged to a component.
type seen map[inodeKey]struct{}

func newSeen() seen { return make(seen) }

// claim reports whether this device/inode has already been attributed.
func (s seen) claim(info fs.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return true
	}
	key := inodeKey{st.Dev, st.Ino}
	if _, dup := s[key]; dup {
		return false
	}
	s[key] = struct{}{}
	return true
}
