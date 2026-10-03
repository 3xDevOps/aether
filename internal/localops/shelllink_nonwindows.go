//go:build !windows

package localops

import (
	"fmt"
	"runtime"
)

func windowsProgramsFolder() (string, error) {
	return "", fmt.Errorf("resolving the Windows Programs folder is unsupported on %s", runtime.GOOS)
}

func writeShellLink(path, target, workingDir string) error {
	return fmt.Errorf("creating a Windows shell link is unsupported on %s", runtime.GOOS)
}
