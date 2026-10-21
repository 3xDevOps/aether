//go:build unix

package gitengine

import (
	"os/exec"
	"syscall"
	"time"
)

// processGroupKillDelay is how long a cancelled process group has to exit on
// SIGTERM before it gets SIGKILL.
const processGroupKillDelay = 5 * time.Second

// runInProcessGroup runs cmd in its own process group. Cancellation sends the
// whole group SIGTERM, on which git removes its lock files; a SIGKILLed git
// leaves locks such as packed-refs.lock that fail every later ref update.
// SIGKILL follows only if the group outlives processGroupKillDelay. cmd must
// not set WaitDelay: children share cmd's output pipes, so Run returns only
// once every process in the group has exited.
func runInProcessGroup(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var kill *time.Timer
	cmd.Cancel = func() error {
		pgid := cmd.Process.Pid
		kill = time.AfterFunc(processGroupKillDelay, func() { _ = syscall.Kill(-pgid, syscall.SIGKILL) })
		return syscall.Kill(-pgid, syscall.SIGTERM)
	}
	err := cmd.Run()
	// The group's id is free for reuse once Run returns.
	if kill != nil {
		kill.Stop()
	}
	return err
}
