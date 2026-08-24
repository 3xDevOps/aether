//go:build windows

package main

import (
	"os"

	"golang.org/x/sys/windows"
)

// enableVirtualTerminal turns on ANSI escape processing for the console
// attached to out, returning a restore func. term.MakeRaw only sets
// ENABLE_VIRTUAL_TERMINAL_INPUT on the input handle, so without this a legacy
// conhost prints the remote TUI's escape sequences as literal bytes. A console
// that refuses VT is a cosmetic degradation, not a reason to fail the attach,
// so every failure path yields a no-op restore.
func enableVirtualTerminal(out *os.File) (restore func()) {
	noop := func() {}
	h := windows.Handle(out.Fd())
	var mode uint32
	if err := windows.GetConsoleMode(h, &mode); err != nil {
		return noop
	}
	if mode&windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING != 0 {
		return noop
	}
	if err := windows.SetConsoleMode(h, mode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING); err != nil {
		return noop
	}
	return func() { _ = windows.SetConsoleMode(h, mode) }
}
