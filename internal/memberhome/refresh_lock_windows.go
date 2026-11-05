package memberhome

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// openLoginForWrite opens leaf without following a reparse point and refuses
// a handle that is not the file parent holds at leaf, so a link or junction
// swapped in for the file or a directory above it is never written through.
// Windows has no FIFO in the file system, so the open cannot block.
func openLoginForWrite(parent *os.Root, leaf string) (*os.File, error) {
	name := filepath.Join(parent.Name(), leaf)
	path, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(path, windows.GENERIC_WRITE, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: name, Err: err}
	}
	f := os.NewFile(uintptr(h), name)
	opened, err := f.Stat()
	if err == nil {
		var held os.FileInfo
		if held, err = parent.Lstat(leaf); err == nil && !os.SameFile(opened, held) {
			err = fmt.Errorf("%s changed while it was opened", leaf)
		}
	}
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

// tryLockFile locks all of f, as Rust's File::try_lock does for Codex,
// without waiting and reports whether it did. Closing f releases it.
func tryLockFile(f *os.File) (bool, error) {
	err := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, ^uint32(0), ^uint32(0), new(windows.Overlapped))
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return false, nil
	}
	return err == nil, err
}
