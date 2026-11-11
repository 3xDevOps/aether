package runtime

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/moby/moby/client"
)

// SessionSlice is the systemd slice session containers run in. Its name has
// no dash: systemd nests "a-b.slice" inside "a.slice", which would put the
// weights packaging/systemd/aether.slice sets below a parent at the default.
const SessionSlice = "aether.slice"

const cgroupDriverTimeout = 5 * time.Second

// cgroupParent caches the parent for the daemon's cgroup driver once a lookup
// succeeds, so a driver change needs a server restart. An unknown driver gets
// "", the daemon's default. The lookup runs outside the lock so a slow daemon
// cannot hold a caller past its own deadline.
func (d *Docker) cgroupParent(ctx context.Context) (string, error) {
	d.cgroupMu.Lock()
	parent, known := d.cgroupParentName, d.cgroupKnown
	d.cgroupMu.Unlock()
	if known {
		return parent, nil
	}
	ctx, cancel := context.WithTimeout(ctx, cgroupDriverTimeout)
	defer cancel()
	info, err := d.cli.Info(ctx, client.InfoOptions{})
	if err != nil {
		return "", fmt.Errorf("runtime: read docker cgroup driver: %w", err)
	}
	switch info.Info.CgroupDriver {
	case "systemd":
		parent = SessionSlice
	case "cgroupfs":
		parent = "/aether"
	}
	d.cgroupMu.Lock()
	d.cgroupParentName, d.cgroupKnown = parent, true
	d.cgroupMu.Unlock()
	return parent, nil
}

// UsesSessionSlice reports whether new containers land in SessionSlice of
// this host's systemd: the daemon is local and uses the systemd driver.
func (d *Docker) UsesSessionSlice(ctx context.Context) (bool, error) {
	if !strings.HasPrefix(d.cli.DaemonHost(), "unix://") {
		return false, nil
	}
	parent, err := d.cgroupParent(ctx)
	return parent == SessionSlice, err
}
