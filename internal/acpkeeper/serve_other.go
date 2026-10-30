//go:build !linux

package acpkeeper

import (
	"fmt"
	"os"
)

// Serve runs only inside a Linux run container.
func Serve(string, string, []string) int {
	fmt.Fprintln(os.Stderr, "acp-keeper: runs only on Linux")
	return 125
}

func undumpable() error { return nil }
