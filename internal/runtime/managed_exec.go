package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/coordtransport"
	"github.com/3xDevOps/Aether/internal/devexec"
)

// ErrExecUnavailable means execution identity or its terminal cannot be proved.
// It never authorizes starting the command again.
var ErrExecUnavailable = errors.New("runtime: managed execution unavailable")

// ManagedExecRuntime is optional: ordinary Runtime implementations and ExecTTY
// callers do not need process ownership. A creation key is single-use inside a
// container, including after an execution exits.
type ManagedExecRuntime interface {
	StartExecTTY(context.Context, ID, ExecSpec) (ManagedExec, error)
	// StartExecPipe owns the command the same way, without a terminal: it
	// gets its own process group and no controlling tty, and its attachment
	// carries separate stdin, stdout and stderr streams. Its stdout never
	// drops bytes: a reader that falls behind slows the command instead.
	StartExecPipe(context.Context, ID, ExecSpec) (ManagedExec, error)
	// RecoverExec, and a ManagedExec's Status and Stop, fail with
	// ErrNotFound only once the command provably no longer runs.
	RecoverExec(context.Context, ExecIdentity) (ManagedExec, error)
}

// ExecSpec inherits the container's configured user and environment. A
// non-empty User, such as "0", runs it as that user instead; the group of a
// configured "uid:gid" may then still read its state and stop it. An empty
// WorkingDir inherits its configured working directory. Argv is executed
// directly; callers wanting shell syntax must explicitly use /bin/sh -c.
// Cols and Rows size StartExecTTY's terminal; StartExecPipe ignores them.
type ExecSpec struct {
	Argv       []string
	User       string
	WorkingDir string
	// Env adds KEY=VALUE entries to this exec's environment.
	Env         []string
	Cols, Rows  uint
	CreationKey string
	// Lossless makes every output stream slow the command when its reader
	// falls behind, instead of dropping the oldest bytes past the buffer.
	// The caller must then read them all: one unread stream stalls the
	// others.
	Lossless bool
}

// ExecIdentity must be persisted before publishing the terminal. ExecID names a
// specific Docker exec, not a PID that might later name unrelated work.
type ExecIdentity struct {
	ContainerID ID     `json:"container_id"`
	ExecID      string `json:"exec_id"`
	CreationKey string `json:"creation_key"`
	ClaimToken  string `json:"claim_token"`
	// User is the exec's user when it is not the container's; the helper's
	// control socket is that user's alone.
	User string `json:"user,omitempty"`
}

// ExecState keeps transport availability separate from command lifecycle.
// ExitCode is present only after the owned command and its descendants end.
type ExecState struct {
	Running           bool   `json:"running"`
	Exited            bool   `json:"exited"`
	ExitCode          *int   `json:"exit_code,omitempty"`
	Attached          bool   `json:"attached"`
	UnavailableReason string `json:"unavailable_reason,omitempty"`
}

// ManagedExec owns a command independently of any dashboard/stream. Detach
// only closes the stream. Attachment retains buffered output after EOF until
// Detach; Status.Attached reports whether the transport is still live.
// Stop signals and reaps the command's process group and adopted descendants
// without stopping the run container. A recovered Docker execution has no
// Attachment: Docker cannot reattach an exec's streams.
type ManagedExec interface {
	Identity() ExecIdentity
	Attachment() Attachment
	Status(context.Context) (ExecState, error)
	Wait(context.Context) (ExitStatus, error)
	Stop(context.Context, time.Duration) (ExitStatus, error)
	Resize(context.Context, uint, uint) error
	Detach() error
}

// ExecControlArgv is the staged helper's control command for one action on
// the execution identity names.
func ExecControlArgv(identity ExecIdentity, action string, grace time.Duration) []string {
	return []string{coordtransport.CLIPath, devexec.Command, "control", identity.CreationKey, identity.ExecID, identity.ClaimToken, action, strconv.FormatInt(grace.Milliseconds(), 10)}
}

// ExecControlState reads the staged helper's reply to a control command that
// exited with code and printed stdout and stderr.
func ExecControlState(identity ExecIdentity, action string, code int, stdout, stderr string) (devexec.State, error) {
	var state devexec.State
	decodeErr := json.Unmarshal([]byte(stdout), &state)
	if decodeErr == nil && state.ExecID == identity.ExecID && state.CreationKey == identity.CreationKey && state.ClaimToken == identity.ClaimToken {
		if state.Error != "" {
			if state.Exited && state.ExitCode != nil && (*state.ExitCode == 126 || *state.ExitCode == 127) {
				if action == "start" {
					return state, &ExecExitError{Code: *state.ExitCode}
				}
				return state, nil
			}
			return state, errors.New(state.Error)
		}
		if code == 0 {
			return state, nil
		}
	}
	failed := fmt.Errorf("%w: helper %s failed (status %d): %s", ErrExecUnavailable, action, code, strings.TrimSpace(stderr))
	if strings.Contains(stderr, devexec.ErrNoSupervisor.Error()) {
		return state, fmt.Errorf("%w: %w", devexec.ErrNoSupervisor, failed)
	}
	return state, failed
}
