//go:build !linux

package helper

import (
	"fmt"
	"os"
)

// Main runs only as PID 1 of a Linux session VM.
func Main([]string) int {
	fmt.Fprintln(os.Stderr, "aether-internal guest: runs only as PID 1 of a Linux session VM")
	return 2
}
