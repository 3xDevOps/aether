package scheduler

import (
	"bufio"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/acpkeeper"
	"github.com/3xDevOps/Aether/internal/collab"
	"github.com/3xDevOps/Aether/internal/coordtransport"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/runtime"
)

// keeperRecord is how a server process reattaches to the keeper of a run's
// adapter: its container-local socket, the token the server gave it, and the
// newest generation that attached.
type keeperRecord struct {
	Socket string `json:"socket"`
	Token  string `json:"token"`
	Gen    uint64 `json:"gen"`
}

// keeperUser runs the keeper and its bridge as root, so the agent's user
// cannot reach the keeper's socket, its token or its memory.
const keeperUser = "0"

// startKeeper starts adapter under a keeper and returns the keeper's exec
// once it listens. The keeper starts adapter as the container's user, as an
// exec without a user would run. The run records the exec and how to
// reattach before the adapter can do anything.
func (d *acpDriver) startKeeper(ctx context.Context, entry *supervised, managed runtime.ManagedExecRuntime, cid runtime.ID, adapter []string) (runtime.ManagedExec, error) {
	info, err := d.s.cfg.Runtime.Inspect(ctx, cid)
	if err != nil {
		return nil, fmt.Errorf("inspect the container's user: %w", err)
	}
	socket := path.Join(acpkeeper.SocketRoot, strings.ToLower(rand.Text()), "keeper.sock")
	exec, err := managed.StartExecPipe(ctx, cid, runtime.ExecSpec{
		Argv:        acpkeeper.ServeArgv(coordtransport.CLIPath, socket, normalizeRecoveryRunUser(info.User), adapter),
		User:        keeperUser,
		WorkingDir:  d.s.cfg.WorktreeMount,
		CreationKey: "acp-" + rand.Text(),
		// A container started for a Standard run lacks both.
		Env: []string{coordtransport.EnhancedEnv + "=1", "NO_BROWSER=1"},
	})
	if err != nil {
		return nil, fmt.Errorf("start %s: %w", adapter[0], err)
	}
	record := &keeperRecord{Socket: socket, Token: rand.Text()}
	identity := exec.Identity()
	if err = d.persist(entry, func() { entry.agentExec, entry.agentKeeper = &identity, record }); err != nil {
		if stopErr := d.stopExec(exec); stopErr != nil {
			slog.Warn("scheduler: stop the unrecorded ACP keeper", "run", entry.runID, "error", stopErr)
		}
		return nil, fmt.Errorf("record the keeper: %w", err)
	}
	att := exec.Attachment()
	stderr := &tailBuffer{max: acpStderrTail}
	copied := make(chan struct{})
	go func() {
		_, _ = io.Copy(stderr, att.Stderr())
		close(copied)
	}()
	ready := make(chan error, 1)
	go func() {
		if _, werr := io.WriteString(att.Stdin(), record.Token+"\n"); werr != nil {
			ready <- fmt.Errorf("hand the keeper its token: %w", werr)
			return
		}
		line, rerr := bufio.NewReader(att.Stdout()).ReadString('\n')
		if rerr == nil && strings.TrimSpace(line) != "ready" {
			rerr = fmt.Errorf("unexpected keeper output %q", line)
		}
		ready <- rerr
	}()
	select {
	case err = <-ready:
	case <-ctx.Done():
		err = ctx.Err()
	}
	if err != nil {
		if stopErr := d.stopExec(exec); stopErr != nil {
			slog.Warn("scheduler: stop the failed ACP keeper", "run", entry.runID, "error", stopErr)
		}
		<-copied
		return nil, adapterError(fmt.Errorf("start %s under its keeper: %w", adapter[0], err), stderr)
	}
	// The keeper no longer uses the exec's stdio, which ends with this
	// server process.
	_ = exec.Detach()
	return exec, nil
}

// startKept starts adapter under a new keeper and attaches to it.
func (d *acpDriver) startKept(ctx context.Context, entry *supervised, managed runtime.ManagedExecRuntime, cid runtime.ID, adapter []string) (runtime.ManagedExec, *acpkeeper.Client, runtime.ManagedExec, error) {
	exec, err := d.startKeeper(ctx, entry, managed, cid, adapter)
	if err != nil {
		return nil, nil, nil, err
	}
	keeper, bridge, err := d.attachKeeper(ctx, entry, managed, cid)
	if err != nil {
		if stopErr := d.stopExec(exec); stopErr != nil {
			slog.Warn("scheduler: stop the ACP keeper", "run", entry.runID, "error", stopErr)
		} else {
			d.forget(entry, exec)
		}
		return nil, nil, nil, fmt.Errorf("attach to %s's keeper: %w", adapter[0], err)
	}
	return exec, keeper, bridge, nil
}

// startDirect starts adapter as a managed exec whose stdio is this server
// process's.
func (d *acpDriver) startDirect(ctx context.Context, entry *supervised, managed runtime.ManagedExecRuntime, cid runtime.ID, adapter []string, stderr *tailBuffer) (runtime.ManagedExec, error) {
	exec, err := managed.StartExecPipe(ctx, cid, runtime.ExecSpec{
		Argv: adapter, WorkingDir: d.s.cfg.WorktreeMount, CreationKey: "acp-" + rand.Text(),
		Env: []string{coordtransport.EnhancedEnv + "=1", "NO_BROWSER=1"},
	})
	if err != nil {
		return nil, fmt.Errorf("start %s: %w", adapter[0], err)
	}
	identity := exec.Identity()
	d.record(entry, func() { entry.agentExec, entry.agentKeeper = &identity, nil })
	go func() { _, _ = io.Copy(stderr, exec.Attachment().Stderr()) }()
	return exec, nil
}

// forget drops the run's record of exec, which no longer runs.
func (d *acpDriver) forget(entry *supervised, exec runtime.ManagedExec) {
	d.record(entry, func() {
		if entry.agentExec != nil && entry.agentExec.ExecID == exec.Identity().ExecID {
			entry.agentExec, entry.agentKeeper = nil, nil
		}
	})
}

// release ends the run's attachment to its keeper, if it has one.
func (r *acpRun) release() {
	if r.keeper != nil {
		_ = r.keeper.Close()
		_ = r.bridge.Detach()
	}
}

// exit is how the adapter ended, when its keeper reported it.
func (r *acpRun) exit() *acpkeeper.Exit {
	if r.keeper == nil {
		return nil
	}
	return r.keeper.Exit()
}

// attachKeeper connects to the run's keeper through a bridge exec as the
// next generation. The bridge ends with the attachment.
func (d *acpDriver) attachKeeper(ctx context.Context, entry *supervised, managed runtime.ManagedExecRuntime, cid runtime.ID) (*acpkeeper.Client, runtime.ManagedExec, error) {
	d.s.mu.Lock()
	record := entry.agentKeeper
	d.s.mu.Unlock()
	if record == nil {
		return nil, nil, errors.New("the run records no ACP keeper")
	}
	next := *record
	next.Gen++
	// Before the hello, so a later server never reuses the generation.
	if err := d.persist(entry, func() { entry.agentKeeper = &next }); err != nil {
		return nil, nil, fmt.Errorf("record the keeper generation: %w", err)
	}
	bridge, err := managed.StartExecPipe(ctx, cid, runtime.ExecSpec{
		Argv:        acpkeeper.AttachArgv(coordtransport.CLIPath, next.Socket),
		User:        keeperUser,
		CreationKey: "acp-attach-" + rand.Text(),
	})
	if err != nil {
		return nil, nil, fmt.Errorf("start the keeper bridge: %w", err)
	}
	att := bridge.Attachment()
	stderr := &tailBuffer{max: acpStderrTail}
	go func() { _, _ = io.Copy(stderr, att.Stderr()) }()
	client, err := acpkeeper.Attach(ctx, bridgeConn{att}, acpkeeper.Hello{Gen: next.Gen, Token: next.Token})
	if err != nil {
		d.stopBridge(entry.runID, bridge)
		return nil, nil, adapterError(err, stderr)
	}
	return client, bridge, nil
}

// reattach takes over the keeper an earlier server process left running.
func (d *acpDriver) reattach(ctx context.Context, entry *supervised, managed runtime.ManagedExecRuntime, cid runtime.ID) (runtime.ManagedExec, *acpkeeper.Client, runtime.ManagedExec, error) {
	d.s.mu.Lock()
	identity := entry.agentExec
	d.s.mu.Unlock()
	if identity == nil {
		return nil, nil, nil, errors.New("the run records no ACP keeper exec")
	}
	exec, err := managed.RecoverExec(ctx, *identity)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("recover the keeper exec: %w", err)
	}
	client, bridge, err := d.attachKeeper(ctx, entry, managed, cid)
	if err != nil {
		return nil, nil, nil, err
	}
	if state := client.State(); len(state.Initialize) == 0 || len(state.Session) == 0 || state.SessionID == "" {
		_ = client.Close()
		d.stopBridge(entry.runID, bridge)
		return nil, nil, nil, errors.New("the server stopped before the agent's session opened")
	}
	return exec, client, bridge, nil
}

// Reattached reports whether this server process adopted run's session from
// the agent's keeper, with the tags of the requests an earlier server process
// left with the agent. A paused run with a keeper is undecided until it
// resumes.
func (s *Scheduler) Reattached(run domain.RunID) (delivered []string, state collab.Reattach) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch entry := s.runs[run]; {
	case entry == nil:
		return nil, collab.Restored
	case entry.keeperAdopted:
		return entry.keeperDelivered, collab.Adopted
	case entry.paused && entry.agentKeeper != nil:
		return nil, collab.Undecided
	}
	return nil, collab.Restored
}

func (d *acpDriver) stopExec(exec runtime.ManagedExec) error {
	ctx, cancel := context.WithTimeout(context.Background(), acpStopGrace+10*time.Second)
	defer cancel()
	defer func() { _ = exec.Detach() }()
	_, err := exec.Stop(ctx, acpStopGrace)
	if err == nil {
		return nil
	}
	if state, statusErr := exec.Status(ctx); statusErr == nil && !state.Running {
		return nil
	}
	return fmt.Errorf("stop the agent's ACP server (exec %s): %w", exec.Identity().ExecID, err)
}

// stopStaleExec stops the keeper, or the adapter an older server ran without
// one, that an earlier open or server process started and that is not
// reattached.
func (d *acpDriver) stopStaleExec(ctx context.Context, entry *supervised) {
	d.s.mu.Lock()
	stale := entry.agentExec
	d.s.mu.Unlock()
	if stale == nil {
		return
	}
	if managed, ok := d.s.cfg.Runtime.(runtime.ManagedExecRuntime); ok {
		if exec, err := managed.RecoverExec(ctx, *stale); err == nil {
			if stopErr := d.stopExec(exec); stopErr != nil {
				slog.Warn("scheduler: stop the earlier ACP server", "run", entry.runID, "error", stopErr)
			}
		} else {
			slog.Info("scheduler: earlier ACP server is gone", "run", entry.runID, "error", err)
		}
	}
	d.record(entry, func() { entry.agentExec, entry.agentKeeper = nil, nil })
}

func (d *acpDriver) stopBridge(run domain.RunID, bridge runtime.ManagedExec) {
	if err := d.stopExec(bridge); err != nil {
		slog.Warn("scheduler: stop the ACP keeper bridge", "run", run, "error", err)
	}
}

// bridgeConn is the bridge exec's stdio as one stream. Closing it ends the
// bridge's input, which detaches it from the keeper.
type bridgeConn struct{ att runtime.Attachment }

func (c bridgeConn) Read(p []byte) (int, error)  { return c.att.Stdout().Read(p) }
func (c bridgeConn) Write(p []byte) (int, error) { return c.att.Stdin().Write(p) }
func (c bridgeConn) Close() error                { return c.att.Stdin().Close() }
