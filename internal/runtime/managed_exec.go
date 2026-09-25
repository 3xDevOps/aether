package runtime

import (
	"context"
	"errors"
	"time"
)

// ErrExecUnavailable means execution identity or its terminal cannot be proved.
// It never authorizes starting the command again.
var ErrExecUnavailable = errors.New("runtime: managed execution unavailable")

// ManagedExecRuntime is optional: ordinary Runtime implementations and ExecTTY
// callers do not need process ownership. A creation key is single-use inside a
// container, including after an execution exits.
type ManagedExecRuntime interface {
	StartExecTTY(context.Context, ID, ExecSpec) (ManagedExec, error)
	RecoverExec(context.Context, ExecIdentity) (ManagedExec, error)
}

// ExecSpec inherits the container's configured user and environment. An empty
// WorkingDir inherits its configured working directory. Argv is executed
// directly; callers wanting shell syntax must explicitly use /bin/sh -c.
type ExecSpec struct {
	Argv []string
	WorkingDir string
	Cols, Rows uint
	CreationKey string
}

// ExecIdentity must be persisted before publishing the terminal. ExecID names a
// specific Docker exec, not a PID that might later name unrelated work.
type ExecIdentity struct {
	ContainerID ID `json:"container_id"`
	ExecID string `json:"exec_id"`
	CreationKey string `json:"creation_key"`
}

// ExecState keeps transport availability separate from command lifecycle.
// ExitCode is present only after the owned command and its descendants end.
type ExecState struct {
	Running bool `json:"running"`
	Exited bool `json:"exited"`
	ExitCode *int `json:"exit_code,omitempty"`
	Attached bool `json:"attached"`
	UnavailableReason string `json:"unavailable_reason,omitempty"`
}

// ManagedExec owns a command independently of any dashboard/stream. Detach
// only closes the stream. Stop signals and reaps the command's process group
// and adopted descendants without stopping the run container. A recovered
// Docker execution has no Attachment: Docker cannot reattach an exec PTY.
type ManagedExec interface {
	Identity() ExecIdentity
	Attachment() Attachment
	Status(context.Context) (ExecState, error)
	Wait(context.Context) (ExitStatus, error)
	Stop(context.Context, time.Duration) (ExitStatus, error)
	Resize(context.Context, uint, uint) error
	Detach() error
}
