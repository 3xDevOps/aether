//go:build !unix

package gitengine

import "os/exec"

func killProcessGroupOnCancel(*exec.Cmd) {}
