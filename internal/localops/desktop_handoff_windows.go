//go:build windows

package localops

import (
	"errors"
	"fmt"
	"os/exec"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

// detach makes cmd outlive its parent and the console it was started from.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP}
}

// waitForExit blocks until process pid ends, by handle, for at most
// timeout. A pid that no longer exists has already ended.
func waitForExit(pid int, timeout time.Duration) error {
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = windows.CloseHandle(h) }()
	ms := uint32(0)
	if timeout > 0 {
		ms = uint32(timeout.Milliseconds())
	}
	switch event, err := windows.WaitForSingleObject(h, ms); event {
	case windows.WAIT_OBJECT_0:
		return nil
	case uint32(windows.WAIT_TIMEOUT):
		return fmt.Errorf("still running after %s", timeout)
	default:
		return err
	}
}

// renameBusy reports whether a failed rename is Windows refusing it over a
// handle another process still holds, which an exiting process releases.
func renameBusy(err error) bool {
	return errors.Is(err, windows.ERROR_SHARING_VIOLATION) ||
		errors.Is(err, windows.ERROR_ACCESS_DENIED) ||
		errors.Is(err, windows.ERROR_LOCK_VIOLATION)
}
