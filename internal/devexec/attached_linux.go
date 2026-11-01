//go:build linux

package devexec

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// RunAttached runs argv in its own process group for as long as its caller
// stays attached. The first n bytes of stdin are argv's whole input; the end
// of the rest, which the caller holds open, kills the group. Processes argv
// leaves behind when it exits by itself keep running.
func RunAttached(n int, argv []string) (int, error) {
	if n < 0 || len(argv) == 0 || argv[0] == "" {
		return 125, errors.New("input size and command are required")
	}
	input := make([]byte, n)
	if _, err := io.ReadFull(os.Stdin, input); err != nil {
		return 125, fmt.Errorf("read the command's input: %w", err)
	}
	child := exec.Command(argv[0], argv[1:]...)
	child.Stdin, child.Stdout, child.Stderr = bytes.NewReader(input), os.Stdout, os.Stderr
	child.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// A descendant that keeps the input pipe open must not hold up the exit.
	child.WaitDelay = time.Second
	if err := child.Start(); err != nil {
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
			return 127, err
		}
		return 126, err
	}
	go func() {
		_, _ = io.Copy(io.Discard, os.Stdin)
		_ = syscall.Kill(-child.Process.Pid, syscall.SIGKILL)
	}()
	err := child.Wait()
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) && !errors.Is(err, exec.ErrWaitDelay) {
		return 125, err
	}
	if status, ok := child.ProcessState.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		return 128 + int(status.Signal()), nil
	}
	return child.ProcessState.ExitCode(), nil
}
