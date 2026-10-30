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
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// exitHandover bounds how long a keeper whose adapter exited waits for its
// attacher to ack the exit.
const exitHandover = 30 * time.Second

// Serve runs the keeper process: it reads its token from the first line of
// stdin, listens on socket in a new directory only its user can enter, starts
// argv as user, a "uid:gid" or empty for its own, and prints "ready". From
// then on it uses no stdio of its own, which is the stream of an exec that
// ends with the server that started it. It returns the adapter's exit code.
func Serve(socket, user string, argv []string) int {
	if err := undumpable(); err != nil {
		fmt.Fprintln(os.Stderr, "acp-keeper:", err)
		return 125
	}
	cred, err := credential(user)
	if err != nil {
		fmt.Fprintln(os.Stderr, "acp-keeper:", err)
		return 125
	}
	token, err := bufio.NewReader(os.Stdin).ReadString('\n')
	token = strings.TrimSpace(token)
	if err != nil || token == "" {
		fmt.Fprintln(os.Stderr, "acp-keeper: read the token from stdin:", err)
		return 125
	}
	dir := filepath.Dir(socket)
	if err = privateDir(filepath.Dir(dir), dir); err != nil {
		fmt.Fprintln(os.Stderr, "acp-keeper:", err)
		return 125
	}
	defer func() { _ = os.RemoveAll(dir) }()
	ln, err := net.Listen("unix", socket)
	if err != nil {
		fmt.Fprintln(os.Stderr, "acp-keeper:", err)
		return 125
	}
	defer func() { _ = ln.Close() }()

	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL, Credential: cred}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		fmt.Fprintln(os.Stderr, "acp-keeper:", err)
		return 125
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		fmt.Fprintln(os.Stderr, "acp-keeper:", err)
		return 125
	}
	k := New(Config{Token: token, AgentIn: stdin})
	cmd.Stderr = k.Stderr()
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "acp-keeper: start", argv[0]+":", err)
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
			return 127
		}
		return 126
	}
	if _, err := fmt.Fprintln(os.Stdout, "ready"); err != nil {
		_ = cmd.Process.Kill()
		return 125
	}
	if err := detachStdio(filepath.Join(dir, "keeper.log")); err != nil {
		fmt.Fprintln(os.Stderr, "acp-keeper:", err)
		_ = cmd.Process.Kill()
		return 125
	}
	k.cfg.Logger = slog.New(slog.NewTextHandler(os.Stderr, nil))

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go k.ServeConn(conn)
		}
	}()
	if err := k.Pump(stdout); err != nil {
		k.cfg.Logger.Warn("acpkeeper: read the agent's output", "error", err)
	}
	code := 0
	if err := cmd.Wait(); err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			k.cfg.Logger.Warn("acpkeeper: wait for the agent", "error", err)
		}
		code = cmd.ProcessState.ExitCode()
		if status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			code = 128 + int(status.Signal())
		}
	}
	k.Exited(code)
	select {
	case <-k.Ended():
	case <-time.After(exitHandover):
	}
	return code
}

// undumpable keeps a process of the agent's user from ptracing this one or
// opening its /proc fds, such as the stdio that carries the token.
func undumpable() error {
	if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
		return fmt.Errorf("prctl PR_SET_DUMPABLE: %w", err)
	}
	return nil
}

// credential is the adapter's user: user's uid and gid, with the gid as its
// only supplementary group, as Docker runs an exec of a container whose user
// is "uid:gid".
func credential(user string) (*syscall.Credential, error) {
	if user == "" {
		return nil, nil
	}
	uid, gid, ok := strings.Cut(user, ":")
	u, uerr := strconv.ParseUint(uid, 10, 32)
	g, gerr := strconv.ParseUint(gid, 10, 32)
	if !ok || uerr != nil || gerr != nil {
		return nil, fmt.Errorf("--user %q is not a numeric uid:gid", user)
	}
	return &syscall.Credential{Uid: uint32(u), Gid: uint32(g), Groups: []uint32{uint32(g)}}, nil
}

// privateDir creates dir, which must not exist yet, inside root, which this
// user owns and no one else may write or enter.
func privateDir(root, dir string) error {
	if err := os.Mkdir(root, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	info, err := os.Lstat(root)
	if err != nil {
		return err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	switch {
	case !info.IsDir():
		return fmt.Errorf("%s is not a directory", root)
	case !ok || int(st.Uid) != os.Geteuid():
		return fmt.Errorf("%s is not owned by uid %d", root, os.Geteuid())
	case info.Mode().Perm()&0o077 != 0:
		return fmt.Errorf("%s has mode %v; others must not reach it", root, info.Mode().Perm())
	}
	return os.Mkdir(dir, 0o700)
}

// detachStdio points stdout and stderr at the log file and stdin at
// /dev/null.
func detachStdio(logPath string) error {
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = log.Close() }()
	null, err := os.Open(os.DevNull)
	if err != nil {
		return err
	}
	defer func() { _ = null.Close() }()
	for _, d := range []struct{ from, to int }{{int(null.Fd()), 0}, {int(log.Fd()), 1}, {int(log.Fd()), 2}} {
		if err := unix.Dup3(d.from, d.to, 0); err != nil {
			return fmt.Errorf("redirect fd %d: %w", d.to, err)
		}
	}
	return nil
}
