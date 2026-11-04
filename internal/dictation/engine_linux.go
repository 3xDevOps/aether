package dictation

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"runtime"
	"syscall"
	"time"
)

// RunEngine is the engine container's entrypoint: args are the Unix
// socket to serve, the engine's port and, after "--", the engine command.
// It starts the engine at nice 10, creates the socket once the engine
// accepts connections and relays each connection on it to the engine. It
// returns when the engine exits.
func RunEngine(args []string) int {
	if len(args) < 4 || args[2] != "--" {
		_, _ = fmt.Fprintln(os.Stderr, "usage: aether-server dictation engine <socket> <port> -- <engine> [args...]")
		return 2
	}
	if err := runEngine(args[0], "127.0.0.1:"+args[1], args[3:]); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "dictation engine:", err)
		return 1
	}
	return 0
}

func runEngine(socket, addr string, engine []string) error {
	// The engine inherits this thread's nice value, and its Pdeathsig
	// watches this thread.
	runtime.LockOSThread()
	if err := syscall.Setpriority(syscall.PRIO_PROCESS, 0, 10); err != nil {
		return fmt.Errorf("lower priority: %w", err)
	}
	cmd := exec.Command(engine[0], engine[1:]...)
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	if err := cmd.Start(); err != nil {
		return err
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	for {
		if c, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
			_ = c.Close()
			break
		}
		select {
		case err := <-exited:
			return fmt.Errorf("the engine exited while loading: %v", err)
		case <-time.After(200 * time.Millisecond):
		}
	}
	if err := os.Remove(socket); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	syscall.Umask(0o177)
	ln, err := net.Listen("unix", socket)
	if err != nil {
		return err
	}
	defer func() { _ = ln.Close() }()
	go relay(ln, addr)
	if err := <-exited; err != nil {
		return fmt.Errorf("the engine exited: %w", err)
	}
	return errors.New("the engine exited")
}

func relay(ln net.Listener, addr string) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go func() {
			defer func() { _ = c.Close() }()
			e, err := net.Dial("tcp", addr)
			if err != nil {
				return
			}
			defer func() { _ = e.Close() }()
			go func() {
				_, _ = io.Copy(e, c)
				_ = e.(*net.TCPConn).CloseWrite()
			}()
			_, _ = io.Copy(c, e)
		}()
	}
}
