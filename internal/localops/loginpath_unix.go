//go:build !windows

package localops

import (
	"os/exec"
	"syscall"
)

// detachCommand starts cmd in its own session so cancelling the context
// kills the whole process group, not only the process: a child it left
// behind would otherwise survive as an orphan and hold the output pipe
// open until WaitDelay. A new session also drops the controlling
// terminal, so the child cannot touch the user's terminal when the
// gateway runs from one.
func detachCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
