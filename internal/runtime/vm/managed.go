package vm

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/3xDevOps/Aether/internal/coordtransport"
	"github.com/3xDevOps/Aether/internal/devexec"
	"github.com/3xDevOps/Aether/internal/guest"
	"github.com/3xDevOps/Aether/internal/runtime"
)

// startManagedExec runs the staged dev-exec supervisor through the helper,
// as the Docker runtime runs it through docker exec: it waits for a claim
// before it starts the command, so a lost reply cannot orphan the command.
func (e *engine) startManagedExec(ctx context.Context, key string, spec runtime.ExecSpec, tty bool) (_ runtime.ManagedExec, resultErr error) {
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
	if tty && (spec.Cols == 0 || spec.Rows == 0 || spec.Cols > 65535 || spec.Rows > 65535) {
		return nil, errors.New("runtime: execution terminal dimensions must be between 1 and 65535")
	}
	rec, err := e.load(key)
	if err != nil {
		return nil, err
	}
	var token [32]byte
	if _, err = rand.Read(token[:]); err != nil {
		return nil, fmt.Errorf("runtime: create execution claim: %w", err)
	}
	claim := hex.EncodeToString(token[:])
	mode := "run-pipe"
	var size *guest.Size
	if tty {
		mode, size = "run", &guest.Size{Cols: uint16(spec.Cols), Rows: uint16(spec.Rows)}
	}
	env := spec.Env
	if spec.User != "" && spec.User != rec.User {
		if _, gid, ok := strings.Cut(rec.User, ":"); ok && gid != "" {
			env = append(append([]string{}, env...), devexec.ControlGroupEnv+"="+gid)
		}
	}
	execID := processID("managed")
	argv := append([]string{coordtransport.CLIPath, devexec.Command, mode, spec.CreationKey, claim}, spec.Argv...)
	p, att, err := e.spawn(ctx, key, guest.ProcessSpec{ID: execID, Argv: argv, Env: env, User: spec.User, Dir: spec.WorkingDir, TTY: size, Lossless: spec.Lossless})
	if err != nil {
		return nil, fmt.Errorf("runtime: create managed execution: %w", err)
	}
	m := &managedExec{engine: e, key: key, tty: tty, proc: p, attachment: att, identity: runtime.ExecIdentity{
		ContainerID: runtime.ID(idPrefix + key), ExecID: execID, CreationKey: spec.CreationKey, ClaimToken: claim, User: spec.User,
	}}
	published := false
	defer func() {
		if published {
			return
		}
		cleanup, cancel := context.WithTimeout(context.Background(), devexec.StartupTimeout+5*time.Second)
		defer cancel()
		_, stopErr := m.control(cleanup, "stop", 0)
		if stopErr == nil {
			_, stopErr = m.Wait(cleanup)
		}
		_ = m.Detach()
		if stopErr != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("runtime: cancelled execution cleanup: %w", stopErr))
		}
	}()
	if _, err := m.control(ctx, "start", 0); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	published = true
	return m, nil
}

// recoverExec proves the helper's claim. Like Docker's, the recovered
// execution has no attachment: callers reach a recovered command through its
// keeper.
func (e *engine) recoverExec(ctx context.Context, key string, identity runtime.ExecIdentity) (runtime.ManagedExec, error) {
	if identity.ExecID == "" || identity.CreationKey == "" || identity.ClaimToken == "" {
		return nil, fmt.Errorf("%w: incomplete identity", runtime.ErrExecUnavailable)
	}
	if _, err := e.requireRunning(ctx, key, "recover an execution in"); err != nil {
		if errors.Is(err, runtime.ErrNotFound) {
			return nil, err
		}
		return nil, fmt.Errorf("%w: %w: %w", runtime.ErrExecUnavailable, runtime.ErrNotFound, err)
	}
	h, err := e.helper(key)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", runtime.ErrExecUnavailable, err)
	}
	p := h.Process(identity.ExecID)
	m := &managedExec{engine: e, key: key, proc: p, identity: identity}
	_, err = p.Status(ctx)
	if errors.Is(err, guest.ErrNotFound) {
		return nil, fmt.Errorf("%w: %w: execution %s", runtime.ErrExecUnavailable, runtime.ErrNotFound, identity.ExecID)
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %w", runtime.ErrExecUnavailable, err)
	}
	if _, err := m.control(ctx, "status", 0); err != nil {
		return nil, fmt.Errorf("%w: cannot prove helper ownership: %w", runtime.ErrExecUnavailable, err)
	}
	return m, nil
}

type managedExec struct {
	engine   *engine
	key      string
	tty      bool
	proc     process
	identity runtime.ExecIdentity

	mu         sync.Mutex
	attachment runtime.Attachment
}

func (m *managedExec) Identity() runtime.ExecIdentity { return m.identity }

func (m *managedExec) Attachment() runtime.Attachment {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.attachment
}

func (m *managedExec) control(ctx context.Context, action string, grace time.Duration) (devexec.State, error) {
	p, att, err := m.engine.spawn(ctx, m.key, guest.ProcessSpec{
		ID: processID("control"), Argv: runtime.ExecControlArgv(m.identity, action, grace), User: m.identity.User, Lossless: true,
	})
	if err != nil {
		return devexec.State{}, fmt.Errorf("%w: helper %s: %w", runtime.ErrExecUnavailable, action, err)
	}
	defer func() { _ = att.Close() }()
	if err = p.CloseStdin(ctx); err != nil {
		return devexec.State{}, fmt.Errorf("%w: helper %s: %w", runtime.ErrExecUnavailable, action, err)
	}
	stdout, stderr, err := readLimited(att)
	if err != nil {
		return devexec.State{}, fmt.Errorf("%w: helper %s output: %w", runtime.ErrExecUnavailable, action, err)
	}
	code, err := p.Wait(ctx)
	if err != nil {
		return devexec.State{}, fmt.Errorf("%w: helper %s: %w", runtime.ErrExecUnavailable, action, err)
	}
	return runtime.ExecControlState(m.identity, action, code, stdout, stderr)
}

// Status asks the helper whether the supervisor runs; once it has ended, the
// supervisor's own record holds the command's exit.
func (m *managedExec) Status(ctx context.Context) (runtime.ExecState, error) {
	st, err := m.proc.Status(ctx)
	if errors.Is(err, guest.ErrNotFound) {
		return runtime.ExecState{}, fmt.Errorf("%w: %w: execution %s", runtime.ErrExecUnavailable, runtime.ErrNotFound, m.identity.ExecID)
	}
	if err != nil {
		return runtime.ExecState{}, fmt.Errorf("%w: %w", runtime.ErrExecUnavailable, err)
	}
	state := runtime.ExecState{Running: !st.Exited, Attached: m.Attachment() != nil}
	if !state.Attached {
		state.UnavailableReason = "attachment is unavailable; the execution was detached"
	}
	if !st.Exited {
		return state, nil
	}
	final, err := m.control(ctx, "status", 0)
	if err != nil {
		return state, fmt.Errorf("%w: command exit record unavailable: %w", runtime.ErrExecUnavailable, err)
	}
	if !final.Exited || final.ExitCode == nil {
		return state, fmt.Errorf("%w: helper ended without a command exit record", runtime.ErrExecUnavailable)
	}
	state.Running, state.Exited, state.ExitCode = false, true, final.ExitCode
	return state, nil
}

func (m *managedExec) Wait(ctx context.Context) (runtime.ExitStatus, error) {
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		state, err := m.Status(ctx)
		if err != nil {
			return runtime.ExitStatus{}, err
		}
		if state.Exited {
			return runtime.ExitStatus{Code: *state.ExitCode}, nil
		}
		select {
		case <-ctx.Done():
			return runtime.ExitStatus{}, ctx.Err()
		case <-tick.C:
		}
	}
}

func (m *managedExec) Stop(ctx context.Context, grace time.Duration) (runtime.ExitStatus, error) {
	if grace < 0 {
		grace = 3 * time.Second
	}
	if grace > time.Minute {
		return runtime.ExitStatus{}, errors.New("runtime: managed execution stop grace exceeds one minute")
	}
	ctx, cancel := context.WithTimeout(ctx, devexec.StartupTimeout+grace+devexec.ReapTimeout)
	defer cancel()
	st, err := m.proc.Status(ctx)
	if err != nil && !errors.Is(err, guest.ErrNotFound) {
		return runtime.ExitStatus{}, fmt.Errorf("%w: %w", runtime.ErrExecUnavailable, err)
	}
	if err == nil && !st.Exited {
		if _, err := m.control(ctx, "stop", grace); err != nil {
			return runtime.ExitStatus{}, err
		}
	}
	return m.Wait(ctx)
}

func (m *managedExec) Resize(ctx context.Context, cols, rows uint) error {
	if cols == 0 || rows == 0 || cols > 65535 || rows > 65535 {
		return errors.New("runtime: execution terminal dimensions must be between 1 and 65535")
	}
	if m.Attachment() == nil {
		return fmt.Errorf("%w: cannot resize an unavailable PTY", runtime.ErrExecUnavailable)
	}
	if !m.tty {
		return errors.New("runtime: resize: execution has no terminal")
	}
	return m.proc.Resize(ctx, guest.Size{Cols: uint16(cols), Rows: uint16(rows)})
}

func (m *managedExec) Detach() error {
	m.mu.Lock()
	attachment := m.attachment
	m.attachment = nil
	m.mu.Unlock()
	if attachment != nil {
		return attachment.Close()
	}
	return nil
}
