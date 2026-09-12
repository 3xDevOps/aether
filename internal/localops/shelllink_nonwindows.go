//go:build !windows

package localops

import (
	"fmt"
	"runtime"
)

func writeShellLink(path, target, workingDir string) error {
	return fmt.Errorf("creating a Windows shell link is unsupported on %s", runtime.GOOS)
}
