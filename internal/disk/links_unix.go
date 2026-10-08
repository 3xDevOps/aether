//go:build unix

package disk

import (
	"io/fs"
	"os"
	"syscall"
)

// inodeKey identifies one filesystem object across every hardlink to it.
type inodeKey struct {
	dev int64
	ino uint64
}

// seen is the set of filesystem objects one measurement has already
// charged to a component.
type seen map[inodeKey]struct{}

func newSeen(_ *os.Root) seen { return make(seen) }

// claim charges each device/inode once, without opening the file again.
func (s seen) claim(_ string, info fs.FileInfo) (bool, error) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false, fs.ErrInvalid
	}
	key := inodeKey{int64(st.Dev), st.Ino}
	if _, dup := s[key]; dup {
		return false, nil
	}
	s[key] = struct{}{}
	return true, nil
}
