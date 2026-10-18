//go:build !windows

package localops

import (
	"errors"
	"os/exec"
	"time"
)

func detach(*exec.Cmd) {}

func waitForExit(int, time.Duration) error {
	return errors.New("waiting for a process by handle is only implemented on Windows")
}
