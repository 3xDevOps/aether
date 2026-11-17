package edgetest

import (
	"net/netip"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

func reusePort(_, _ string, c syscall.RawConn) error {
	var serr error
	if err := c.Control(func(fd uintptr) {
		serr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEPORT, 1)
	}); err != nil {
		return err
	}
	return serr
}

// holdPort keeps addr bound until the test ends, so nothing else takes
// the port while the edge is stopped. The socket never listens: a dial
// to a stopped edge is still refused.
func holdPort(t *testing.T, addr string) {
	t.Helper()
	ap, err := netip.ParseAddrPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unix.Close(fd) })
	if err = unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_REUSEPORT, 1); err != nil {
		t.Fatal(err)
	}
	if err = unix.Bind(fd, &unix.SockaddrInet4{Port: int(ap.Port()), Addr: ap.Addr().As4()}); err != nil {
		t.Fatalf("hold %s: %v", addr, err)
	}
}
