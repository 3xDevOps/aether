//go:build !unix

package gitengine

import "os/exec"

func runInProcessGroup(cmd *exec.Cmd) error { return cmd.Run() }
