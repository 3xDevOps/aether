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

// claim reports whether info is being counted for the first time in this
// measurement, recording it when it is. Files with a single link are
// waved through without touching the map: nothing else can reach them, so
// they cannot be double-counted, and the map then costs only the shared
// objects rather than one entry per file in the data directory.
func (s seen) claim(info fs.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st.Nlink < 2 {
		return true
	}
	key := inodeKey{st.Dev, st.Ino}
	if _, dup := s[key]; dup {
		return false
	}
	s[key] = struct{}{}
	return true
}
