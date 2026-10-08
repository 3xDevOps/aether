//go:build windows

package disk

import (
	"io/fs"
	"os"
	"syscall"
)

// fileKey is the public Windows identity used by os.SameFile: a volume and
// the file's index on that volume, shared by all of its hardlinks.
type fileKey struct{ volume, indexHigh, indexLow uint32 }

type seen struct {
	root  *os.Root
	files map[fileKey]struct{}
}

func newSeen(root *os.Root) seen {
	return seen{root: root, files: make(map[fileKey]struct{})}
}

func (s seen) claim(name string, _ fs.FileInfo) (bool, error) {
	// FileInfo.Sys exposes only Win32FileAttributeData, not the file ID.
	// Obtain it from a handle opened beneath the same root as the traversal.
	// OPEN_REPARSE_POINT prevents a replaced leaf from following a symlink.
	f, err := s.root.OpenFile(name, os.O_RDONLY|syscall.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return false, err
	}
	defer func() { _ = f.Close() }()
	var st syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(syscall.Handle(f.Fd()), &st); err != nil {
		return false, err
	}
	if st.FileAttributes&syscall.FILE_ATTRIBUTE_DIRECTORY != 0 {
		return false, nil
	}
	if st.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		// Some reparse points (Windows data deduplication) are regular files;
		// inspect their mode rather than discarding every reparse point.
		info, err := f.Stat()
		if err != nil {
			return false, err
		}
		if !info.Mode().IsRegular() {
			return false, nil
		}
	}
	key := fileKey{st.VolumeSerialNumber, st.FileIndexHigh, st.FileIndexLow}
	if _, dup := s.files[key]; dup {
		return false, nil
	}
	s.files[key] = struct{}{}
	return true, nil
}
