//go:build !windows

package main

import "os"

// enableVirtualTerminal turns on ANSI escape processing for the console
// attached to out, returning a restore func. On non-Windows it is a no-op.
func enableVirtualTerminal(out *os.File) (restore func()) {
	return func() {}
}
