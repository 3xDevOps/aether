//go:build linux

package acpkeeper

import (
	"bufio"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// termDrain bounds how long a keeper whose program exited reads output a
// descendant still holding the PTY open writes.
const termDrain = time.Second

// ServeTerm runs a terminal keeper: as Serve, but argv runs as the session
// leader of a PTY of cols by rows the keeper owns. A SIGTERM or SIGHUP to
// the keeper hangs the terminal up, as stopping a terminal exec does. It
// returns the program's exit code.
func ServeTerm(socket, user string, cols, rows uint16, argv []string) int {
	fail := func(code int, err error) int {
		fmt.Fprintln(os.Stderr, "acp-keeper:", err)
		return code
	}
	if err := undumpable(); err != nil {
		return fail(125, err)
	}
	cred, err := credential(user)
	if err != nil {
		return fail(125, err)
	}
	token, err := bufio.NewReader(os.Stdin).ReadString('\n')
	token = strings.TrimSpace(token)
	if err != nil || token == "" {
		return fail(125, fmt.Errorf("read the token from stdin: %w", err))
	}
	dir := filepath.Dir(socket)
	if err = privateDir(filepath.Dir(dir), dir); err != nil {
		return fail(125, err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	ln, err := net.Listen("unix", socket)
	if err != nil {
		return fail(125, err)
	}
	defer func() { _ = ln.Close() }()
	master, slave, err := openPTY(cred)
	if err != nil {
		return fail(125, err)
	}
	defer func() { _ = master.Close() }()
	resize := func(cols, rows uint16) error {
		return unix.IoctlSetWinsize(int(master.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Col: cols, Row: rows})
	}
	if err = resize(cols, rows); err != nil {
		return fail(125, fmt.Errorf("size the terminal: %w", err))
	}

	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Pdeathsig: syscall.SIGKILL, Credential: cred}
	err = cmd.Start()
	_ = slave.Close()
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
			return fail(127, fmt.Errorf("start %s: %w", argv[0], err))
		}
		return fail(126, fmt.Errorf("start %s: %w", argv[0], err))
	}
	if _, err := fmt.Fprintln(os.Stdout, "ready"); err != nil {
		_ = cmd.Process.Kill()
		return 125
	}
	if err := detachStdio(filepath.Join(dir, "keeper.log")); err != nil {
		_ = cmd.Process.Kill()
		return fail(125, err)
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	t := NewTerm(TermConfig{Token: token, Input: master, Resize: resize, Cols: cols, Rows: rows, Logger: logger})

	hangup := make(chan os.Signal, 1)
	signal.Notify(hangup, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		for range hangup {
			for _, sig := range []syscall.Signal{syscall.SIGHUP, syscall.SIGTERM} {
				_ = syscall.Kill(-cmd.Process.Pid, sig)
			}
		}
	}()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go t.ServeConn(conn)
		}
	}()
	pumped := make(chan struct{})
	go func() {
		defer close(pumped)
		buf := make([]byte, termChunk)
		for {
			n, err := master.Read(buf)
			if n > 0 {
				_, _ = t.Write(buf[:n])
			}
			if err != nil {
				// EIO: no process holds the PTY open any more.
				return
			}
		}
	}()
	code := 0
	if err := cmd.Wait(); err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			logger.Warn("acpkeeper: wait for the terminal's program", "error", err)
		}
		code = cmd.ProcessState.ExitCode()
		if status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			code = 128 + int(status.Signal())
		}
	}
	select {
	case <-pumped:
	case <-time.After(termDrain):
	}
	t.Exited(code)
	select {
	case <-t.Ended():
	case <-time.After(exitHandover):
	}
	return code
}

// openPTY opens a new PTY whose terminal belongs to cred's user, when set.
func openPTY(cred *syscall.Credential) (master, slave *os.File, err error) {
	fd, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("open a PTY: %w", err)
	}
	master = os.NewFile(uintptr(fd), "/dev/ptmx")
	defer func() {
		if err != nil {
			_ = master.Close()
		}
	}()
	if err = unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0); err != nil {
		return nil, nil, fmt.Errorf("unlock the PTY: %w", err)
	}
	n, err := unix.IoctlGetUint32(fd, unix.TIOCGPTN)
	if err != nil {
		return nil, nil, fmt.Errorf("name the PTY: %w", err)
	}
	slave, err = os.OpenFile("/dev/pts/"+strconv.FormatUint(uint64(n), 10), os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("open the PTY's terminal: %w", err)
	}
	if cred != nil {
		if err = slave.Chown(int(cred.Uid), int(cred.Gid)); err != nil {
			_ = slave.Close()
			return nil, nil, fmt.Errorf("hand the PTY's terminal to uid %d: %w", cred.Uid, err)
		}
	}
	return master, slave, nil
}
