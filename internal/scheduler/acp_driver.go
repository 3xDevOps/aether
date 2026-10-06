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

	acp "github.com/coder/acp-go-sdk"

	"github.com/3xDevOps/Aether/internal/acphost"
	"github.com/3xDevOps/Aether/internal/agentstatus"
	"github.com/3xDevOps/Aether/internal/coordtransport"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/ptyhost"
	"github.com/3xDevOps/Aether/internal/runtime"
)

const (
	// acpConnectTimeout bounds starting the adapter and opening its session:
	// a cold npm adapter takes seconds, a hung one must not hold a launch.
	acpConnectTimeout = time.Minute
	acpStopGrace      = 3 * time.Second
	acpStderrTail     = 4 << 10
	// acpReportWait bounds how long a report waits for its run to leave
	// provisioning: the adapter can answer before the launch marks the run
	// running.
	acpReportWait = 30 * time.Second
	// acpSteerTimeout bounds a delivery, which runs under the run's
	// admission lock and may wait on the agent's answer to a steer.
	acpSteerTimeout = 15 * time.Second
)

// ErrACPNotRunning rejects input for an enhanced run with no live session.
var ErrACPNotRunning = errors.New("scheduler: the enhanced session is not running")

// The dashboard matches these reason prefixes (web/src/lib/needs-you.ts).
const (
	acpFailedReason = "enhanced session failed: "
	acpEndedReason  = "enhanced session ended: "
)

// acpDriver hosts an enhanced run: the container's primary PTY is a login
// shell, and the agent's ACP server runs beside it as a managed exec whose
// stdio the acphost session owns. The adapter never outlives that pipe, so
// every reattach stops any earlier exec before starting a fresh one.
type acpDriver struct {
	s *Scheduler

	mu      sync.Mutex
	closed  bool
	runs    map[domain.RunID]*acpRun
	ops     map[domain.RunID]*opLock
	waiters map[domain.RunID]*waiter
}

// opLock and waiter entries live only while someone holds or waits on them.
type opLock struct {
	mu   sync.Mutex
	refs int
}

type waiter struct {
	ch   chan struct{}
	refs int
}

type acpRun struct {
	session  *acphost.Session
	exec     runtime.ManagedExec
	stderr   *tailBuffer
	err      error
	stopping bool
}

func newACPDriver(s *Scheduler) *acpDriver {
	return &acpDriver{
		s:       s,
		runs:    make(map[domain.RunID]*acpRun),
		ops:     make(map[domain.RunID]*opLock),
		waiters: make(map[domain.RunID]*waiter),
	}
}

// lockOp takes the run's op lock and returns its unlock.
func (d *acpDriver) lockOp(run domain.RunID) func() {
	d.mu.Lock()
	l := d.ops[run]
	if l == nil {
		l = new(opLock)
		d.ops[run] = l
	}
	l.refs++
	d.mu.Unlock()
	l.mu.Lock()
	return func() {
		l.mu.Unlock()
		d.mu.Lock()
		defer d.mu.Unlock()
		if l.refs--; l.refs == 0 {
			delete(d.ops, run)
		}
	}
}

func (d *acpDriver) Start(ctx context.Context, entry *supervised, att runtime.Attachment) error {
	if err := d.s.cfg.PTY.StartSession(ctx, ptyhost.RunSession(entry.runID), att); err != nil {
		return err
	}
	d.connect(ctx, entry, true)
	return nil
}

func (d *acpDriver) Resume(ctx context.Context, entry *supervised, att runtime.Attachment) error {
	if err := d.s.cfg.PTY.StartSession(ctx, ptyhost.RunSession(entry.runID), att); err != nil {
		return err
	}
	d.connect(ctx, entry, false)
	return nil
}

func (d *acpDriver) Stop(ctx context.Context, run domain.RunID) error {
	d.stopAdapter(ctx, run)
	return d.s.cfg.PTY.StopSession(ctx, ptyhost.RunSession(run))
}

func (d *acpDriver) LastActivity(run domain.RunID) time.Time {
	if sess := d.session(run); sess != nil {
		return sess.State().LastActivity
	}
	return time.Time{}
}

func (d *acpDriver) Deliver(ctx context.Context, run *domain.Run, _ *domain.Member, message string, steer bool) (string, error) {
	sess, err := d.live(run.ID)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, acpSteerTimeout)
	defer cancel()
	receipt, err := sess.Prompt(ctx, []acp.ContentBlock{acp.TextBlock(message)}, steer)
	return receipt.Outcome, err
}

func (d *acpDriver) session(run domain.RunID) *acphost.Session {
	d.mu.Lock()
	defer d.mu.Unlock()
	if r := d.runs[run]; r != nil {
		return r.session
	}
	return nil
}

func (d *acpDriver) live(run domain.RunID) (*acphost.Session, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	r := d.runs[run]
	switch {
	case r != nil && r.session != nil:
		return r.session, nil
	case r != nil && r.err != nil:
		return nil, fmt.Errorf("%w: %w", ErrACPNotRunning, r.err)
	}
	return nil, ErrACPNotRunning
}

// started returns the live session, or else a channel closed when the next
// one starts and the release for it, both read under one lock.
func (d *acpDriver) started(run domain.RunID) (*acphost.Session, <-chan struct{}, func()) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if r := d.runs[run]; r != nil && r.session != nil {
		return r.session, nil, func() {}
	}
	w := d.waiters[run]
	if w == nil {
		w = &waiter{ch: make(chan struct{})}
		d.waiters[run] = w
	}
	w.refs++
	var once sync.Once
	return nil, w.ch, func() {
		once.Do(func() {
			d.mu.Lock()
			defer d.mu.Unlock()
			if w.refs--; w.refs == 0 && d.waiters[run] == w {
				delete(d.waiters, run)
			}
		})
	}
}

func (d *acpDriver) connect(ctx context.Context, entry *supervised, fresh bool) {
	defer d.lockOp(entry.runID)()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), acpConnectTimeout)
	defer cancel()
	d.stopAdapterLocked(ctx, entry.runID)
	d.stopStaleExec(ctx, entry)

	d.s.mu.Lock()
	paused, cid, coordDir, cachedSession, task := entry.paused, entry.containerID, entry.coordDir, entry.agentSessionID, entry.task
	d.s.mu.Unlock()
	if paused {
		// Docker cannot exec into a frozen container; Resume connects.
		d.setRun(entry.runID, &acpRun{err: errors.New("the container is paused")})
		return
	}
	run, err := d.s.cfg.Store.GetRun(ctx, entry.runID)
	if err != nil {
		d.fail(entry, fmt.Errorf("load run: %w", err))
		return
	}
	profile, argv, err := d.s.launchProfile(ctx, run.MemberID, run.AccountMember(), run.Harness)
	if err != nil {
		d.fail(entry, err)
		return
	}
	adapter := argv[domain.LaunchACP]
	if len(adapter) == 0 {
		d.fail(entry, fmt.Errorf("agent %q has no ACP command", run.Harness))
		return
	}
	managed, ok := d.s.cfg.Runtime.(runtime.ManagedExecRuntime)
	if !ok {
		d.fail(entry, fmt.Errorf("%w: the runtime cannot run a managed exec", runtime.ErrExecUnavailable))
		return
	}
	sessionID, mode := "", profile.ACPMode
	if !fresh {
		sessionID = run.HarnessSessionID
		if sessionID == "" {
			sessionID = cachedSession
		}
		// A restored session may come back in the agent's default mode.
		if recorded := d.s.recordedMode(entry.runID); recorded != "" {
			mode = recorded
		}
	}
	exec, err := managed.StartExecPipe(ctx, cid, runtime.ExecSpec{
		Argv: adapter, WorkingDir: d.s.cfg.WorktreeMount, CreationKey: "acp-" + rand.Text(),
	})
	if err != nil {
		d.fail(entry, fmt.Errorf("start %s: %w", adapter[0], err))
		return
	}
	identity := exec.Identity()
	d.record(entry, func() { entry.agentExec = &identity })
	att := exec.Attachment()
	stderr := &tailBuffer{max: acpStderrTail}
	go func() { _, _ = io.Copy(stderr, att.Stderr()) }()

	var mcp []acp.McpServer
	if coordDir != "" {
		mcp = []acp.McpServer{{Stdio: &acp.McpServerStdio{
			Name: "aether", Command: coordtransport.BinaryPath, Args: []string{"mcp"}, Env: []acp.EnvVariable{},
		}}}
	}
	runID := entry.runID
	sess, err := acphost.Start(ctx, att.Stdout(), att.Stdin(), acphost.Config{
		LogPath:    d.s.cfg.PTY.ItemLogPath(runID),
		Cwd:        d.s.cfg.WorktreeMount,
		MCPServers: mcp,
		SessionID:  sessionID,
		Logger:     slog.Default().With("run", runID),
		OnState: func(working bool, _ string) {
			report := agentstatus.Report{State: agentstatus.Working}
			if !working {
				report = agentstatus.Report{State: agentstatus.Idle, Reason: agentstatus.ReasonIdle}
			}
			d.report(runID, report)
		},
		OnInputs: func(pending []domain.RunInputRequest) {
			d.report(runID, agentstatus.Report{InputUpdates: []domain.RunInputUpdate{{Operation: "replace", Requests: pending}}})
		},
		OnActivity: func(verb, target string) { d.activity(entry, verb, target) },
	})
	if err != nil {
		d.stopExec(exec)
		d.fail(entry, adapterError(fmt.Errorf("open the agent session: %w", err), stderr))
		return
	}
	if id := sess.SessionID(); id != run.HarnessSessionID {
		if err := d.s.cfg.Store.SetRunAgentSession(ctx, runID, id); err != nil {
			slog.Warn("scheduler: record agent session", "run", runID, "error", err)
		}
	}
	d.record(entry, func() { entry.agentSessionID = sess.SessionID() })
	if mode != "" && sess.State().Mode != mode {
		if err := sess.SetMode(ctx, mode); err != nil {
			slog.Warn("scheduler: set the agent's mode", "run", runID, "mode", mode, "error", err)
		}
	}
	r := &acpRun{session: sess, exec: exec, stderr: stderr}
	d.setRun(runID, r)
	go d.watch(entry, r)
	if fresh && task != "" {
		if _, err := sess.Prompt(ctx, []acp.ContentBlock{acp.TextBlock(d.s.withCoAuthorInstruction(task))}, false); err != nil {
			slog.Warn("scheduler: send the task to the agent", "run", runID, "error", err)
		}
	}
}

func (d *acpDriver) setRun(run domain.RunID, r *acpRun) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.runs[run] = r
	if r.session != nil {
		if w := d.waiters[run]; w != nil {
			close(w.ch)
			delete(d.waiters, run)
		}
	}
}

func (d *acpDriver) fail(entry *supervised, err error) {
	slog.Warn("scheduler: enhanced session failed", "run", entry.runID, "error", err)
	d.setRun(entry.runID, &acpRun{err: err})
	d.notice(entry.runID, "Enhanced session failed", err.Error())
	go d.report(entry.runID, agentstatus.Report{State: agentstatus.Idle, Reason: acpFailedReason + err.Error()})
}

// notice appends to the item log of a run with no live session. The caller
// holds the run's op lock, so no session opens the log meanwhile.
func (d *acpDriver) notice(run domain.RunID, title, description string) {
	log, err := acphost.OpenLog(d.s.cfg.PTY.ItemLogPath(run))
	if err != nil {
		slog.Warn("scheduler: open item log", "run", run, "error", err)
		return
	}
	if err := log.Append(&acphost.Item{Kind: acphost.KindNotice, Notice: &acphost.Notice{Severity: "error", Title: title, Description: description}}); err != nil {
		slog.Warn("scheduler: record notice", "run", run, "error", err)
	}
	if err := log.Close(); err != nil {
		slog.Warn("scheduler: close item log", "run", run, "error", err)
	}
}

func (d *acpDriver) watch(entry *supervised, r *acpRun) {
	<-r.session.Done()
	d.mu.Lock()
	stopping := r.stopping
	if d.runs[entry.runID] == r {
		r.session = nil
	}
	d.mu.Unlock()
	if stopping {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), acpStopGrace+10*time.Second)
	defer cancel()
	status, err := r.exec.Wait(ctx)
	cause := fmt.Errorf("the agent's ACP server exited with code %d", status.Code)
	if err != nil {
		cause = fmt.Errorf("the agent's ACP server closed its output: %w", err)
		_, _ = r.exec.Stop(ctx, acpStopGrace)
	}
	_ = r.exec.Detach()
	cause = adapterError(cause, r.stderr)
	defer d.lockOp(entry.runID)()
	d.mu.Lock()
	current := d.runs[entry.runID] == r && !r.stopping
	if current {
		r.err = cause
	}
	d.mu.Unlock()
	if !current {
		return
	}
	d.notice(entry.runID, "Enhanced session ended", cause.Error())
	d.report(entry.runID, agentstatus.Report{State: agentstatus.Idle, Reason: acpEndedReason + cause.Error()})
}

func adapterError(err error, stderr *tailBuffer) error {
	if tail := strings.TrimSpace(stderr.String()); tail != "" {
		return fmt.Errorf("%w; stderr: %s", err, tail)
	}
	return err
}

func (d *acpDriver) stopAdapter(ctx context.Context, run domain.RunID) {
	defer d.lockOp(run)()
	d.stopAdapterLocked(ctx, run)
}

func (d *acpDriver) stopAdapterLocked(ctx context.Context, run domain.RunID) {
	d.mu.Lock()
	r := d.runs[run]
	delete(d.runs, run)
	if r != nil {
		r.stopping = true
	}
	d.mu.Unlock()
	if r == nil || r.exec == nil {
		return
	}
	if r.session != nil {
		_ = r.session.Close()
		select {
		case <-r.session.Done():
		case <-time.After(acpStopGrace):
		case <-ctx.Done():
		}
	}
	d.stopExec(r.exec)
	d.s.mu.Lock()
	defer d.s.mu.Unlock()
	if entry := d.s.runs[run]; entry != nil && entry.agentExec != nil && entry.agentExec.ExecID == r.exec.Identity().ExecID {
		entry.agentExec = nil
		if err := d.s.writeSidecar(entry.sidecar()); err != nil {
			slog.Warn("scheduler: persist stopped ACP server", "run", run, "error", err)
		}
	}
}

// shutdown closes every session without stopping its adapter or reporting
// its end, as the end of the server process would; the next server replaces
// the adapters and reports the restored sessions' state.
func (d *acpDriver) shutdown() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.closed = true
	for _, r := range d.runs {
		r.stopping = true
		if r.session != nil {
			_ = r.session.Close()
		}
	}
}

func (d *acpDriver) stopExec(exec runtime.ManagedExec) {
	ctx, cancel := context.WithTimeout(context.Background(), acpStopGrace+10*time.Second)
	defer cancel()
	if _, err := exec.Stop(ctx, acpStopGrace); err != nil {
		slog.Warn("scheduler: stop the agent's ACP server", "exec", exec.Identity().ExecID, "error", err)
	}
	_ = exec.Detach()
}

// stopStaleExec stops an adapter a previous server process started: Docker
// cannot reattach its stdio, so it can only be replaced.
func (d *acpDriver) stopStaleExec(ctx context.Context, entry *supervised) {
	d.s.mu.Lock()
	stale := entry.agentExec
	d.s.mu.Unlock()
	if stale == nil {
		return
	}
	if managed, ok := d.s.cfg.Runtime.(runtime.ManagedExecRuntime); ok {
		if exec, err := managed.RecoverExec(ctx, *stale); err == nil {
			d.stopExec(exec)
		} else {
			slog.Info("scheduler: earlier ACP server is gone", "run", entry.runID, "error", err)
		}
	}
	d.record(entry, func() { entry.agentExec = nil })
}

func (d *acpDriver) record(entry *supervised, change func()) {
	d.s.mu.Lock()
	defer d.s.mu.Unlock()
	change()
	if d.s.runs[entry.runID] != entry {
		return
	}
	if err := d.s.writeSidecar(entry.sidecar()); err != nil {
		slog.Warn("scheduler: persist enhanced session", "run", entry.runID, "error", err)
	}
}

// report hands the session's state to ReportAgentState. A launch marks its
// run running only after the session opened, so a report that arrives first
// waits for that.
func (d *acpDriver) report(run domain.RunID, report agentstatus.Report) {
	d.mu.Lock()
	closed := d.closed
	d.mu.Unlock()
	if closed {
		return
	}
	deadline := time.Now().Add(acpReportWait)
	for {
		err := d.s.ReportAgentState(context.Background(), run, report)
		if err == nil {
			return
		}
		d.s.mu.Lock()
		entry := d.s.runs[run]
		provisioning := entry != nil && entry.status == domain.RunProvisioning
		d.s.mu.Unlock()
		if !provisioning || time.Now().After(deadline) {
			slog.Debug("scheduler: enhanced session report not applied", "run", run, "error", err)
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (d *acpDriver) activity(entry *supervised, verb, target string) {
	d.s.publish(context.Background(), events.Event{
		WorkspaceID: entry.workspaceID,
		RunID:       entry.runID,
		Payload:     events.AgentEventPayload{Kind: events.AgentToolCall, Tool: verb, Detail: truncateRunes(target, 200)},
	})
}

func (d *acpDriver) resumeAfterPause(ctx context.Context, entry *supervised) {
	if d.session(entry.runID) == nil {
		d.connect(ctx, entry, false)
	}
}

func truncateRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

type tailBuffer struct {
	mu  sync.Mutex
	max int
	b   []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.b = append(t.b, p...)
	if over := len(t.b) - t.max; over > 0 {
		t.b = append(t.b[:0], t.b[over:]...)
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.b)
}
