//go:build unix

package disk

import (
	"fmt"
	"syscall"
)

// filesystem reads the headroom of the filesystem holding path. Free is
// Bavail, what an unprivileged writer can still claim, which is what the
// floor must be checked against; Used deliberately counts reserved blocks
// as gone, because for anyone but root they are.
func filesystem(path string) (Usage, error) {
	var fs syscall.Statfs_t
	if err := syscall.Statfs(path, &fs); err != nil {
		return Usage{}, fmt.Errorf("disk: statfs %s: %w", path, err)
	}
	block := uint64(fs.Bsize)
	return Usage{
		FreeBytes:  fs.Bavail * block,
		UsedBytes:  (fs.Blocks - fs.Bfree) * block,
		TotalBytes: fs.Blocks * block,
	}, nil
}
