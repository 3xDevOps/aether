package helper

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"github.com/3xDevOps/Aether/internal/guest"
	"golang.org/x/sys/unix"
)

const (
	// privateDir holds the helper's own mounts and sockets.
	privateDir = "/run/aether-guest"
	shareRoot  = privateDir + "/share"
	dataRoot   = privateDir + "/data"
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

// mountVolumes binds each configured mount from the share or the data disk.
// A mount that fails does not keep the others from being made.
func mountVolumes(mounts []guest.Mount) error {
	roots := map[guest.Volume]string{}
	var errs []error
	for v, root := range map[guest.Volume]string{guest.VolumeShare: shareRoot, guest.VolumeData: dataRoot} {
		if !slices.ContainsFunc(mounts, func(m guest.Mount) bool { return m.Volume == v }) {
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
	if v == guest.VolumeShare {
		return mountOnce(mountSpec{guest.ShareTag, root, "virtiofs", unix.MS_NOSUID | unix.MS_NODEV, ""})
	}
	device, _, err := diskBySerial("data")
	if err != nil {
		return err
	}
	return mountOnce(mountSpec{device, root, "ext4", 0, ""})
}

// bind opens the source beneath its volume, so a symlink planted in a
// persistent data disk cannot point a mount anywhere else.
func bind(root string, m guest.Mount) error {
	if m.Volume == guest.VolumeData {
		if err := createLike(root, m.Source, m.Target); err != nil {
			return err
		}
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
	if err := unix.Mount("/proc/self/fd/"+strconv.Itoa(fd), m.Target, "", unix.MS_BIND, ""); err != nil {
		return err
	}
	var flags uintptr
	if m.Volume == guest.VolumeShare {
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
