package helper

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/3xDevOps/Aether/internal/guest"
	"github.com/mdlayher/vsock"
	"golang.org/x/sys/unix"
)

// forward is one socket the helper relays to the host.
type forward struct {
	guest.Socket
	// path is where the helper listens: Path, or with overlay a socket of its
	// own that it binds over Path.
	path    string
	overlay bool
	failing bool

	// mu guards listener and the bind over Path.
	mu sync.Mutex
	// listener is nil while the host socket does not answer.
	listener net.Listener
}

// forwardSockets listens on every configured socket and relays each
// connection to its vsock port on the host. A path that is an entry of a
// read-only share, such as a host socket the share shows but cannot carry,
// gets the helper's socket bound over it, and bound again whenever the
// share drops that bind: virtio-fs drops it once the host replaces the
// entry, as the server does on every restart.
func forwardSockets(sockets []guest.Socket) error {
	var overlaid []*forward
	var errs []error
	for _, s := range sockets {
		f := &forward{Socket: s}
		if err := f.listen(); err != nil {
			errs = append(errs, fmt.Errorf("socket %s: %w", s.Path, err))
			continue
		}
		if f.overlay {
			overlaid = append(overlaid, f)
		}
	}
	if len(overlaid) > 0 {
		go keepBound(overlaid)
	}
	return errors.Join(errs...)
}

func (f *forward) listen() error {
	if err := os.MkdirAll(filepath.Dir(f.Path), 0o755); err != nil && !errors.Is(err, syscall.EROFS) {
		return err
	}
	f.path = f.Path
	if _, err := os.Lstat(f.Path); err == nil {
		if err := os.Remove(f.Path); err != nil {
			f.path = filepath.Join(privateDir, "sockets", strconv.FormatUint(uint64(f.Port), 10)+".sock")
			if err := os.MkdirAll(filepath.Dir(f.path), 0o755); err != nil {
				return err
			}
			f.overlay = true
		}
	}
	return f.open()
}

// open listens on a fresh socket at path. Its file outlives the listener,
// so a connection while the listener is closed is refused, as one to a
// stopped server's socket is.
func (f *forward) open() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.overlay && f.bound() {
		if err := unix.Unmount(f.Path, unix.MNT_DETACH); err != nil {
			return fmt.Errorf("unbind the previous socket: %w", err)
		}
	}
	if err := os.Remove(f.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	listener, err := net.Listen("unix", f.path)
	if err != nil {
		return err
	}
	listener.(*net.UnixListener).SetUnlinkOnClose(false)
	if err := os.Chmod(f.path, 0o666); err != nil {
		_ = listener.Close()
		return err
	}
	if f.overlay {
		if err := f.bind(); err != nil {
			_ = listener.Close()
			return err
		}
	}
	f.listener = listener
	go f.serve(listener)
	return nil
}

func (f *forward) bind() error {
	return unix.Mount(f.path, f.Path, "", unix.MS_BIND, "")
}

// bound reports whether Path is still the helper's socket.
func (f *forward) bound() bool {
	var path, own unix.Stat_t
	return unix.Stat(f.Path, &path) == nil && unix.Stat(f.path, &own) == nil && path.Dev == own.Dev && path.Ino == own.Ino
}

// keepBound rebinds a dropped socket on every mount table change, and
// retries while the host's entry is missing. A mount table change marks
// mountinfo once per open file, so the file stays out of Go's poller, which
// would take the mark first.
func keepBound(forwards []*forward) {
	mountinfo, err := unix.Open("/proc/self/mountinfo", unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		slog.Error("guest: watch mounts", "error", err)
		return
	}
	defer func() { _ = unix.Close(mountinfo) }()
	timeout := -1
	for {
		fds := []unix.PollFd{{Fd: int32(mountinfo), Events: unix.POLLPRI}}
		if _, err := unix.Poll(fds, timeout); err != nil && !errors.Is(err, unix.EINTR) {
			slog.Error("guest: watch mounts", "error", err)
			return
		}
		timeout = -1
		for _, f := range forwards {
			if err := f.rebind(); err != nil {
				timeout = int((100 * time.Millisecond).Milliseconds())
			}
		}
	}
}

func (f *forward) rebind() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.bound() {
		return nil
	}
	err := f.bind()
	if err != nil && !f.failing {
		slog.Warn("guest: bind a socket again", "path", f.Path, "error", err)
	}
	f.failing = err != nil
	return err
}

func (f *forward) serve(listener net.Listener) {
	for {
		conn, err := listener.Accept()
		if errors.Is(err, net.ErrClosed) {
			return
		}
		if err != nil {
			slog.Warn("guest: accept on a forwarded socket", "path", f.Path, "error", err)
			time.Sleep(100 * time.Millisecond)
			continue
		}
		go f.relay(conn)
	}
}

func (f *forward) relay(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	var host net.Conn
	host, err := vsock.Dial(vsock.Host, f.Port, nil)
	if err != nil {
		f.hostDown()
		if host, err = awaitHost(conn, f.Port); err != nil {
			slog.Warn("guest: reach host socket", "path", f.Path, "port", f.Port, "error", err)
			return
		}
	}
	defer func() { _ = host.Close() }()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); splice(host, conn) }()
	go func() { defer wg.Done(); splice(conn, host) }()
	wg.Wait()
}

// hostDown closes the listener once the host socket stops answering, as
// while the server restarts, so new connections are refused and the
// coordination client retries them, and listens again once the host
// answers.
func (f *forward) hostDown() {
	f.mu.Lock()
	listener := f.listener
	f.listener = nil
	f.mu.Unlock()
	if listener == nil {
		return
	}
	_ = listener.Close()
	go func() {
		for delay := 50 * time.Millisecond; ; delay = min(2*delay, time.Second) {
			if probe, err := vsock.Dial(vsock.Host, f.Port, nil); err == nil {
				_ = probe.Close()
				break
			}
			time.Sleep(delay)
		}
		for {
			err := f.open()
			if err == nil {
				return
			}
			slog.Warn("guest: listen on a forwarded socket again", "path", f.Path, "error", err)
			time.Sleep(time.Second)
		}
	}()
}

// hostWait bounds how long a connection accepted before the host stopped
// answering waits for it.
const hostWait = 60 * time.Second

var errHungUp = errors.New("the client in the VM hung up")

// awaitHost holds a connection that was accepted before the host stopped
// answering until the host answers again. A client that hangs up first,
// such as one whose call timed out, never has its request reach the server.
func awaitHost(conn net.Conn, port uint32) (net.Conn, error) {
	raw, err := conn.(syscall.Conn).SyscallConn()
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(hostWait)
	for delay := 50 * time.Millisecond; time.Now().Before(deadline); delay = min(2*delay, time.Second) {
		if hungUp(raw, delay) {
			return nil, errHungUp
		}
		if host, err := vsock.Dial(vsock.Host, port, nil); err == nil {
			return host, nil
		}
	}
	return nil, fmt.Errorf("the host socket did not answer within %s", hostWait)
}

// hungUp waits up to wait for the peer to close the connection. A peer that
// only closed its writing side still waits for a reply.
func hungUp(raw syscall.RawConn, wait time.Duration) bool {
	gone := false
	_ = raw.Control(func(fd uintptr) {
		fds := []unix.PollFd{{Fd: int32(fd)}}
		n, _ := unix.Poll(fds, int(wait.Milliseconds()))
		gone = n > 0 && fds[0].Revents&(unix.POLLHUP|unix.POLLERR) != 0
	})
	return gone
}

type halfCloser interface {
	net.Conn
	CloseWrite() error
}

// splice copies one direction and passes its end on, so a protocol that
// half-closes still sees its reply.
func splice(dst, src net.Conn) {
	_, _ = io.Copy(dst, src)
	if hc, ok := dst.(halfCloser); ok {
		_ = hc.CloseWrite()
	} else {
		_ = dst.Close()
	}
}
