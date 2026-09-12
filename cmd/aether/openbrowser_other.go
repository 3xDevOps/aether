//go:build !windows

package main

import (
	"os/exec"
	"runtime"
)

// openBrowser hands url to the desktop's URL opener.
func openBrowser(url string) {
	opener := "xdg-open"
	if runtime.GOOS == "darwin" {
		opener = "open"
	}
	_ = exec.Command(opener, url).Start()
}
