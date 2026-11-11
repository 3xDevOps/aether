package runtime

import (
	"context"
	"fmt"

	"github.com/moby/moby/client"
)

// SessionSlice is the systemd slice session containers run in. Its name has
// no dash: systemd nests "a-b.slice" inside "a.slice", which would put the
// weights packaging/systemd/aether.slice sets below a parent at the default.
const SessionSlice = "aether.slice"

// sessionCgroupFS is the cgroupfs driver's equivalent of SessionSlice.
const sessionCgroupFS = "/aether"

// sessionCgroupParent is empty for a driver it does not know, which leaves
// the daemon's default parent.
func sessionCgroupParent(driver string) string {
	switch driver {
	case "systemd":
		return SessionSlice
	case "cgroupfs":
		return sessionCgroupFS
	}
	return ""
}

// cgroupParent caches the daemon's cgroup driver once a lookup succeeds. The
// lookup runs outside the lock so a slow daemon cannot hold a caller past
// its own deadline.
func (d *Docker) cgroupParent(ctx context.Context) (string, error) {
	d.cgroupMu.Lock()
	parent, known := d.cgroupParentName, d.cgroupKnown
	d.cgroupMu.Unlock()
	if known {
		return parent, nil
	}
	info, err := d.cli.Info(ctx, client.InfoOptions{})
	if err != nil {
		return "", fmt.Errorf("runtime: read docker cgroup driver: %w", err)
	}
	parent = sessionCgroupParent(info.Info.CgroupDriver)
	d.cgroupMu.Lock()
	d.cgroupParentName, d.cgroupKnown = parent, true
	d.cgroupMu.Unlock()
	return parent, nil
}
