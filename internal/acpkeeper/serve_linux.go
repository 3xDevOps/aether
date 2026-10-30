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
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// exitHandover bounds how long a keeper whose adapter exited waits for its
// attacher to ack the exit.
const exitHandover = 30 * time.Second

// Serve runs the keeper process: it reads its token from the first line of
// stdin, listens on socket in a directory only its user can enter, starts
// argv and prints "ready". From then on it uses no stdio of its own, which
// is the stream of an exec that ends with the server that started it. It
// returns the adapter's exit code.
func Serve(socket string, argv []string) int {
	token, err := bufio.NewReader(os.Stdin).ReadString('\n')
	token = strings.TrimSpace(token)
	if err != nil || token == "" {
		fmt.Fprintln(os.Stderr, "acp-keeper: read the token from stdin:", err)
		return 125
	}
	dir := filepath.Dir(socket)
	if err = os.Mkdir(dir, 0o700); err != nil {
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
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
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
