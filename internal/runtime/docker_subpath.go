package runtime

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
)

// checkSubpathMounts confirms the container sees each subpath mount's own
// file or directory at its target, comparing inodes only. A mount the engine
// or container runtime leaves out shows the container whatever the parent
// mount holds there instead: for a shared account's login, the launcher's own.
func (d *Docker) checkSubpathMounts(ctx context.Context, id ID, mounts []mount.Mount) error {
	for _, m := range mounts {
		if m.Type != mount.TypeVolume || m.VolumeOptions == nil || m.VolumeOptions.Subpath == "" || m.VolumeOptions.DriverConfig == nil {
			continue
		}
		base, sub := m.VolumeOptions.DriverConfig.Options["device"], m.VolumeOptions.Subpath
		want, err := subpathInode(base, sub)
		if err != nil {
			return fmt.Errorf("runtime: check the mount at %s: %w", m.Target, err)
		}
		code, output, _, err := d.execCombined(ctx, id, []string{"ls", "-di", "--", m.Target}, "", "0:0")
		if err != nil {
			return fmt.Errorf("runtime: check the mount at %s: %w", m.Target, err)
		}
		fields := strings.Fields(output)
		if code != 0 || len(fields) == 0 {
			return fmt.Errorf("runtime: check the mount at %s: ls exited %d: %s", m.Target, code, strings.TrimSpace(output))
		}
		if fields[0] != strconv.FormatUint(want, 10) {
			return fmt.Errorf("runtime: %s in the container is inode %s, not %s in %s (inode %d); %s",
				m.Target, fields[0], sub, base, want, d.mountListed(ctx, id, m.Target))
		}
	}
	return nil
}

// subpathInode reads sub's inode without following a symlink out of base.
func subpathInode(base, sub string) (uint64, error) {
	root, err := os.OpenRoot(base)
	if err != nil {
		return 0, err
	}
	defer func() { _ = root.Close() }()
	info, err := root.Lstat(sub)
	if err != nil {
		return 0, err
	}
	return fileInode(info)
}

// mountListed says whether Docker recorded the mount, which tells an engine
// that never made it from a container runtime that did not apply it.
func (d *Docker) mountListed(ctx context.Context, id ID, target string) string {
	info, err := d.cli.ContainerInspect(ctx, string(id), client.ContainerInspectOptions{})
	if err != nil {
		return "inspect the container: " + err.Error()
	}
	engine := info.Container.HostConfig.Runtime
	for _, m := range info.Container.Mounts {
		if m.Destination == target {
			return fmt.Sprintf("Docker lists the mount, so container runtime %q did not apply it", engine)
		}
	}
	return fmt.Sprintf("Docker did not mount it (container runtime %q)", engine)
}
