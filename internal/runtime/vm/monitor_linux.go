package vm

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const (
	cloudHypervisor       = "cloud-hypervisor"
	virtiofsd             = "/usr/libexec/virtiofsd"
	virtiofsdReadyTimeout = 10 * time.Second
)

// Monitor runs in a VM's unit, whose private mount namespace keeps every
// bind it makes off the host: it composes the share, starts virtiofsd on
// it, then becomes Cloud Hypervisor, so the unit ends with the VM.
func Monitor(args []string) error {
	if len(args) != 1 || !filepath.IsAbs(args[0]) {
		return errors.New("usage: aether-server vm-monitor <vm directory>")
	}
	dir := args[0]
	l, err := readLaunch(dir)
	if err != nil {
		return err
	}
	for _, name := range []string{apiSocket, vsockSocket, fsSocket, eventsFile} {
		if err = os.Remove(filepath.Join(dir, name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("vm monitor: remove stale %s: %w", name, err)
		}
	}
	if len(l.Shares) > 0 {
		root := filepath.Join(dir, shareDir)
		if err = composeShare(root, l.Shares); err != nil {
			return err
		}
		if err = startVirtiofsd(filepath.Join(dir, fsSocket), root); err != nil {
			return err
		}
	}
	argv := append([]string{cloudHypervisor}, l.Args...)
	path, err := exec.LookPath(cloudHypervisor)
	if err != nil {
		return fmt.Errorf("vm monitor: %w", err)
	}
	return syscall.Exec(path, argv, os.Environ())
}

// composeShare mounts a small tmpfs at root and binds every share at
// root/<index>: nosuid and nodev always, read-only where asked, and
// recursively, like Docker's rbind. The tmpfs itself ends read-only.
func composeShare(root string, shares []share) error {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return fmt.Errorf("vm monitor: %w", err)
	}
	if err := unix.Mount("aether-share", root, "tmpfs", unix.MS_NOSUID|unix.MS_NODEV, "mode=0755,size=1m"); err != nil {
		return fmt.Errorf("vm monitor: mount share root: %w", err)
	}
	for i, s := range shares {
		if err := bindShare(filepath.Join(root, strconv.Itoa(i)), s); err != nil {
			return err
		}
	}
	if err := unix.MountSetattr(unix.AT_FDCWD, root, 0, &unix.MountAttr{Attr_set: unix.MOUNT_ATTR_RDONLY}); err != nil {
		return fmt.Errorf("vm monitor: make share root read-only: %w", err)
	}
	return nil
}

// bindShare resolves a subpath beneath its base without following a symlink
// out of it, as Docker's volume subpath does, because an agent may control
// the base's content.
func bindShare(target string, s share) error {
	fd, err := unix.Open(s.Source, unix.O_PATH|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("vm monitor: open %s: %w", s.Source, err)
	}
	defer func() { _ = unix.Close(fd) }()
	if s.Subpath != "" {
		sub, openErr := unix.Openat2(fd, s.Subpath, &unix.OpenHow{
			Flags:   unix.O_PATH | unix.O_CLOEXEC,
			Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_MAGICLINKS,
		})
		if openErr != nil {
			return fmt.Errorf("vm monitor: resolve %s beneath %s: %w", s.Subpath, s.Source, openErr)
		}
		_ = unix.Close(fd)
		fd = sub
	}
	var st unix.Stat_t
	if err = unix.Fstat(fd, &st); err != nil {
		return fmt.Errorf("vm monitor: stat %s: %w", s.Source, err)
	}
	if st.Mode&unix.S_IFMT == unix.S_IFDIR {
		err = os.Mkdir(target, 0o755)
	} else {
		err = os.WriteFile(target, nil, 0o644)
	}
	if err != nil {
		return fmt.Errorf("vm monitor: %w", err)
	}
	tree, err := unix.OpenTree(fd, "", unix.OPEN_TREE_CLONE|unix.OPEN_TREE_CLOEXEC|unix.AT_EMPTY_PATH|unix.AT_RECURSIVE)
	if err != nil {
		return fmt.Errorf("vm monitor: clone %s: %w", s.Source, err)
	}
	defer func() { _ = unix.Close(tree) }()
	attr := unix.MountAttr{Attr_set: unix.MOUNT_ATTR_NOSUID | unix.MOUNT_ATTR_NODEV, Propagation: unix.MS_PRIVATE}
	if s.ReadOnly {
		attr.Attr_set |= unix.MOUNT_ATTR_RDONLY
	}
	if err := unix.MountSetattr(tree, "", unix.AT_EMPTY_PATH|unix.AT_RECURSIVE, &attr); err != nil {
		return fmt.Errorf("vm monitor: set mount flags on %s: %w", s.Source, err)
	}
	if err := unix.MoveMount(tree, "", unix.AT_FDCWD, target, unix.MOVE_MOUNT_F_EMPTY_PATH); err != nil {
		return fmt.Errorf("vm monitor: bind %s: %w", s.Source, err)
	}
	return nil
}

// startVirtiofsd leaves virtiofsd as a child that outlives the exec into
// Cloud Hypervisor; the unit stops it with the VM.
func startVirtiofsd(socket, root string) error {
	cmd := exec.Command(virtiofsd, "--socket-path="+socket, "--shared-dir="+root,
		"--announce-submounts", "--xattr", "--inode-file-handles=prefer", "--sandbox=namespace")
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("vm monitor: start virtiofsd: %w", err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	deadline := time.After(virtiofsdReadyTimeout)
	for {
		if _, err := os.Stat(socket); err == nil {
			return nil
		}
		select {
		case err := <-exited:
			return fmt.Errorf("vm monitor: virtiofsd exited before it listened: %v", err)
		case <-deadline:
			return fmt.Errorf("vm monitor: virtiofsd did not listen on %s within %s", socket, virtiofsdReadyTimeout)
		case <-time.After(10 * time.Millisecond):
		}
	}
}
