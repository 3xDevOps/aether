package coord

import (
	"context"
	"net"
	"syscall"
	"time"
)

// watchConnection cancels ctx when a Unix peer closes. A net.Pipe or other
// non-syscall connection still receives service-level cancellation and the
// report timeout; this fast path covers the production Linux socket.
func watchConnection(conn net.Conn, cancel context.CancelFunc, stop <-chan struct{}) {
	sc, ok := conn.(syscall.Conn)
	if !ok {
		return
	}
	raw, err := sc.SyscallConn()
	if err != nil {
		return
	}
	for {
		select {
		case <-stop:
			return
		default:
		}
		closed := false
		_ = raw.Control(func(fd uintptr) {
			buf := []byte{0}
			n, _, recvErr := syscall.Recvfrom(int(fd), buf, syscall.MSG_PEEK|syscall.MSG_DONTWAIT)
			if n == 0 || (recvErr != nil && recvErr != syscall.EAGAIN && recvErr != syscall.EWOULDBLOCK && recvErr != syscall.EINTR) {
				closed = true
			}
		})
		if closed {
			cancel()
			return
		}
		select {
		case <-stop:
			return
		case <-time.After(10 * time.Millisecond):
		}
	}
}
