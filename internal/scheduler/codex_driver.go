package scheduler

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/3xDevOps/Aether/internal/agentstatus"
	"github.com/3xDevOps/Aether/internal/codexapp"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/harness"
	"github.com/3xDevOps/Aether/internal/ptyhost"
	"github.com/3xDevOps/Aether/internal/runtime"
)

const (
	// appServerConnectTimeout outlasts the bridge's wait for a cold
	// app-server.
	appServerConnectTimeout = 90 * time.Second
	appServerReconnectDelay = time.Second
	appServerFailedReason   = "codex app-server unavailable: "
	appServerAskingReason   = "waiting for your answer in the terminal"
)

var (
	errAppServerPaused      = errors.New("the container is paused")
	errAppServerUnavailable = errors.New("scheduler: the session's Codex app-server is not connected")
)

// appServerDriver hosts a Standard Codex run. The primary PTY runs Codex's
// terminal attached to its app-server (codexapp.Launch), and a bridge exec
// beside it is the server's JSON-RPC client of that app-server: the run
// reports its turns through it and takes mail and messages as turns or
// steers. Without a client, the driver is the tui driver.
type appServerDriver struct {
	tuiDriver
	s *Scheduler

	mu     sync.Mutex
	closed bool
	runs   map[domain.RunID]*appServerRun
}

type appServerRun struct {
	// ready is closed once connecting settles; client is nil if it failed.
	ready    chan struct{}
	client   *codexapp.Client
	exec     runtime.ManagedExec
	stopping bool
	// ctx ends with the connection, so a report still waiting for a
	// provisioning run is dropped once a newer connection replaces it.
	ctx    context.Context
	cancel context.CancelFunc
	// pending are the statuses not yet reported, which one reporter at a
	// time hands on in order.
	pending   []threadStatus
	reporting bool
}

type threadStatus struct {
	thread string
	status codexapp.Status
}

func newAppServerDriver(s *Scheduler) *appServerDriver {
	return &appServerDriver{tuiDriver: tuiDriver{pty: s.cfg.PTY}, s: s, runs: make(map[domain.RunID]*appServerRun)}
}

func (d *appServerDriver) Start(ctx context.Context, entry *supervised, att runtime.Attachment) error {
	if err := d.tuiDriver.Start(ctx, entry, att); err != nil {
		return err
	}
	d.connect(entry, nil)
	return nil
}

func (d *appServerDriver) Resume(ctx context.Context, entry *supervised, att runtime.Attachment) error {
	return d.Start(ctx, entry, att)
}

func (d *appServerDriver) Stop(ctx context.Context, run domain.RunID) error {
	d.mu.Lock()
	r := d.runs[run]
	delete(d.runs, run)
	d.mu.Unlock()
	if r != nil {
		d.close(r)
	}
	return d.tuiDriver.Stop(ctx, run)
}

func (d *appServerDriver) Deliver(ctx context.Context, run *domain.Run, member *domain.Member, prompt domain.AgentPrompt, steer bool, delivered func(error)) (string, error) {
	client := d.client(ctx, run.ID)
	if client == nil || client.Thread() == "" {
		return d.tuiDriver.Deliver(ctx, run, member, prompt, steer, delivered)
	}
	text, sent := prompt.TextWithAttachments(), time.Now().UTC()
	if err := client.Send(ctx, text); err != nil {
		return "", err
	}
	if err := d.s.answerReached(run.ID, sent); err != nil {
		slog.Warn("scheduler: a member's message reached the agent", "run", run.ID, "error", err)
	}
	return "", d.pty.Annotate(ctx, ptyhost.RunSession(run.ID), member.DisplayName, member.Color, text)
}

// wake hands mail to the run's terminal. A turn it starts is the server's,
// which does not answer a question the run shows.
func (d *appServerDriver) wake(ctx context.Context, run domain.RunID, prompt string) error {
	client := d.connected(run)
	if client == nil {
		return errAppServerUnavailable
	}
	d.serverTurn(run, true)
	err := client.Send(ctx, prompt)
	if err != nil {
		d.serverTurn(run, false)
	}
	return err
}

func (d *appServerDriver) serverTurn(run domain.RunID, on bool) {
	d.s.mu.Lock()
	defer d.s.mu.Unlock()
	entry := d.s.runs[run]
	if entry == nil || entry.serverTurn == on {
		return
	}
	entry.serverTurn = on
	if err := d.s.writeSidecar(entry.sidecar()); err != nil {
		slog.Warn("scheduler: persist the run's mail turn", "run", run, "error", err)
	}
}

// client waits for a connection in progress, up to acpSteerTimeout, and is
// nil without a live one.
func (d *appServerDriver) client(ctx context.Context, run domain.RunID) *codexapp.Client {
	d.mu.Lock()
	r := d.runs[run]
	d.mu.Unlock()
	if r == nil {
		return nil
	}
	select {
	case <-r.ready:
	case <-ctx.Done():
		return nil
	case <-time.After(acpSteerTimeout):
		return nil
	}
	return d.live(r)
}

func (d *appServerDriver) live(r *appServerRun) *codexapp.Client {
	d.mu.Lock()
	defer d.mu.Unlock()
	if r.stopping || r.client == nil {
		return nil
	}
	select {
	case <-r.client.Done():
		return nil
	default:
		return r.client
	}
}

// connected is the run's live client once it follows a thread, which takes
// mail as a turn when idle and as a steer mid-turn.
func (d *appServerDriver) connected(run domain.RunID) *codexapp.Client {
	d.mu.Lock()
	r := d.runs[run]
	d.mu.Unlock()
	if r == nil {
		return nil
	}
	select {
	case <-r.ready:
	default:
		return nil
	}
	if client := d.live(r); client != nil && client.Thread() != "" {
		return client
	}
	return nil
}

// resumeAfterPause connects a run whose last attempt failed, as one does
// while its container is paused. A client that connected and then ended
// had its terminal exit.
func (d *appServerDriver) resumeAfterPause(entry *supervised) {
	d.mu.Lock()
	r := d.runs[entry.runID]
	d.mu.Unlock()
	if r != nil {
		select {
		case <-r.ready:
			d.mu.Lock()
			connected := r.client != nil
			d.mu.Unlock()
			if connected {
				return
			}
		default:
			return
		}
	}
	d.connect(entry, r)
}

// connect replaces the run's connection, or with replaces set, only that
// one if it is still the run's.
func (d *appServerDriver) connect(entry *supervised, replaces *appServerRun) {
	r := &appServerRun{ready: make(chan struct{})}
	r.ctx, r.cancel = context.WithCancel(context.Background())
	d.mu.Lock()
	old := d.runs[entry.runID]
	if d.closed || replaces != nil && (old != replaces || replaces.stopping) {
		d.mu.Unlock()
		r.cancel()
		return
	}
	d.runs[entry.runID] = r
	d.mu.Unlock()
	if old != nil {
		d.close(old)
	}
	go d.serve(entry, r)
}

func (d *appServerDriver) serve(entry *supervised, r *appServerRun) {
	client, exec, err := d.dial(entry, r)
	d.mu.Lock()
	r.client, r.exec = client, exec
	stopping := r.stopping
	d.mu.Unlock()
	close(r.ready)
	d.s.mu.Lock()
	paused := entry.paused
	d.s.mu.Unlock()
	switch {
	case errors.Is(err, errAppServerPaused) || err != nil && paused:
		return
	case err != nil:
		if !stopping {
			slog.Warn("scheduler: connect to the run's Codex app-server", "run", entry.runID, "error", err)
			d.report(entry, r, agentstatus.Report{State: agentstatus.Idle, Reason: appServerFailedReason + err.Error()})
		}
		return
	case stopping:
		d.close(r)
		return
	}
	if c := d.s.coordinationSeam(); c != nil && c.enabled {
		c.svc.EnhancedSessionOpened(entry.runID)
	}
	<-client.Done()
	d.mu.Lock()
	stopping = r.stopping
	exec, r.exec = r.exec, nil
	d.mu.Unlock()
	if stopping {
		return
	}
	stopBridge(exec)
	// A lost attachment ends the client while the terminal goes on; once the
	// terminal exits, its app-server is gone and the new connect says so.
	time.Sleep(appServerReconnectDelay)
	d.connect(entry, r)
}

func (d *appServerDriver) dial(entry *supervised, r *appServerRun) (*codexapp.Client, runtime.ManagedExec, error) {
	d.s.mu.Lock()
	cid, paused, session := entry.containerID, entry.paused, entry.agentSessionID
	d.s.mu.Unlock()
	if paused {
		return nil, nil, errAppServerPaused
	}
	managed, ok := d.s.cfg.Runtime.(runtime.ManagedExecRuntime)
	if !ok {
		return nil, nil, fmt.Errorf("%w: the runtime cannot run a managed exec", runtime.ErrExecUnavailable)
	}
	ctx, cancel := context.WithTimeout(context.Background(), appServerConnectTimeout)
	defer cancel()
	exec, err := managed.StartExecPipe(ctx, cid, runtime.ExecSpec{
		Argv: codexapp.BridgeCommand, WorkingDir: d.s.cfg.WorktreeMount, CreationKey: "codex-" + rand.Text(),
	})
	if err != nil {
		return nil, nil, fmt.Errorf("start the bridge: %w", err)
	}
	att := exec.Attachment()
	stderr := &tailBuffer{max: acpStderrTail}
	copied := make(chan struct{})
	go func() {
		_, _ = io.Copy(stderr, att.Stderr())
		close(copied)
	}()
	client, err := codexapp.Dial(ctx, att.Stdout(), att.Stdin(), codexapp.Config{
		Session:  session,
		OnStatus: func(thread string, status codexapp.Status) { d.status(entry, r, thread, status) },
	})
	if err != nil {
		stopBridge(exec)
		select {
		case <-copied:
		case <-time.After(time.Second):
		}
		if tail := strings.TrimSpace(stderr.String()); tail != "" {
			err = fmt.Errorf("%w: %s", err, tail)
		}
		return nil, nil, err
	}
	return client, exec, nil
}

func (d *appServerDriver) close(r *appServerRun) {
	d.mu.Lock()
	r.stopping = true
	client, exec := r.client, r.exec
	r.exec = nil
	d.mu.Unlock()
	r.cancel()
	if client != nil {
		_ = client.Close()
	}
	if exec != nil {
		stopBridge(exec)
	}
}

func stopBridge(exec runtime.ManagedExec) {
	ctx, cancel := context.WithTimeout(context.Background(), acpStopGrace+10*time.Second)
	defer cancel()
	if _, err := exec.Stop(ctx, acpStopGrace); err != nil {
		slog.Warn("scheduler: stop the Codex app-server bridge", "exec", exec.Identity().ExecID, "error", err)
	}
	_ = exec.Detach()
}

// status runs on the client's notification loop, so the report, which can
// wait for a provisioning run, runs beside it.
func (d *appServerDriver) status(entry *supervised, r *appServerRun, thread string, status codexapp.Status) {
	d.mu.Lock()
	r.pending = append(r.pending, threadStatus{thread, status})
	if r.reporting {
		d.mu.Unlock()
		return
	}
	r.reporting = true
	d.mu.Unlock()
	go func() {
		for {
			d.mu.Lock()
			if len(r.pending) == 0 || r.stopping {
				r.pending, r.reporting = nil, false
				d.mu.Unlock()
				return
			}
			thread, status := r.pending[0].thread, r.pending[0].status
			r.pending = r.pending[1:]
			d.mu.Unlock()
			report := agentstatus.Report{State: agentstatus.Idle, Reason: agentstatus.ReasonIdle, SessionID: thread}
			switch status {
			case codexapp.Working:
				report = agentstatus.Report{State: agentstatus.Working, SessionID: thread}
			case codexapp.Asking:
				report.Reason = appServerAskingReason
			case codexapp.Failed:
				report.Reason = agentstatus.ReasonFailed
			}
			if status == codexapp.Idle || status == codexapp.Failed {
				d.serverTurn(entry.runID, false)
			}
			d.report(entry, r, report)
			// A turn start is where the hooks announced overlapping edits; a
			// turn end retries owed mail.
			if status == codexapp.Idle || status == codexapp.Working {
				if c := d.s.coordinationSeam(); c != nil && c.enabled {
					c.svc.WakeIdle(entry.runID)
				}
			}
		}
	}()
}

// report follows conflict policy, as lifecycle reporting does, and only
// while r is the run's connection.
func (d *appServerDriver) report(entry *supervised, r *appServerRun, report agentstatus.Report) {
	d.s.mu.Lock()
	reporter := entry.reporter
	d.s.mu.Unlock()
	d.mu.Lock()
	stale := d.closed || r.stopping
	d.mu.Unlock()
	if stale || reporter != harness.ReporterFull {
		return
	}
	if err := d.s.ReportAgentState(r.ctx, entry.runID, report); err != nil {
		slog.Debug("scheduler: Codex app-server report not applied", "run", entry.runID, "error", err)
	}
}

// shutdown closes every client without reporting, as the end of the server
// process would; each bridge exits with its stdin.
func (d *appServerDriver) shutdown() {
	d.mu.Lock()
	d.closed = true
	runs := make([]*appServerRun, 0, len(d.runs))
	for _, r := range d.runs {
		r.stopping = true
		r.cancel()
		runs = append(runs, r)
	}
	d.mu.Unlock()
	for _, r := range runs {
		d.mu.Lock()
		client := r.client
		d.mu.Unlock()
		if client != nil {
			_ = client.Close()
		}
	}
}
