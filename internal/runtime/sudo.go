package runtime

import (
	"context"
	"io"
)

// SudoRuntime optionally serves a Default run's sudo shim.
type SudoRuntime interface {
	// SettingUp reports whether the container's setup script is running:
	// its main command has not started.
	SettingUp(ID) bool
	// ExecAttached runs spec.Argv in a running container until it exits and
	// reports its exit code. Ending ctx kills its process group; processes
	// it leaves behind when it exits by itself keep running.
	ExecAttached(ctx context.Context, id ID, spec AttachedExec) (int, error)
}

// AttachedExec is one ExecAttached command with the container's
// environment. Stdin is its whole input.
type AttachedExec struct {
	Argv           []string
	WorkingDir     string
	User           string
	Stdin          []byte
	Stdout, Stderr io.Writer
}
