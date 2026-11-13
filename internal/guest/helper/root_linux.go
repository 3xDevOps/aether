package helper

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"github.com/3xDevOps/Aether/internal/guest"
	"github.com/3xDevOps/Aether/internal/guest/home"
	"github.com/mdlayher/vsock"
	"golang.org/x/sys/unix"
)

const (
	// privateDir holds the helper's own mounts and sockets.
	privateDir = "/run/aether-guest"
	shareRoot  = privateDir + "/share"
	dataRoot   = privateDir + "/data"
	homeRoot   = privateDir + "/home"
	cacheRoot  = privateDir + "/cache"
	stateRoot  = privateDir + "/state"
	// selfExe is the helper's executable, on the helper disk, which stays
	// mounted beneath the root the helper switched to.
	selfExe = "/proc/self/exe"
)

type mountSpec struct {
	source, target, fstype string
	flags                  uintptr
	data                   string
}

// switchRoot makes the disk whose virtio serial is "root" the root, unless
// the kernel booted it as the root already. The kernel boots the read-only
// helper disk, whose /mnt is empty for this. The helper stays PID 1, so it
// moves the root as switch_root does instead of executing a new init.
func switchRoot() error {
	for _, m := range []mountSpec{
		{"proc", "/proc", "proc", unix.MS_NOSUID | unix.MS_NODEV | unix.MS_NOEXEC, ""},
		{"devtmpfs", "/dev", "devtmpfs", unix.MS_NOSUID, "mode=0755"},
		{"sysfs", "/sys", "sysfs", unix.MS_NOSUID | unix.MS_NODEV | unix.MS_NOEXEC, ""},
	} {
		if err := mountOnce(m); err != nil {
			return err
		}
	}
	device, number, err := diskBySerial("root")
	if err != nil {
		return err
	}
	var root unix.Stat_t
	if err := unix.Stat("/", &root); err != nil {
		return err
	}
	if root.Dev == number {
		return nil
	}
	const newRoot = "/mnt"
	if err := unix.Mount(device, newRoot, "ext4", 0, ""); err != nil {
		return fmt.Errorf("mount %s at %s: %w", device, newRoot, err)
	}
	for _, dir := range []string{"/dev", "/proc", "/sys"} {
		if err := os.MkdirAll(newRoot+dir, 0o755); err != nil {
			return err
		}
		if err := unix.Mount(dir, newRoot+dir, "", unix.MS_MOVE, ""); err != nil {
			return fmt.Errorf("move %s: %w", dir, err)
		}
	}
	if err := unix.Chdir(newRoot); err != nil {
		return err
	}
	if err := unix.Mount(".", "/", "", unix.MS_MOVE, ""); err != nil {
		return fmt.Errorf("move the root disk to /: %w", err)
	}
	if err := unix.Chroot("."); err != nil {
		return err
	}
	return unix.Chdir("/")
}

// mountSystem mounts what a container runtime gives every container.
func mountSystem() error {
	for _, m := range []mountSpec{
		{"devpts", "/dev/pts", "devpts", unix.MS_NOSUID | unix.MS_NOEXEC, "newinstance,ptmxmode=0666,mode=0620,gid=5"},
		{"shm", "/dev/shm", "tmpfs", unix.MS_NOSUID | unix.MS_NODEV, "mode=1777"},
		{"cgroup2", "/sys/fs/cgroup", "cgroup2", unix.MS_NOSUID | unix.MS_NODEV | unix.MS_NOEXEC, ""},
		{"tmpfs", "/run", "tmpfs", unix.MS_NOSUID | unix.MS_NODEV, "mode=0755"},
	} {
		if err := mountOnce(m); err != nil {
			return err
		}
	}
	for link, target := range map[string]string{
		"/dev/fd": "/proc/self/fd", "/dev/stdin": "/proc/self/fd/0", "/dev/stdout": "/proc/self/fd/1",
		"/dev/stderr": "/proc/self/fd/2", "/dev/ptmx": "pts/ptmx",
	} {
		_ = os.Remove(link)
		if err := os.Symlink(target, link); err != nil {
			return err
		}
	}
	return os.MkdirAll(privateDir, 0o700)
}

func mountOnce(m mountSpec) error {
	mounted, err := isMountPoint(m.target)
	if err != nil || mounted {
		return err
	}
	if err := os.MkdirAll(m.target, 0o755); err != nil {
		return err
	}
	if err := unix.Mount(m.source, m.target, m.fstype, m.flags, m.data); err != nil {
		return fmt.Errorf("mount %s at %s: %w", m.fstype, m.target, err)
	}
	return nil
}

// isMountPoint reads /proc/self/mountinfo, or reports nothing mounted
// before /proc is.
func isMountPoint(target string) (bool, error) {
	data, err := os.ReadFile("/proc/self/mountinfo")
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		if fields := strings.Fields(scanner.Text()); len(fields) > 4 && fields[4] == target {
			return true, nil
		}
	}
	return false, scanner.Err()
}

// diskBySerial finds a virtio disk by the serial the VM monitor gave it.
func diskBySerial(serial string) (string, uint64, error) {
	paths, err := filepath.Glob("/sys/block/vd*/serial")
	if err != nil {
		return "", 0, err
	}
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil || strings.TrimSpace(string(data)) != serial {
			continue
		}
		dir := filepath.Dir(p)
		dev, err := os.ReadFile(filepath.Join(dir, "dev"))
		if err != nil {
			return "", 0, err
		}
		major, minor, ok := strings.Cut(strings.TrimSpace(string(dev)), ":")
		maj, majErr := strconv.ParseUint(major, 10, 32)
		mnr, mnrErr := strconv.ParseUint(minor, 10, 32)
		if !ok || majErr != nil || mnrErr != nil {
			return "", 0, fmt.Errorf("%s/dev holds %q", dir, dev)
		}
		return "/dev/" + filepath.Base(dir), unix.Mkdev(uint32(maj), uint32(mnr)), nil
	}
	return "", 0, fmt.Errorf("no disk has serial %q", serial)
}

// mountVolumes binds each configured mount from its volume. A mount that
// fails does not keep the others from being made.
func mountVolumes(mounts []guest.Mount) error {
	roots := map[guest.Volume]string{guest.VolumeHelper: selfExe}
	var errs []error
	for v, root := range map[guest.Volume]string{
		guest.VolumeShare: shareRoot, guest.VolumeData: dataRoot, guest.VolumeHome: homeRoot, guest.VolumeCache: cacheRoot,
		guest.VolumeState: stateRoot,
	} {
		if !slices.ContainsFunc(mounts, func(m guest.Mount) bool { return m.Volume == v || (v == guest.VolumeData && m.Overlay) }) {
			continue
		}
		if err := mountVolume(v, root); err != nil {
			errs = append(errs, fmt.Errorf("mount the %s volume: %w", v, err))
			continue
		}
		roots[v] = root
	}
	for _, m := range mounts {
		root, ok := roots[m.Volume]
		if !ok {
			continue
		}
		if err := bind(root, m); err != nil {
			errs = append(errs, fmt.Errorf("mount %s %s at %s: %w", m.Volume, m.Source, m.Target, err))
		}
	}
	return errors.Join(errs...)
}

func mountVolume(v guest.Volume, root string) error {
	switch v {
	case guest.VolumeShare:
		return mountOnce(mountSpec{guest.ShareTag, root, "virtiofs", unix.MS_NOSUID | unix.MS_NODEV, ""})
	case guest.VolumeHome:
		if err := os.MkdirAll(root, 0o755); err != nil {
			return err
		}
		return home.Mount(root, func(context.Context) (net.Conn, error) { return vsock.Dial(vsock.Host, home.Port, nil) })
	}
	device, _, err := diskBySerial(string(v))
	if err != nil {
		return err
	}
	if err = mountOnce(mountSpec{device, root, "ext4", 0, ""}); err != nil || v != guest.VolumeCache {
		return err
	}
	// Any user of the session writes the caches, as any writes /tmp, and
	// finds no lost+found it cannot read, as in a container's.
	if err = os.Remove(filepath.Join(root, "lost+found")); err != nil && !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, unix.ENOTEMPTY) {
		return err
	}
	return os.Chmod(root, os.ModeSticky|0o777)
}

// bind opens the source beneath its volume, so a symlink planted in a
// persistent data disk cannot point a mount anywhere else.
func bind(root string, m guest.Mount) error {
	switch m.Volume {
	case guest.VolumeData, guest.VolumeState:
		if err := createLike(root, m.Source, m.Target); err != nil {
			return err
		}
	case guest.VolumeHelper:
		return bindSelf(m.Target)
	}
	rootFD, err := unix.Open(root, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(rootFD) }()
	fd, err := unix.Openat2(rootFD, m.Source, &unix.OpenHow{Flags: unix.O_PATH | unix.O_CLOEXEC, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_MAGICLINKS})
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(fd) }()
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return err
	}
	if err := ensureTarget(m.Target, st.Mode&unix.S_IFMT == unix.S_IFDIR); err != nil {
		return err
	}
	if m.Overlay {
		return overlay(fd, &st, m)
	}
	if err := unix.Mount("/proc/self/fd/"+strconv.Itoa(fd), m.Target, "", unix.MS_BIND, ""); err != nil {
		return err
	}
	var flags uintptr
	if m.Volume == guest.VolumeShare || m.Volume == guest.VolumeHome {
		flags |= unix.MS_NOSUID | unix.MS_NODEV
	}
	if m.ReadOnly {
		flags |= unix.MS_RDONLY
	}
	if flags == 0 {
		return nil
	}
	return unix.Mount("", m.Target, "", unix.MS_BIND|unix.MS_REMOUNT|flags, "")
}

// createLike creates a missing data-disk directory with the mode and owner
// its target has in the image, so /tmp keeps its sticky bit. It works
// beneath the volume, which a symlink on the persistent disk cannot leave.
func createLike(root, source, target string) error {
	volume, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer func() { _ = volume.Close() }()
	if _, err := volume.Lstat(source); err == nil || !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	mode, uid, gid := os.FileMode(0o755), 0, 0
	if info, err := os.Stat(target); err == nil && info.IsDir() {
		st := info.Sys().(*syscall.Stat_t)
		mode, uid, gid = info.Mode()&(os.ModePerm|os.ModeSticky|os.ModeSetgid), int(st.Uid), int(st.Gid)
	}
	if err := volume.MkdirAll(source, 0o755); err != nil {
		return err
	}
	return errors.Join(volume.Chown(source, uid, gid), volume.Chmod(source, mode))
}

func ensureTarget(target string, dir bool) error {
	if dir {
		return os.MkdirAll(target, 0o755)
	}
	if _, err := os.Stat(target); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	return f.Close()
}

// bindSelf binds the helper's executable read-only at target: the server
// binary a session runs as aether-internal, the same one the VM booted with
// even after the host's was replaced by an update.
func bindSelf(target string) error {
	if err := ensureTarget(target, false); err != nil {
		return err
	}
	if err := unix.Mount(selfExe, target, "", unix.MS_BIND, ""); err != nil {
		return err
	}
	return unix.Mount("", target, "", unix.MS_BIND|unix.MS_REMOUNT|unix.MS_RDONLY|unix.MS_NODEV, "")
}

// overlay mounts lower, a home entry, read-only beneath a writable layer
// at overlay/<source> on the data disk, as the Isolated tier's home is in a
// container. The layer's root takes lower's owner and mode, which the
// merged root shows.
func overlay(lower int, st *unix.Stat_t, m guest.Mount) error {
	data, err := os.OpenRoot(dataRoot)
	if err != nil {
		return err
	}
	defer func() { _ = data.Close() }()
	dir := "overlay/" + m.Source
	for _, sub := range []string{"upper", "work"} {
		if err = data.MkdirAll(dir+"/"+sub, 0o700); err != nil {
			return err
		}
	}
	if err = errors.Join(data.Chown(dir+"/upper", int(st.Uid), int(st.Gid)), data.Chmod(dir+"/upper", os.FileMode(st.Mode&0o7777))); err != nil {
		return err
	}
	dataFD, err := unix.Open(dataRoot, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(dataFD) }()
	layers := make([]int, 2)
	for i, sub := range []string{"upper", "work"} {
		fd, err := unix.Openat2(dataFD, dir+"/"+sub, &unix.OpenHow{
			Flags: unix.O_PATH | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS,
		})
		if err != nil {
			return err
		}
		defer func() { _ = unix.Close(fd) }()
		layers[i] = fd
	}
	options := fmt.Sprintf("lowerdir=/proc/self/fd/%d,upperdir=/proc/self/fd/%d,workdir=/proc/self/fd/%d", lower, layers[0], layers[1])
	return unix.Mount("overlay", m.Target, "overlay", unix.MS_NOSUID|unix.MS_NODEV, options)
}
