package cli

import (
	"fmt"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// tcpUserTimeout drops a connection whose sent bytes go unacknowledged this
// long. The kernel acknowledges for a server process too starved to answer,
// so only a dead network trips it, well inside the gateway's keepalive window.
const tcpUserTimeout = 90 * time.Second

func dialControl(_, _ string, c syscall.RawConn) error {
	var serr error
	err := c.Control(func(fd uintptr) {
		serr = unix.SetsockoptInt(int(fd), unix.IPPROTO_TCP, unix.TCP_USER_TIMEOUT, int(tcpUserTimeout.Milliseconds()))
	})
	if err != nil {
		return err
	}
	if serr != nil {
		return fmt.Errorf("set TCP_USER_TIMEOUT: %w", serr)
	}
	return nil
}
