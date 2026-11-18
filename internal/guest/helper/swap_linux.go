package helper

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/3xDevOps/Aether/internal/guest"
)

const (
	swapFile = dataRoot + "/swap"
	// swapFlagDiscard is SWAP_FLAG_DISCARD: freed swap goes back to the
	// data disk, whose host file is sparse.
	swapFlagDiscard = 0x10000
)

// enableSwap makes a swap file of size bytes on the data disk and turns it
// on, as mkswap and swapon would: the image need have neither. A kernel
// built without swap, which has no /proc/swaps, gets no file.
func enableSwap(size uint64) error {
	if size == 0 {
		return nil
	}
	if _, err := os.Stat("/proc/swaps"); err != nil {
		return fmt.Errorf("the kernel has no swap: %w", err)
	}
	if err := mountVolume(guest.VolumeData, dataRoot); err != nil {
		return fmt.Errorf("mount the data volume: %w", err)
	}
	if err := writeSwapFile(swapFile, size); err != nil {
		return err
	}
	path, err := unix.BytePtrFromString(swapFile)
	if err != nil {
		return err
	}
	if _, _, errno := unix.Syscall(unix.SYS_SWAPON, uintptr(unsafe.Pointer(path)), swapFlagDiscard, 0); errno != 0 {
		return fmt.Errorf("swapon %s: %w", swapFile, errno)
	}
	return nil
}

// writeSwapFile allocates the whole file, since swapon refuses one with
// holes, and writes mkswap's version 1 header on its first page.
func writeSwapFile(path string, size uint64) (err error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	page := uint64(os.Getpagesize())
	size -= size % page
	if err = f.Truncate(int64(size)); err != nil {
		return err
	}
	if err = unix.Fallocate(int(f.Fd()), 0, 0, int64(size)); err != nil {
		return fmt.Errorf("allocate %s: %w", path, err)
	}
	header := make([]byte, page)
	binary.NativeEndian.PutUint32(header[1024:], 1)
	binary.NativeEndian.PutUint32(header[1028:], uint32(size/page-1))
	copy(header[page-10:], "SWAPSPACE2")
	if _, err = f.WriteAt(header, 0); err != nil {
		return err
	}
	return f.Sync()
}
