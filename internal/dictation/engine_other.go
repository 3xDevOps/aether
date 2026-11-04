//go:build !linux

package dictation

import (
	"fmt"
	"os"
)

// RunEngine runs only on Linux.
func RunEngine([]string) int {
	_, _ = fmt.Fprintln(os.Stderr, "dictation runs only on Linux")
	return 125
}
