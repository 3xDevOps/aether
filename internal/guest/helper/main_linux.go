// Package helper is aether-internal guest, PID 1 of every session VM: it
// mounts the VM's filesystems, configures its network, relays its sockets
// to the host, reaps orphans and serves the API package guest's client
// speaks, over vsock. docs/vm-guest.md describes it.
package helper

import (
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
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

// noNewPrivsArg runs the helper binary as the first step of a process
// spawned with NoNewPrivileges: Go cannot set no_new_privs between fork and
// exec, so the child sets it on itself and executes the program.
const noNewPrivsArg = "no-new-privs"

// Main runs the helper with the arguments after "guest". It never returns
// as PID 1: a helper that cannot serve powers the VM off, and the kernel
// would panic if PID 1 exited.
func Main(args []string) int {
	if len(args) > 2 && args[0] == noNewPrivsArg {
		return execNoNewPrivs(args[1], args[2:])
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

func execNoNewPrivs(program string, argv []string) int {
	err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0)
	if err == nil {
		err = unix.Exec(program, argv, os.Environ())
	}
	fmt.Fprintf(os.Stderr, "aether-internal guest: %s: %v\n", program, err)
	return 126
}

func serve() error {
	var failures []error
	record := func(step string, err error) {
		if err != nil {
			slog.Error("guest: "+step, "error", err)
			failures = append(failures, fmt.Errorf("%s: %w", step, err))
		}
	}
	record("switch to the root disk", switchRoot())
	record("mount system filesystems", mountSystem())
	cmdline, err := os.ReadFile("/proc/cmdline")
	record("read the kernel command line", err)
	cfg, err := guest.ParseCmdline(string(cmdline))
	record("read the configuration", err)
	record("mount volumes", mountVolumes(cfg.Mounts))
	record("configure the network", configureNetwork(cfg))
	cgroup, err := newSessionCgroup("/sys/fs/cgroup")
	record("create the session cgroup", err)
	var procsDir *os.File
	if cgroup != nil {
		procsDir = cgroup.procs
	}
	procs := newProcesses(procsDir)
	go reapForever(procs)
	go forwardSignals(procs)
	record("forward sockets", forwardSockets(cfg.Sockets))

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
