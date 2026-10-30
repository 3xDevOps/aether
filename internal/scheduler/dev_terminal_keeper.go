package scheduler

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/acpkeeper"
	"github.com/3xDevOps/Aether/internal/coordtransport"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/ptyhost"
	"github.com/3xDevOps/Aether/internal/runtime"
)

// terminalKeeper is how a server process reattaches to a development
// terminal that runs under a keeper, and the epoch its output positions
// belong to across server processes.
type terminalKeeper struct {
	keeperRecord
	Epoch ptyhost.TerminalEpoch `json:"epoch"`
}

// startKeptTerminal runs spec's command under a terminal keeper as the
// container's user and returns the keeper's exec once it listens. A command
// the keeper cannot start fails with its *runtime.ExecExitError;
// errNoTerminalKeeper reports a staged binary that cannot run a keeper.
func (s *Scheduler) startKeptTerminal(ctx context.Context, managed runtime.ManagedExecRuntime, live LiveRun, spec runtime.ExecSpec) (runtime.ManagedExec, *terminalKeeper, error) {
	info, err := s.cfg.Runtime.Inspect(ctx, live.ContainerID)
	if err != nil {
		return nil, nil, fmt.Errorf("inspect the container's user: %w", err)
	}
	socket := path.Join(acpkeeper.SocketRoot, strings.ToLower(rand.Text()), "keeper.sock")
	exec, err := managed.StartExecPipe(ctx, live.ContainerID, runtime.ExecSpec{
		Argv:        acpkeeper.TermArgv(coordtransport.CLIPath, socket, normalizeRecoveryRunUser(info.User), envHome(live.Env), spec.Cols, spec.Rows, spec.Argv),
		User:        keeperUser,
		WorkingDir:  spec.WorkingDir,
		CreationKey: spec.CreationKey,
		// Docker sets TERM only for an exec with a terminal of its own.
		Env: append(append([]string(nil), live.Env...), "TERM=xterm"),
	})
	if err != nil {
		return nil, nil, fmt.Errorf("start the terminal keeper: %w", err)
	}
	keeper := &terminalKeeper{keeperRecord: keeperRecord{Socket: socket, Token: rand.Text()}, Epoch: ptyhost.TerminalEpoch(rand.Text())}
	stderr, copied, err := keeperReady(ctx, exec, keeper.Token)
	if err == nil {
		return exec, keeper, nil
	}
	stopCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	exit, stopErr := exec.Stop(stopCtx, 0)
	_ = exec.Detach()
	<-copied
	switch {
	case stopErr != nil:
		// The command may still run: starting it again could run it twice.
		return nil, nil, fmt.Errorf("start the terminal keeper: %w", errors.Join(adapterError(err, stderr), stopErr))
	case exit.Code == 126 || exit.Code == 127:
		return nil, nil, &runtime.ExecExitError{Code: exit.Code}
	}
	return nil, nil, fmt.Errorf("%w: %w", errNoTerminalKeeper, adapterError(err, stderr))
}

// envHome is the HOME env sets, if any.
func envHome(env []string) string {
	home := ""
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "HOME="); ok {
			home = v
		}
	}
	return home
}

var errNoTerminalKeeper = errors.New("the run's staged binary cannot keep a terminal")

// attachTerminalLocked attaches this server process to terminal's keeper as
// its next generation and starts the terminal's PTY session on it, replaying
// the output the keeper retains. Output positions continue those of earlier
// server processes. The caller holds the run's shell lock.
func (s *Scheduler) attachTerminalLocked(ctx context.Context, run domain.RunID, set *runTerminalSet, terminal *runTerminal) error {
	managed, ok := s.cfg.Runtime.(runtime.ManagedExecRuntime)
	host, hostOK := s.cfg.PTY.(DevelopmentPTYHost)
	if !ok || !hostOK {
		return runtime.ErrExecUnavailable
	}
	next := *terminal.Keeper
	next.Gen++
	terminal.Keeper = &next
	// Before the hello, so a later server never reuses the generation.
	if err := s.persistRunTerminalsLocked(run, set); err != nil {
		return fmt.Errorf("record the keeper generation: %w", err)
	}
	bridge, err := managed.StartExecPipe(ctx, terminal.Identity.ContainerID, runtime.ExecSpec{
		Argv:        acpkeeper.AttachArgv(coordtransport.CLIPath, next.Socket),
		User:        keeperUser,
		CreationKey: "terminal-attach-" + rand.Text(),
	})
	if err != nil {
		return fmt.Errorf("start the keeper bridge: %w", err)
	}
	client, err := acpkeeper.AttachTerm(ctx, bridgeConn{bridge.Attachment()}, acpkeeper.Hello{Gen: next.Gen, Token: next.Token}, 0)
	if err == nil {
		state := client.State()
		key := ptyhost.RunShellSession(run, terminal.ID)
		// Earlier server processes answered the terminal queries in the
		// output they were sent.
		err = host.ResumeDevelopmentSession(ctx, key, client, uint(state.Cols), uint(state.Rows),
			ptyhost.TerminalPosition{Epoch: next.Epoch, Sequence: ptyhost.TerminalSequence(client.From())}, ptyhost.TerminalSequence(state.Sent))
		if err == nil {
			terminal.Generation = s.cfg.PTY.SessionGeneration(key)
			terminal.Cols, terminal.Rows = uint(state.Cols), uint(state.Rows)
			terminal.Client, terminal.Bridge = client, bridge
			terminal.Unavailable = ""
			return nil
		}
		_ = client.Close()
	}
	stopCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, stopErr := bridge.Stop(stopCtx, 0); stopErr != nil {
		slog.Warn("scheduler: stop the terminal keeper bridge", "run", run, "terminal", terminal.ID, "error", stopErr)
	}
	_ = bridge.Detach()
	return err
}

// releaseTerminal ends this server process's hold on terminal's streams. A
// kept terminal keeps running.
func releaseTerminal(terminal *runTerminal) error {
	if terminal.Client != nil {
		_ = terminal.Client.Close()
		_ = terminal.Bridge.Detach()
		terminal.Client, terminal.Bridge = nil, nil
	}
	if terminal.Exec != nil {
		return terminal.Exec.Detach()
	}
	return nil
}
