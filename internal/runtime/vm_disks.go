package runtime

import "context"

// DiskUseRuntime runs containers whose disks are sparse files on the host,
// which grow as their guests write until the host's filesystem is full.
// DiskUse reports the bytes each container's disks take on the host.
type DiskUseRuntime interface {
	DiskUse(ctx context.Context) (map[ID]int64, error)
}
