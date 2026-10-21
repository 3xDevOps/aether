//go:build unix

package gitengine

import (
	"os/exec"
	"syscall"
)

// killProcessGroupOnCancel starts cmd in its own process group and kills the
// whole group when cmd's context is done, so no child outlives Wait.
func killProcessGroupOnCancel(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
}
