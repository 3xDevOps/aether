// Package runtime is the pluggable container runtime seam. It defines how
// Aether creates, drives, and observes run containers without naming any
// concrete engine: Docker is the v1 implementation, and future backends
// (Podman, firecracker, remote runner nodes) implement the same interface.
package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"time"
)

// ID identifies a container within the Runtime that created it. It is opaque
// to callers.
type ID string

// ErrNotFound reports that no container matches a lookup. Returned by
// FindByCreationKey when no container carries the key.
var ErrNotFound = errors.New("runtime: container not found")

// Mount is one additional bind mount into a run container. Host paths are
// validated against Aether-owned roots by the mount validator (see
// mounts.go) before a spec reaches any runtime; the runtime itself only
// requires both paths to be absolute.
type Mount struct {
	// HostPath is the absolute host path of the bind source.
	HostPath string
	// ContainerPath is the absolute path inside the container.
	ContainerPath string
	// ReadOnly mounts the bind read-only.
	ReadOnly bool
}

// Spec describes one run container, engine-neutrally: the workspace image,
// environment, the run worktree bind-mounted into the container, resource
// limits, working directory, setup script hook, and the main command.
type Spec struct {
	// Name is an optional human-readable name hint; the runtime may
	// decorate or ignore it.
	Name string
	// Image is the container image reference. Missing images are pulled at
	// Create time.
	Image string
	// Env is the environment applied to both the setup script and the main
	// command.
	Env map[string]string
	// WorktreeHostPath is the absolute host path of the run's git worktree.
	// It is bind-mounted read-write at WorktreeMountPath. Both fields are
	// set together or left empty together.
	WorktreeHostPath string
	// WorktreeMountPath is the absolute path inside the container where the
	// worktree appears.
	WorktreeMountPath string
	// WorkingDir is the working directory for the setup script and the main
	// command; empty means the image default.
	WorkingDir string
	// Command is the argv of the container's main process. It replaces any
	// entrypoint or command baked into the image.
	Command []string
	// TTY allocates a pseudo-terminal for the main process. All output
	// then arrives merged on the attachment's Stdout (Stderr stays empty)
	// and Attachment.Resize adjusts the terminal geometry.
	TTY bool
	// SetupScript, when non-empty, is a shell script executed inside the
	// container after Start but before Command runs. A nonzero setup exit
	// fails Start and the main command never runs. The script runs at most
	// once per container: restarting a container whose setup already
	// succeeded skips it. Requirements and caveats: the image must provide
	// /bin/sh and a sleep utility, and the script is persisted in container
	// metadata - like Env it is visible to anyone who can inspect the
	// container, so it must not embed secrets.
	SetupScript string
	// CPULimit caps CPU usage in (possibly fractional) cores; 0 means
	// unlimited.
	CPULimit float64
	// MemoryLimitBytes caps memory usage; 0 means unlimited.
	MemoryLimitBytes int64
	// Mounts are additional bind mounts (credential homes, materialized
	// profiles, coordination assets), applied after the worktree mount.
	// Callers must pass them through ValidateMounts first.
	Mounts []Mount
	// User is the numeric "uid:gid" the main process (and setup script)
	// runs as; empty means the image default.
	User string
	// CreationKey is an opaque caller-chosen key persisted with the
	// container (Docker: an aether-owned label) so a container created
	// right before a crash can be found again via FindByCreationKey. The
	// caller is responsible for uniqueness.
	CreationKey string
}

// Validate reports every problem that makes the spec unusable by any
// runtime implementation.
func (s Spec) Validate() error {
	var errs []error
	if s.Image == "" {
		errs = append(errs, errors.New("image is required"))
	}
	if len(s.Command) == 0 {
		errs = append(errs, errors.New("command is required"))
	} else if s.Command[0] == "" {
		errs = append(errs, errors.New("command[0] must not be empty"))
	}
	switch {
	case (s.WorktreeHostPath == "") != (s.WorktreeMountPath == ""):
		errs = append(errs, errors.New("worktree host path and mount path must be set together"))
	case s.WorktreeHostPath != "":
		if !path.IsAbs(s.WorktreeHostPath) {
			errs = append(errs, fmt.Errorf("worktree host path %q must be absolute", s.WorktreeHostPath))
		}
		if !path.IsAbs(s.WorktreeMountPath) {
			errs = append(errs, fmt.Errorf("worktree mount path %q must be absolute", s.WorktreeMountPath))
		}
	}
	if s.WorkingDir != "" && !path.IsAbs(s.WorkingDir) {
		errs = append(errs, fmt.Errorf("working dir %q must be absolute", s.WorkingDir))
	}
	if s.CPULimit < 0 {
		errs = append(errs, fmt.Errorf("cpu limit must not be negative, got %v", s.CPULimit))
	}
	if s.MemoryLimitBytes < 0 {
		errs = append(errs, fmt.Errorf("memory limit must not be negative, got %d", s.MemoryLimitBytes))
	}
	for k := range s.Env {
		if k == "" || strings.ContainsAny(k, "=\x00") {
			errs = append(errs, fmt.Errorf("invalid env var name %q", k))
		}
	}
	for i, m := range s.Mounts {
		if !path.IsAbs(m.HostPath) {
			errs = append(errs, fmt.Errorf("mount %d host path %q must be absolute", i, m.HostPath))
		}
		if !path.IsAbs(m.ContainerPath) {
			errs = append(errs, fmt.Errorf("mount %d container path %q must be absolute", i, m.ContainerPath))
		}
	}
	if s.User != "" {
		if uid, gid, ok := strings.Cut(s.User, ":"); !ok || !allDigits(uid) || !allDigits(gid) {
			errs = append(errs, fmt.Errorf("user %q must be numeric uid:gid", s.User))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("runtime: invalid spec: %w", errors.Join(errs...))
	}
	return nil
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// ExitStatus reports how a container's main process finished.
type ExitStatus struct {
	// Code is the process exit code; by convention 128+signal when the
	// process was killed by a signal.
	Code int
}

// ExecExitError reports an immediate exec process exit that prevented
// attachment. Code 126 and 127 commonly indicate a missing executable.
type ExecExitError struct {
	Code int
}

func (e *ExecExitError) Error() string {
	return fmt.Sprintf("runtime: exec exited with status %d", e.Code)
}

// Attachment is a live stdio stream to a container's main process. A
// container supports any number of sequential attachments: detaching
// (Close) and re-attaching later is always safe.
type Attachment interface {
	// Stdin writes to the process's stdin. Closing it signals that this
	// attachment supplies no more input; the process's stdin stays open so
	// a later Attach can continue writing, which also means the process is
	// not sent EOF. Engines may end this attachment's output streams when
	// stdin is closed (Docker does) - re-attach to keep reading.
	Stdin() io.WriteCloser
	// Stdout streams the process's stdout; with Spec.TTY set it carries
	// the merged terminal output. Stdout and Stderr are buffered
	// independently: reading only one never stalls the other.
	Stdout() io.Reader
	// Stderr streams the process's stderr; it is empty when Spec.TTY is
	// set.
	Stderr() io.Reader
	// Resize sets the terminal geometry of a TTY attachment; it fails on
	// containers created without Spec.TTY.
	Resize(ctx context.Context, cols, rows uint) error
	// Close detaches the streams. It never stops the container, and the
	// run can be re-attached afterwards.
	Close() error
}

// Runtime creates and drives run containers. Implementations must be safe
// for concurrent use.
type Runtime interface {
	// Create materializes a container for spec without starting it,
	// pulling the image first if it is missing locally.
	Create(ctx context.Context, spec Spec) (ID, error)
	// Start launches the container: the setup script (if any) runs first,
	// and a nonzero setup exit fails Start with the container stopped, the
	// main command never having run.
	Start(ctx context.Context, id ID) error
	// Pause freezes every process in the container (SIGSTOP semantics).
	Pause(ctx context.Context, id ID) error
	// Resume thaws a paused container.
	Resume(ctx context.Context, id ID) error
	// Stop terminates the main process gracefully, escalating to a hard
	// kill after grace. A negative grace means the runtime default.
	Stop(ctx context.Context, id ID, grace time.Duration) error
	// Destroy force-removes the container and its resources. Destroying a
	// container that no longer exists is not an error.
	Destroy(ctx context.Context, id ID) error
	// Attach opens a stdio stream to the container's main process.
	Attach(ctx context.Context, id ID) (Attachment, error)
	// ExecTTY opens an additional TTY process inside a running container;
	// exit 126/127 surfaces as *ExecExitError so callers can retry with
	// /bin/sh -l.
	ExecTTY(ctx context.Context, id ID, argv []string, workDir string, cols, rows uint) (Attachment, error)
	// Wait blocks until the main process exits and reports its exit code.
	Wait(ctx context.Context, id ID) (ExitStatus, error)
	// FindByCreationKey returns the container created with
	// Spec.CreationKey == key, or ErrNotFound. It exists for crash
	// recovery: a container created before its ID was persisted can be
	// found and destroyed. Keys are assumed unique; with duplicates any
	// match may be returned.
	FindByCreationKey(ctx context.Context, key string) (ID, error)
	// BuildImage builds an image from the Dockerfile text and tags it.
	// The build context contains the Dockerfile alone, so the file
	// cannot COPY or ADD anything from the host. Engine progress lines
	// stream to progress (which may be nil) as the build runs; a failed
	// build returns the engine's error detail.
	BuildImage(ctx context.Context, dockerfile string, tag string, progress io.Writer) error
	// RemoveImage removes a local image tag. Removing a tag that does
	// not exist is not an error.
	RemoveImage(ctx context.Context, tag string) error
}
