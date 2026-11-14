package vm

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/3xDevOps/Aether/internal/guest/home"
)

const (
	cloudHypervisor       = "cloud-hypervisor"
	virtiofsd             = "/usr/libexec/virtiofsd"
	virtiofsdReadyTimeout = 10 * time.Second
	consoleFlushTimeout   = 2 * time.Second
)

// Monitor runs in a VM's unit, whose private mount namespace keeps every
// bind it makes off the host: it composes the share and starts virtiofsd on
// it, serves the file service, and runs Cloud Hypervisor until the VM ends,
// which ends the unit. The file service is the monitor itself, so a failure
// of it ends the VM rather than leave it without its home.
func Monitor(args []string) error {
	if len(args) != 1 || !filepath.IsAbs(args[0]) {
		return errors.New("usage: aether-server vm-monitor <vm directory>")
	}
	dir := args[0]
	l, err := readLaunch(dir)
	if err != nil {
		return err
	}
	for _, name := range []string{apiSocket, vsockSocket, fsSocket, eventsFile, homeSocket, consolePipe} {
		if err = os.Remove(filepath.Join(dir, name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("vm monitor: remove stale %s: %w", name, err)
		}
	}
	flush, err := drainConsole(dir)
	if err != nil {
		return err
	}
	if len(l.Shares) > 0 {
		root := filepath.Join(dir, shareDir)
		if err = composeShare(root, l.Shares); err != nil {
			return err
		}
		if err = startVirtiofsd(filepath.Join(dir, fsSocket), root); err != nil {
			return err
		}
		if err = dropShareCaps(); err != nil {
			return err
		}
	}
	served := make(chan error, 1)
	if l.Home != nil {
		listener, listenErr := net.Listen("unix", filepath.Join(dir, homeSocket))
		if listenErr != nil {
			return fmt.Errorf("vm monitor: listen for the file service: %w", listenErr)
		}
		go func() { served <- home.Serve(listener, *l.Home) }()
	}
	return runHypervisor(l.Args, served, flush)
}

// runHypervisor runs Cloud Hypervisor and exits as it does. The unit's stop
// signals every process in it, so the monitor only waits for its child.
func runHypervisor(args []string, served <-chan error, flush func()) error {
	signal.Notify(make(chan os.Signal, 1), unix.SIGTERM, unix.SIGINT, unix.SIGHUP)
	cmd := exec.Command(cloudHypervisor, args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("vm monitor: %w", err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	select {
	case err := <-served:
		_ = cmd.Process.Kill()
		<-exited
		flush()
		return fmt.Errorf("vm monitor: the file service stopped: %w", err)
	case err := <-exited:
		flush()
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			if status, ok := exit.Sys().(syscall.WaitStatus); ok && status.Signaled() {
				os.Exit(128 + int(status.Signal()))
			}
			os.Exit(exit.ExitCode())
		}
		return err
	}
}

// drainConsole makes the FIFO Cloud Hypervisor writes the serial console
// to, and copies what arrives into console.log. Output the host cannot
// write is dropped rather than left to stall the guest. flush, once Cloud
// Hypervisor has exited, waits for the drain to copy what is left.
func drainConsole(dir string) (flush func(), err error) {
	pipe := filepath.Join(dir, consolePipe)
	if err := unix.Mkfifo(pipe, 0o600); err != nil {
		return nil, fmt.Errorf("vm monitor: make %s: %w", pipe, err)
	}
	fifo, err := os.OpenFile(pipe, os.O_RDONLY|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("vm monitor: %w", err)
	}
	// keep holds the FIFO open, so it reaches end of file only once flush
	// closes keep and Cloud Hypervisor has closed it too.
	keep, err := os.OpenFile(pipe, os.O_WRONLY, 0)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("vm monitor: %w", err), fifo.Close())
	}
	log, err := openConsoleLog(filepath.Join(dir, consoleLog))
	if err != nil {
		return nil, errors.Join(err, keep.Close(), fifo.Close())
	}
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		buf := make([]byte, 32<<10)
		for {
			n, err := fifo.Read(buf)
			if n > 0 {
				_, _ = log.Write(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()
	return func() {
		_ = keep.Close()
		select {
		case <-drained:
		case <-time.After(consoleFlushTimeout):
		}
	}, nil
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

// shareCapBits are shareCaps, in the order they can be dropped: CAP_SETPCAP
// last, since dropping from the bounding set takes it.
var shareCapBits = []uintptr{unix.CAP_SYS_ADMIN, unix.CAP_SETUID, unix.CAP_SETGID, unix.CAP_MKNOD, unix.CAP_SETFCAP, unix.CAP_SETPCAP}

// dropShareCaps takes the share's capabilities off every thread of the
// monitor once its mounts are made and virtiofsd runs, so neither Cloud
// Hypervisor nor the file service could undo the unit's mounts.
func dropShareCaps() error {
	for _, c := range shareCapBits {
		if _, _, errno := syscall.AllThreadsSyscall(unix.SYS_PRCTL, unix.PR_CAPBSET_DROP, c, 0); errno != 0 {
			return fmt.Errorf("vm monitor: drop capability %d from the bounding set: %w", c, errno)
		}
	}
	hdr := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
	var data [2]unix.CapUserData
	if err := unix.Capget(&hdr, &data[0]); err != nil {
		return fmt.Errorf("vm monitor: read capabilities: %w", err)
	}
	for _, c := range shareCapBits {
		bit := uint32(1) << (c % 32)
		data[c/32].Effective &^= bit
		data[c/32].Permitted &^= bit
		data[c/32].Inheritable &^= bit
	}
	if _, _, errno := syscall.AllThreadsSyscall(unix.SYS_CAPSET, uintptr(unsafe.Pointer(&hdr)), uintptr(unsafe.Pointer(&data[0])), 0); errno != 0 {
		return fmt.Errorf("vm monitor: drop the share's capabilities: %w", errno)
	}
	return nil
}

// startVirtiofsd leaves virtiofsd as a child that outlives the exec into
// Cloud Hypervisor; the unit stops it with the VM.
func startVirtiofsd(socket, root string) error {
	// --modcaps drops virtiofsd's CAP_MKNOD and CAP_SETFCAP so a guest cannot
	// create a host device node or a file-capability xattr through the share.
	// It cannot strip set-user-ID bits; a nosuid data dir covers those, as
	// under Docker (docs/security.md).
	cmd := exec.Command(virtiofsd, "--socket-path="+socket, "--shared-dir="+root,
		"--announce-submounts", "--xattr", "--modcaps=-mknod:-setfcap",
		"--inode-file-handles=prefer", "--sandbox=namespace")
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
