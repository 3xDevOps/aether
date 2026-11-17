package helper

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/guest"
	"golang.org/x/sys/unix"
)

func TestDenyVsockFailsOnlyVsockSocketsAndIOURing(t *testing.T) {
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
		_, _, ringErr := unix.Syscall(unix.SYS_IO_URING_SETUP, 1, 0, 0)
		fmt.Printf("vsock=%v pair=%v unix=%v ring=%v\n", errors.Is(vsockErr, unix.EPERM), errors.Is(pairErr, unix.EPERM), unixErr == nil, ringErr == unix.ENOSYS)
		return
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	_, client := startHelper(t)
	p := spawn(t, client, guest.ProcessSpec{
		ID:   rand.Text(),
		Argv: []string{exe, "-test.run", "^TestDenyVsockFailsOnlyVsockSocketsAndIOURing$"},
		Env:  []string{"AETHER_DENY_VSOCK_CHILD=1"},
	})
	a := p.Attach(true)
	defer func() { _ = a.Close() }()
	out, _ := io.ReadAll(a.Stdout())
	stderr, _ := io.ReadAll(a.Stderr())
	code, err := p.Wait(testContext(t))
	if err != nil || code != 0 || !strings.Contains(string(out), "vsock=true pair=true unix=true ring=true") {
		t.Fatalf("filtered child: exit %d, %v\n%s%s", code, err, out, stderr)
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
