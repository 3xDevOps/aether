package runtime

import "context"

// DiskUseRuntime runs containers whose disks are sparse files on the host,
// which grow as their guests write until the host's filesystem is full.
// DiskUse reports the bytes each container's disks take on the host.
// HoldDisks stops a paused container's guest entirely, so nothing in it,
// root included, writes more; its next Resume starts it again.
type DiskUseRuntime interface {
	DiskUse(ctx context.Context) (map[ID]int64, error)
	HoldDisks(ctx context.Context, id ID) error
}
