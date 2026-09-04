//go:build windows

package localops

import "os/exec"

// detachCommand is a no-op: syscall.SysProcAttr has no Setsid on Windows,
// and AdoptLoginPath never runs a shell there.
func detachCommand(*exec.Cmd) {}
