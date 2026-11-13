package helper

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/3xDevOps/Aether/internal/guest"
	"github.com/3xDevOps/Aether/internal/guest/guestpb"
	"golang.org/x/sys/unix"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The development browser's companion runs from the disk with serial
// "browser", mounted read-only under an overlay whose only write is the
// control directory's mountpoint, then remounted read-only, which becomes
// the root of its own mount namespace. Its /tmp, /dev/shm and control
// directory are fresh for every start.
const (
	browserDir     = privateDir + "/browser"
	browserRoot    = browserDir + "/root"
	browserCgroup  = "/sys/fs/cgroup" + sessionCgroupParent + "/browser"
	browserPids    = 512
	browserTmp     = 512 << 20
	browserStopped = 10 * time.Second
)

type browser struct {
	mu       sync.Mutex
	prepared bool
	cgroup   *os.File
	current  string
}

func (s *service) Browser(_ context.Context, req *guestpb.BrowserRequest) (*guestpb.BrowserReply, error) {
	if s.companion == nil {
		return nil, status.Error(codes.FailedPrecondition, "the helper is not PID 1")
	}
	var err error
	switch req.GetAction() {
	case guestpb.BrowserRequest_START:
		err = s.companion.start(s.procs, req)
	case guestpb.BrowserRequest_PAUSE:
		err = writeCgroup("cgroup.freeze", "1")
	case guestpb.BrowserRequest_RESUME:
		err = writeCgroup("cgroup.freeze", "0")
	case guestpb.BrowserRequest_STOP:
		err = s.companion.stop(s.procs)
	default:
		return nil, status.Errorf(codes.InvalidArgument, "unknown browser action %v", req.GetAction())
	}
	if err != nil {
		return nil, rpcError(err)
	}
	return &guestpb.BrowserReply{}, nil
}

func writeCgroup(name, value string) error {
	if err := os.WriteFile(filepath.Join(browserCgroup, name), []byte(value), 0); err != nil {
		return fmt.Errorf("browser cgroup %s: %w", name, err)
	}
	return nil
}

func (b *browser) start(procs *processes, req *guestpb.BrowserRequest) error {
	spec := processSpec(req.GetProcess())
	if spec.User == "" || spec.User == "0" || spec.User == "0:0" || spec.TTY != nil {
		return fmt.Errorf("%w: the browser runs as a numeric user other than root, without a terminal", errInvalid)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if p, err := procs.get(b.current); err == nil && !p.exited() {
		return fmt.Errorf("%w: the browser %s still runs", errExists, b.current)
	}
	if err := b.prepare(); err != nil {
		return err
	}
	cred, _, err := credential(spec.User)
	if err != nil {
		return err
	}
	limits := map[string]string{
		"memory.max": "max", "memory.swap.max": "0", "pids.max": strconv.Itoa(browserPids), "cpu.max": "max 100000", "cgroup.freeze": "0",
	}
	if m := req.GetMemoryBytes(); m > 0 {
		limits["memory.max"] = strconv.FormatUint(m, 10)
	}
	if c := req.GetCpuMillis(); c > 0 {
		limits["cpu.max"] = strconv.FormatUint(uint64(c)*100, 10) + " 100000"
	}
	for name, value := range limits {
		// A guest kernel without swap has no memory.swap.max.
		if _, statErr := os.Stat(filepath.Join(browserCgroup, name)); errors.Is(statErr, os.ErrNotExist) && name == "memory.swap.max" {
			continue
		}
		if err = writeCgroup(name, value); err != nil {
			return err
		}
	}
	if err = freshMounts(cred, req.GetShmBytes()); err != nil {
		return err
	}
	if _, err = procs.spawn(spawnSpec{ProcessSpec: spec, root: browserRoot, cgroup: b.cgroup, newIPC: true}); err != nil {
		return err
	}
	b.current = spec.ID
	return nil
}

// prepare mounts the browser's root once per boot.
func (b *browser) prepare() error {
	if b.prepared {
		return nil
	}
	device, _, err := diskBySerial("browser")
	if err != nil {
		return fmt.Errorf("the browser's disk: %w", err)
	}
	disk, rw := browserDir+"/disk", browserDir+"/rw"
	for _, m := range []mountSpec{
		{device, disk, "ext4", unix.MS_RDONLY | unix.MS_NOSUID | unix.MS_NODEV, "noload"},
		{"tmpfs", rw, "tmpfs", unix.MS_NOSUID | unix.MS_NODEV, "size=1m,mode=0700"},
	} {
		if err = mountOnce(m); err != nil {
			return err
		}
	}
	for _, d := range []string{rw + "/upper", rw + "/work", browserRoot} {
		if err = os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	if err = mountOnce(mountSpec{"overlay", browserRoot, "overlay", unix.MS_NOSUID | unix.MS_NODEV, "lowerdir=" + disk + ",upperdir=" + rw + "/upper,workdir=" + rw + "/work"}); err != nil {
		return err
	}
	if err = os.MkdirAll(browserRoot+"/aether-control", 0o755); err != nil {
		return err
	}
	for _, name := range []string{"resolv.conf", "hosts", "hostname"} {
		if err = touch(filepath.Join(browserRoot, "etc", name)); err != nil {
			return err
		}
		if err = unix.Mount("/etc/"+name, filepath.Join(browserRoot, "etc", name), "", unix.MS_BIND|unix.MS_RDONLY, ""); err != nil {
			return fmt.Errorf("bind /etc/%s into the browser's root: %w", name, err)
		}
	}
	if err = unix.Mount("", browserRoot, "", unix.MS_REMOUNT|unix.MS_BIND|unix.MS_RDONLY|unix.MS_NOSUID|unix.MS_NODEV, ""); err != nil {
		return fmt.Errorf("make the browser's root read-only: %w", err)
	}
	for _, m := range []mountSpec{
		{"proc", browserRoot + "/proc", "proc", unix.MS_NOSUID | unix.MS_NODEV | unix.MS_NOEXEC, "hidepid=invisible"},
		{"sysfs", browserRoot + "/sys", "sysfs", unix.MS_RDONLY | unix.MS_NOSUID | unix.MS_NODEV | unix.MS_NOEXEC, ""},
		{"tmpfs", browserRoot + "/dev", "tmpfs", unix.MS_NOSUID | unix.MS_NOEXEC, "size=64k,mode=0755"},
	} {
		if err = mountOnce(m); err != nil {
			return err
		}
	}
	for _, name := range []string{"null", "zero", "full", "random", "urandom", "tty"} {
		target := browserRoot + "/dev/" + name
		if err = touch(target); err != nil {
			return err
		}
		if err = unix.Mount("/dev/"+name, target, "", unix.MS_BIND, ""); err != nil {
			return fmt.Errorf("bind /dev/%s into the browser's root: %w", name, err)
		}
	}
	for link, target := range map[string]string{"fd": "/proc/self/fd", "stdin": "/proc/self/fd/0", "stdout": "/proc/self/fd/1", "stderr": "/proc/self/fd/2"} {
		if err = os.Symlink(target, browserRoot+"/dev/"+link); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
	}
	if err = os.MkdirAll(browserRoot+"/dev/shm", 0o1777); err != nil {
		return err
	}
	for _, dir := range []string{"/sys/fs/cgroup", filepath.Dir(browserCgroup)} {
		if err = os.WriteFile(filepath.Join(dir, "cgroup.subtree_control"), []byte("+memory +pids +cpu"), 0); err != nil {
			return fmt.Errorf("enable the browser's cgroup controllers in %s: %w", dir, err)
		}
	}
	if err = os.MkdirAll(browserCgroup, 0o755); err != nil {
		return err
	}
	if b.cgroup, err = os.Open(browserCgroup); err != nil {
		return err
	}
	b.prepared = true
	return nil
}

// freshMounts gives a new companion empty /tmp, /dev/shm and control
// directory, the last owned by its user.
func freshMounts(cred *syscall.Credential, shm uint64) error {
	control := filepath.Dir(guest.BrowserSocket)
	for _, target := range []string{browserRoot + "/tmp", browserRoot + "/dev/shm", browserRoot + "/aether-control", control} {
		if mounted, err := isMountPoint(target); err != nil {
			return err
		} else if mounted {
			if err = unix.Unmount(target, unix.MNT_DETACH); err != nil {
				return fmt.Errorf("unmount the last browser's %s: %w", target, err)
			}
		}
	}
	if err := os.MkdirAll(control, 0o700); err != nil {
		return err
	}
	if err := unix.Mount("tmpfs", control, "tmpfs", unix.MS_NOSUID|unix.MS_NODEV|unix.MS_NOEXEC, "size=64k,mode=0700"); err != nil {
		return fmt.Errorf("mount the browser's control directory: %w", err)
	}
	if cred != nil {
		if err := os.Chown(control, int(cred.Uid), int(cred.Gid)); err != nil {
			return err
		}
	}
	for _, m := range []mountSpec{
		{"tmpfs", browserRoot + "/tmp", "tmpfs", unix.MS_NOSUID | unix.MS_NODEV, "size=" + strconv.Itoa(browserTmp) + ",mode=1777"},
		{"tmpfs", browserRoot + "/dev/shm", "tmpfs", unix.MS_NOSUID | unix.MS_NODEV | unix.MS_NOEXEC, "size=" + strconv.FormatUint(max(shm, 1<<20), 10) + ",mode=1777"},
		{control, browserRoot + "/aether-control", "", unix.MS_BIND, ""},
	} {
		if err := unix.Mount(m.source, m.target, m.fstype, m.flags, m.data); err != nil {
			return fmt.Errorf("mount %s for the browser: %w", m.target, err)
		}
	}
	return nil
}

// stop kills every process in the browser's cgroup and waits for the
// companion's exit.
func (b *browser) stop(procs *processes) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.prepared {
		return nil
	}
	if err := writeCgroup("cgroup.freeze", "0"); err != nil {
		return err
	}
	if err := writeCgroup("cgroup.kill", "1"); err != nil {
		return err
	}
	p, err := procs.get(b.current)
	if err != nil {
		return nil
	}
	select {
	case <-p.done:
		return nil
	case <-time.After(browserStopped):
		return fmt.Errorf("the browser %s did not end within %s of being killed", b.current, browserStopped)
	}
}

func touch(path string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDONLY, 0o644)
	if err != nil {
		return err
	}
	return f.Close()
}
