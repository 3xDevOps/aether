package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/3xDevOps/Aether/internal/coordtransport"
	"github.com/3xDevOps/Aether/internal/devexec"
	"github.com/moby/moby/client"
)

var _ ManagedExecRuntime = (*Docker)(nil)

// StartExecTTY runs the staged helper as the container's configured user,
// environment and working directory. The helper waits for a claim before
// launching the command: losing/cancelling ExecAttach cannot orphan a command.
func (d *Docker) StartExecTTY(ctx context.Context, id ID, spec ExecSpec) (_ ManagedExec, resultErr error) {
	if spec.CreationKey == "" || len(spec.CreationKey) > 1024 || strings.ContainsRune(spec.CreationKey, 0) {
		return nil, errors.New("runtime: valid single-use execution creation key is required")
	}
	if len(spec.Argv) == 0 || spec.Argv[0] == "" {
		return nil, errors.New("runtime: execution command is required")
	}
	for _, arg := range spec.Argv {
		if strings.ContainsRune(arg, 0) {
			return nil, errors.New("runtime: execution argument contains NUL")
		}
	}
	if spec.WorkingDir != "" && !path.IsAbs(spec.WorkingDir) {
		return nil, errors.New("runtime: execution working directory must be absolute")
	}
	if spec.Cols == 0 || spec.Rows == 0 || spec.Cols > 65535 || spec.Rows > 65535 {
		return nil, errors.New("runtime: execution terminal dimensions must be between 1 and 65535")
	}
	container, err := d.cli.ContainerInspect(ctx, string(id), client.ContainerInspectOptions{})
	if err != nil {
		return nil, fmt.Errorf("runtime: inspect managed execution container: %w", err)
	}
	argv := append([]string{coordtransport.CLIPath, devexec.Command, "run", spec.CreationKey}, spec.Argv...)
	created, err := d.cli.ExecCreate(ctx, container.Container.ID, client.ExecCreateOptions{
		TTY: true, AttachStdin: true, AttachStdout: true, AttachStderr: true,
		Cmd: argv, WorkingDir: spec.WorkingDir,
		ConsoleSize: client.ConsoleSize{Width: spec.Cols, Height: spec.Rows},
	})
	if err != nil {
		return nil, fmt.Errorf("runtime: create managed execution: %w", err)
	}
	execution := &dockerManagedExec{docker: d, identity: ExecIdentity{
		ContainerID: ID(container.Container.ID), ExecID: created.ID, CreationKey: spec.CreationKey,
	}}
	published := false
	defer func() {
		if published {
			return
		}
		// Cleanup has its own bounded context; the caller's cancelled context
		// must not defeat ownership after an ambiguous creation/claim response.
		cleanup, cancel := context.WithTimeout(context.Background(), devexec.StartupTimeout+5*time.Second)
		defer cancel()
		_, stopErr := execution.control(cleanup, "stop", 0)
		if stopErr == nil {
			_, stopErr = execution.Wait(cleanup)
		}
		_ = execution.Detach()
		if stopErr != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("runtime: cancelled execution cleanup: %w", stopErr))
		}
	}()
	attached, err := d.cli.ExecAttach(ctx, created.ID, client.ExecAttachOptions{
		TTY: true, ConsoleSize: client.ConsoleSize{Width: spec.Cols, Height: spec.Rows},
	})
	if err != nil {
		return nil, fmt.Errorf("runtime: attach managed execution: %w", err)
	}
	execution.attachment = newExecAttachment(d.cli, created.ID, attached.HijackedResponse)
	if _, err := execution.control(ctx, "start", 0); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	published = true
	return execution, nil
}

// RecoverExec proves both Docker's immutable execution/container association
// and the helper's claim identity. It cannot restore an exec's stdio: consumers
// must expose the unavailable terminal, not launch a replacement implicitly.
func (d *Docker) RecoverExec(ctx context.Context, identity ExecIdentity) (ManagedExec, error) {
	if identity.ContainerID == "" || identity.ExecID == "" || identity.CreationKey == "" {
		return nil, fmt.Errorf("%w: incomplete identity", ErrExecUnavailable)
	}
	execution := &dockerManagedExec{docker: d, identity: identity}
	if _, err := execution.inspect(ctx); err != nil {
		return nil, err
	}
	if _, err := execution.control(ctx, "status", 0); err != nil {
		return nil, fmt.Errorf("%w: cannot prove helper ownership: %w", ErrExecUnavailable, err)
	}
	return execution, nil
}

type dockerManagedExec struct {
	docker *Docker
	identity ExecIdentity
	mu sync.Mutex
	attachment *execAttachment
}

func (e *dockerManagedExec) Identity() ExecIdentity { return e.identity }

func (e *dockerManagedExec) Attachment() Attachment {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.attachment == nil {
		return nil
	}
	select {
	case <-e.attachment.done:
		return nil
	default:
		return e.attachment
	}
}

func (e *dockerManagedExec) inspect(ctx context.Context) (client.ExecInspectResult, error) {
	info, err := e.docker.cli.ExecInspect(ctx, e.identity.ExecID, client.ExecInspectOptions{})
	if err != nil {
		return info, fmt.Errorf("%w: inspect execution: %w", ErrExecUnavailable, err)
	}
	if info.ID != e.identity.ExecID || info.ContainerID != string(e.identity.ContainerID) {
		return info, fmt.Errorf("%w: execution/container identity mismatch", ErrExecUnavailable)
	}
	return info, nil
}

func (e *dockerManagedExec) control(ctx context.Context, action string, grace time.Duration) (devexec.State, error) {
	args := []string{coordtransport.CLIPath, devexec.Command, "control", e.identity.CreationKey, e.identity.ExecID, action, strconv.FormatInt(grace.Milliseconds(), 10)}
	code, stdout, stderr, err := e.docker.Exec(ctx, e.identity.ContainerID, args, "")
	if err != nil {
		return devexec.State{}, err
	}
	var state devexec.State
	decodeErr := json.Unmarshal([]byte(stdout), &state)
	if decodeErr == nil && state.ExecID == e.identity.ExecID && state.CreationKey == e.identity.CreationKey {
		if state.Error != "" {
			if state.Exited && state.ExitCode != nil && (*state.ExitCode == 126 || *state.ExitCode == 127) {
				return state, &ExecExitError{Code: *state.ExitCode}
			}
			return state, errors.New(state.Error)
		}
		if code == 0 {
			return state, nil
		}
	}
	return state, fmt.Errorf("%w: helper %s failed (status %d): %s", ErrExecUnavailable, action, code, strings.TrimSpace(stderr))
}

func (e *dockerManagedExec) Status(ctx context.Context) (ExecState, error) {
	info, err := e.inspect(ctx)
	if err != nil {
		return ExecState{}, err
	}
	state := ExecState{Running: info.Running, Attached: e.Attachment() != nil}
	if !state.Attached {
		state.UnavailableReason = "PTY attachment is unavailable; Docker exec terminals cannot be reattached"
	}
	if info.Running {
		return state, nil
	}
	final, err := e.control(ctx, "status", 0)
	if err != nil {
		return state, fmt.Errorf("%w: command exit record unavailable: %w", ErrExecUnavailable, err)
	}
	if !final.Exited || final.ExitCode == nil {
		return state, fmt.Errorf("%w: helper ended without a command exit record", ErrExecUnavailable)
	}
	state.Running, state.Exited, state.ExitCode = false, true, final.ExitCode
	return state, nil
}

func (e *dockerManagedExec) Wait(ctx context.Context) (ExitStatus, error) {
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		state, err := e.Status(ctx)
		if err != nil {
			return ExitStatus{}, err
		}
		if state.Exited {
			return ExitStatus{Code: *state.ExitCode}, nil
		}
		select {
		case <-ctx.Done():
			return ExitStatus{}, ctx.Err()
		case <-tick.C:
		}
	}
}

func (e *dockerManagedExec) Stop(ctx context.Context, grace time.Duration) (ExitStatus, error) {
	if grace < 0 {
		grace = 3 * time.Second
	}
	if grace > time.Minute {
		return ExitStatus{}, errors.New("runtime: managed execution stop grace exceeds one minute")
	}
	if _, err := e.inspect(ctx); err != nil {
		return ExitStatus{}, err
	}
	if _, err := e.control(ctx, "stop", grace); err != nil {
		return ExitStatus{}, err
	}
	return e.Wait(ctx)
}

func (e *dockerManagedExec) Resize(ctx context.Context, cols, rows uint) error {
	if cols == 0 || rows == 0 || cols > 65535 || rows > 65535 {
		return errors.New("runtime: execution terminal dimensions must be between 1 and 65535")
	}
	if e.Attachment() == nil {
		return fmt.Errorf("%w: cannot resize an unavailable PTY", ErrExecUnavailable)
	}
	_, err := e.docker.cli.ExecResize(ctx, e.identity.ExecID, client.ExecResizeOptions{Width: cols, Height: rows})
	return err
}

func (e *dockerManagedExec) Detach() error {
	e.mu.Lock()
	attachment := e.attachment
	e.attachment = nil
	e.mu.Unlock()
	if attachment != nil {
		return attachment.Close()
	}
	return nil
}
