package helper

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestDenyVsockFailsOnlyVsockSockets(t *testing.T) {
	if os.Getenv("AETHER_DENY_VSOCK_CHILD") != "" {
		runtime.LockOSThread()
		if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
			t.Fatal(err)
		}
		if err := denyVsock(); err != nil {
			t.Fatal(err)
		}
		_, vsockErr := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM, 0)
		_, _, pairErr := unixSocketpair(unix.AF_VSOCK)
		fd, unixErr := unix.Socket(unix.AF_UNIX, unix.SOCK_STREAM, 0)
		if unixErr == nil {
			_ = unix.Close(fd)
		}
		fmt.Printf("vsock=%v pair=%v unix=%v\n", errors.Is(vsockErr, unix.EPERM), errors.Is(pairErr, unix.EPERM), unixErr == nil)
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run", "^TestDenyVsockFailsOnlyVsockSockets$")
	cmd.Env = append(os.Environ(), "AETHER_DENY_VSOCK_CHILD=1")
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "vsock=true pair=true unix=true") {
		t.Fatalf("filtered child: %v\n%s", err, out)
	}
}

func unixSocketpair(domain int) (int, int, error) {
	fds, err := unix.Socketpair(domain, unix.SOCK_STREAM, 0)
	if err == nil {
		_ = unix.Close(fds[0])
		_ = unix.Close(fds[1])
	}
	return fds[0], fds[1], err
}
