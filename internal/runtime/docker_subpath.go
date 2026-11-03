package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
)

// checkMountInodes confirms the container sees each subpath mount's own
// file or directory at its target, and each bind whose target checks (the
// labelInodeChecks JSON array) lists its source's own, by mount point and
// inode, never content. A mount the engine or container runtime leaves out,
// or that was detached, shows the container whatever the parent mount holds
// there instead: for a shared account's login, the launcher's own.
func (d *Docker) checkMountInodes(ctx context.Context, id ID, mounts []mount.Mount, checks string) error {
	var checked []string
	if checks != "" {
		if err := json.Unmarshal([]byte(checks), &checked); err != nil {
			return fmt.Errorf("runtime: read the container's %s label: %w", labelInodeChecks, err)
		}
	}
	for _, m := range mounts {
		var want uint64
		var source string
		var err error
		switch {
		case m.Type == mount.TypeVolume && m.VolumeOptions != nil && m.VolumeOptions.Subpath != "" && m.VolumeOptions.DriverConfig != nil:
			base, sub := m.VolumeOptions.DriverConfig.Options["device"], m.VolumeOptions.Subpath
			source = sub + " in " + base
			want, err = subpathInode(base, sub)
		case m.Type == mount.TypeBind && slices.Contains(checked, m.Target):
			source = m.Source
			var info os.FileInfo
			if info, err = os.Lstat(m.Source); err == nil {
				want, err = fileInode(info)
			}
		default:
			continue
		}
		if err != nil {
			return fmt.Errorf("runtime: check the mount at %s: %w", m.Target, err)
		}
		code, output, _, err := d.execCombined(ctx, id, []string{"/bin/sh", "-c", mountCheckScript, "aether-mount-check", m.Target}, "", "0:0")
		if err != nil {
			return fmt.Errorf("runtime: check the mount at %s: %w", m.Target, err)
		}
		lines := strings.Split(strings.TrimSpace(output), "\n")
		fields := strings.Fields(lines[0])
		if code != 0 || len(lines) != 2 || len(fields) == 0 {
			return fmt.Errorf("runtime: check the mount at %s: exit %d: %s", m.Target, code, strings.TrimSpace(output))
		}
		if lines[1] != "mounted" {
			return fmt.Errorf("runtime: %s is not a mount point in the container; %s", m.Target, d.mountListed(ctx, id, m.Target))
		}
		if fields[0] != strconv.FormatUint(want, 10) {
			return fmt.Errorf("runtime: %s in the container is inode %s, not %s (inode %d); %s",
				m.Target, fields[0], source, want, d.mountListed(ctx, id, m.Target))
		}
	}
	return nil
}

// mountCheckScript prints $1's inode, then whether the container's mount
// table has a mount point at $1: an inode number alone can repeat on
// another filesystem. The engine mounts where the image's links lead, and
// the table lists that path.
const mountCheckScript = `ls -di -- "$1" || exit 1
real=$(readlink -f -- "$1" 2>/dev/null) || real=$1
while read -r _ _ _ _ point _; do
	if [ "$point" = "$1" ] || [ "$point" = "$real" ]; then
		echo mounted
		exit 0
	fi
done < /proc/self/mountinfo
echo unmounted`

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
