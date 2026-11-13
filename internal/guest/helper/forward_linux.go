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
	listener net.Listener
	// overlay is the helper's own socket bound over Path, empty when the
	// helper could create Path itself.
	overlay string
	failing bool
}

// forwardSockets listens on every configured socket and relays each
// connection to its vsock port on the host. A path that is an entry of a
// read-only share, such as a host socket the share shows but cannot carry,
// gets the helper's socket bound over it, and bound again whenever the
// share drops that bind: virtio-fs drops it once the host replaces the
// entry, as the server does on every restart.
func forwardSockets(sockets []guest.Socket) error {
	var overlaid []*forward
	for _, s := range sockets {
		f := &forward{Socket: s}
		if err := f.listen(); err != nil {
			return fmt.Errorf("socket %s: %w", s.Path, err)
		}
		if f.overlay != "" {
			overlaid = append(overlaid, f)
		}
		go f.serve()
	}
	if len(overlaid) > 0 {
		go keepBound(overlaid)
	}
	return nil
}

func (f *forward) listen() error {
	if err := os.MkdirAll(filepath.Dir(f.Path), 0o755); err != nil && !errors.Is(err, syscall.EROFS) {
		return err
	}
	path := f.Path
	if _, err := os.Lstat(f.Path); err == nil {
		if err := os.Remove(f.Path); err != nil {
			path = filepath.Join(privateDir, "sockets", strconv.FormatUint(uint64(f.Port), 10)+".sock")
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			_ = os.Remove(path)
			f.overlay = path
		}
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	f.listener = listener
	if err := os.Chmod(path, 0o666); err != nil {
		return err
	}
	if f.overlay != "" {
		return f.bind()
	}
	return nil
}

func (f *forward) bind() error {
	return unix.Mount(f.overlay, f.Path, "", unix.MS_BIND, "")
}

// bound reports whether Path is still the helper's socket.
func (f *forward) bound() bool {
	var path, own unix.Stat_t
	return unix.Stat(f.Path, &path) == nil && unix.Stat(f.overlay, &own) == nil && path.Dev == own.Dev && path.Ino == own.Ino
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
			if f.bound() {
				continue
			}
			err := f.bind()
			if err != nil {
				timeout = int((100 * time.Millisecond).Milliseconds())
				if !f.failing {
					slog.Warn("guest: bind a socket again", "path", f.Path, "error", err)
				}
			}
			f.failing = err != nil
		}
	}
}

// hostWait is how long a relay waits for the host's socket: as long as the
// coordination client retries a refused one, so a call made while the
// server restarts is delivered once it is back instead of being cut off
// after it was sent.
const hostWait = 60 * time.Second

func (f *forward) serve() {
	for {
		conn, err := f.listener.Accept()
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
	host, err := dialHost(f.Port)
	if err != nil {
		slog.Warn("guest: reach host socket", "path", f.Path, "port", f.Port, "error", err)
		return
	}
	defer func() { _ = host.Close() }()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); splice(host, conn) }()
	go func() { defer wg.Done(); splice(conn, host) }()
	wg.Wait()
}

func dialHost(port uint32) (net.Conn, error) {
	deadline := time.Now().Add(hostWait)
	delay := 50 * time.Millisecond
	for {
		conn, err := vsock.Dial(vsock.Host, port, nil)
		if err == nil || time.Now().After(deadline) {
			return conn, err
		}
		time.Sleep(delay)
		delay = min(2*delay, time.Second)
	}
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
