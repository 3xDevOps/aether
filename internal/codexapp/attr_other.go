//go:build !linux

package codexapp

import "syscall"

// serverAttr has no parent-death signal outside Linux, where no run
// container starts the app-server.
func serverAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}
