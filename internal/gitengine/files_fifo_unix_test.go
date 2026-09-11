//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package gitengine

import "syscall"

func makeFIFO(path string) error {
	return syscall.Mkfifo(path, 0o600)
}
