// Package helper is aether-internal guest, PID 1 of every session VM: it
// mounts the VM's filesystems, configures its network, relays its sockets
// to the host, reaps orphans and serves the API package guest's client
// speaks, over vsock. docs/vm-guest.md describes it.
package helper

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/3xDevOps/Aether/internal/guest"
	"github.com/mdlayher/vsock"
	"golang.org/x/sys/unix"
)

// forwarded are the signals PID 1 passes to the main process, as
// docker-init does. Go's own SIGURG is not among them.
var forwarded = []os.Signal{
	unix.SIGHUP, unix.SIGINT, unix.SIGQUIT, unix.SIGTERM, unix.SIGUSR1, unix.SIGUSR2, unix.SIGALRM, unix.SIGWINCH,
}

// startArg runs the helper binary as the first step of every spawned
// process. Go's ForkExec keeps one of the helper's Ps until the child's
// exec, and a VM may have only one: a child that entered a directory or
// executed a program on the file service, which the helper serves, would
// hang the VM. The child starts in / with an empty environment, so its Go
// runtime ignores the program's GOMEMLIMIT or GODEBUG. It sets no_new_privs
// on itself when asked, since Go cannot set it between fork and exec, then
// enters its working directory and executes the program.
const startArg = "start"

// enterRootArg runs the helper binary as the first step of a process
// spawned in another root: in its new mount namespace, still root, it makes
// that root its own with pivot_root, as a container runtime does, then
// drops to the process's user without new privileges and executes the
// program. A chroot would keep the program from making user namespaces,
// which Chromium's sandbox needs.
const enterRootArg = "enter-root"

// Main runs the helper with the arguments after "guest". It never returns
// as PID 1: a helper that cannot serve powers the VM off, and the kernel
// would panic if PID 1 exited.
func Main(args []string) int {
	if len(args) > 4 && args[0] == startArg {
		return execStart(args[1] == "true", args[2], args[3], args[4:])
	}
	if len(args) == 1 && args[0] == gitServiceArg {
		err := serveGit()
		slog.Error("guest: the git service stopped", "error", err)
		return 1
	}
	if len(args) > 6 && args[0] == enterRootArg {
		return execInRoot(args[1], args[2], args[3], args[4], args[5], args[6:])
	}
	if os.Getpid() != 1 {
		fmt.Fprintln(os.Stderr, "aether-internal guest: runs only as PID 1 of a session VM")
		return 2
	}
	err := serve()
	slog.Error("guest: the helper cannot serve; powering off", "error", err)
	unix.Sync()
	_ = unix.Reboot(unix.LINUX_REBOOT_CMD_POWER_OFF)
	select {}
}

// execStart runs with the spawning helper's status pipe on fd 3, which gets
// startMarker, then closes when the exec succeeds and otherwise gets its
// errno, and the program's environment on fd 4. no_new_privs belongs to
// one thread, so the exec stays on the thread that set it.
func execStart(noNewPrivs bool, dir, program string, argv []string) int {
	runtime.LockOSThread()
	status := os.NewFile(3, "exec status")
	env, err := started(status)
	if err == nil && noNewPrivs {
		err = unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0)
	}
	// The runtime's preemption signal can interrupt a chdir or execve that
	// waits on a FUSE file system such as the file service; nothing changed
	// then, so it is tried again.
	if err == nil {
		err = unix.Chdir(dir)
		for errors.Is(err, unix.EINTR) {
			err = unix.Chdir(dir)
		}
	}
	if err == nil {
		err = unix.Exec(program, argv, env)
		for errors.Is(err, unix.EINTR) {
			err = unix.Exec(program, argv, env)
		}
	}
	return execFailed(status, err)
}

// started writes startMarker to the status pipe and returns the program's
// environment, which the spawning helper sends on fd 4.
func started(status *os.File) ([]string, error) {
	syscall.CloseOnExec(3)
	syscall.CloseOnExec(4)
	if _, err := status.Write([]byte{startMarker}); err != nil {
		return nil, err
	}
	environ, err := io.ReadAll(os.NewFile(4, "environment"))
	if err != nil || len(environ) == 0 {
		return nil, err
	}
	return strings.Split(string(environ), "\x00"), nil
}

// execFailed reports a failed start's errno on the status pipe.
func execFailed(status *os.File, err error) int {
	errno, ok := err.(unix.Errno)
	if !ok {
		errno = unix.EINVAL
	}
	_, _ = fmt.Fprint(status, int(errno))
	return 126
}

// execInRoot reports like execStart, on fd 3, and takes the environment
// from fd 4 as it does.
func execInRoot(root, uid, gid, dir, program string, argv []string) int {
	runtime.LockOSThread()
	status := os.NewFile(3, "exec status")
	env, err := started(status)
	if err == nil {
		err = enterRoot(root)
	}
	if err == nil {
		err = unix.Chdir(dir)
	}
	u, uidErr := strconv.Atoi(uid)
	g, gidErr := strconv.Atoi(gid)
	if err == nil {
		err = errors.Join(uidErr, gidErr)
	}
	if err == nil {
		err = unix.Setgroups(nil)
	}
	if err == nil {
		err = unix.Setresgid(g, g, g)
	}
	if err == nil {
		err = unix.Setresuid(u, u, u)
	}
	if err == nil {
		err = unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0)
	}
	if err == nil {
		err = unix.Exec(program, argv, env)
	}
	return execFailed(status, err)
}

// enterRoot pivots the process's own mount namespace onto root and detaches
// the old root, so nothing of the VM's own filesystem stays reachable.
func enterRoot(root string) error {
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		return err
	}
	if err := unix.Chdir(root); err != nil {
		return err
	}
	if err := unix.PivotRoot(".", "."); err != nil {
		return err
	}
	return unix.Unmount(".", unix.MNT_DETACH)
}

func serve() error {
	var failures []error
	record := func(step string, err error) {
		if err != nil {
			slog.Error("guest: "+step, "error", err)
			failures = append(failures, fmt.Errorf("%s: %w", step, err))
		}
	}
	record(guest.RootStep, switchRoot())
	record("mount system filesystems", mountSystem())
	cmdline, err := os.ReadFile("/proc/cmdline")
	record("read the kernel command line", err)
	cfg, err := guest.ParseCmdline(string(cmdline))
	record("read the configuration", err)
	record("mount volumes", mountVolumes(cfg.Mounts))
	record("mount checkouts", mountCheckouts(cfg.Checkouts))
	record("configure the network", configureNetwork(cfg))
	cgroup, err := newSessionCgroup("/sys/fs/cgroup", len(cfg.Checkouts) > 0)
	record("create the session cgroup", err)
	var procsDir *os.File
	if cgroup != nil {
		procsDir = cgroup.procs
	}
	procs := newProcesses(procsDir)
	procs.mainExited = func() {
		endSession(cgroup)
		dropCaches()
	}
	go reapForever(procs)
	if len(cfg.Checkouts) > 0 {
		gitCgroup, gitErr := newGitCgroup("/sys/fs/cgroup")
		record("create the git service's cgroup", gitErr)
		if gitErr == nil {
			go superviseGit(procs, gitCgroup)
		}
	}
	go forwardSignals(procs)
	record("forward sockets", forwardSockets(cfg.Sockets))
	record("serve sockets", serveSockets(cfg.Serves))

	bootError := ""
	if len(failures) > 0 {
		bootError = failures[0].Error()
	}
	svc := newService(procs, cgroup, rand.Text(), bootError)
	svc.powerOff = func() error { return unix.Reboot(unix.LINUX_REBOOT_CMD_POWER_OFF) }
	svc.setClock = func(t time.Time) error {
		ts := unix.NsecToTimespec(t.UnixNano())
		return unix.ClockSettime(unix.CLOCK_REALTIME, &ts)
	}
	svc.freezer = &freezer{set: freezeRoot}
	svc.companion = &browser{}
	listener, err := vsock.Listen(guest.APIPort, nil)
	if err != nil {
		return fmt.Errorf("listen on vsock port %d: %w", guest.APIPort, err)
	}
	slog.Info("guest: serving", "port", guest.APIPort, "boot_error", bootError)
	return newServer(svc).Serve(hostOnly{listener})
}

// hostOnly accepts only the host: through vsock loopback a process in the
// VM could otherwise reach the helper and start processes as root.
type hostOnly struct{ net.Listener }

func (l hostOnly) Accept() (net.Conn, error) {
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		if addr, ok := conn.RemoteAddr().(*vsock.Addr); ok && addr.ContextID == vsock.Host {
			return conn, nil
		}
		slog.Warn("guest: refused an API connection from inside the VM", "from", conn.RemoteAddr())
		_ = conn.Close()
	}
}

func reapForever(procs *processes) {
	sigchld := make(chan os.Signal, 1)
	signal.Notify(sigchld, unix.SIGCHLD)
	for range sigchld {
		procs.reap()
	}
}

func forwardSignals(procs *processes) {
	signals := make(chan os.Signal, 16)
	signal.Notify(signals, forwarded...)
	for sig := range signals {
		if err := procs.signal(guest.MainID, sig.(unix.Signal)); err != nil && !errors.Is(err, errUnknown) && !errors.Is(err, errExited) {
			slog.Warn("guest: forward a signal to the main process", "signal", sig, "error", err)
		}
	}
}
