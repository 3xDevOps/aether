package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/control"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/ptyhost"
	"github.com/3xDevOps/Aether/internal/runtime"
)

// Use the existing scheduler runtime seam and fake process, but independent
// processes for owned execs. Real PTYHost supplies rendering and physical I/O.
type terminalTestRuntime struct {
	*fakeRuntime
	mu         sync.Mutex
	executions map[string]*terminalTestExec
	failBash   bool
}

type terminalTestExec struct {
	identity   runtime.ExecIdentity
	process    *fakeContainer
	attachment *fakeAttachment
}

func newTerminalTestEnv(t *testing.T, mutate func(*Config)) *testEnv {
	t.Helper()
	return newTestEnv(t, func(config *Config) {
		if mutate != nil {
			mutate(config)
		}
		host, err := ptyhost.New(ptyhost.Config{TranscriptDir: t.TempDir(), ReplayBytes: 32, DefaultCols: 80, DefaultRows: 24})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = host.Close() })
		config.PTY = host
		config.Control = control.New(control.Config{})
		config.Runtime = &terminalTestRuntime{fakeRuntime: config.Runtime.(*fakeRuntime), executions: make(map[string]*terminalTestExec)}
	})
}

func (r *terminalTestRuntime) StartExecTTY(_ context.Context, container runtime.ID, spec runtime.ExecSpec) (runtime.ManagedExec, error) {
	r.fakeRuntime.mu.Lock()
	r.execCalls = append(r.execCalls, fakeExecTTYCall{id: container, argv: append([]string(nil), spec.Argv...), workDir: spec.WorkingDir, cols: spec.Cols, rows: spec.Rows})
	r.fakeRuntime.mu.Unlock()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failBash && spec.Argv[0] == "/bin/bash" {
		return nil, &runtime.ExecExitError{Code: 127}
	}
	process := &fakeContainer{id: runtime.ID(spec.CreationKey), state: "running", done: make(chan struct{})}
	reader, writer := io.Pipe()
	attachment := &fakeAttachment{c: process, pr: reader, pw: writer, cols: spec.Cols, rows: spec.Rows}
	process.atts = []*fakeAttachment{attachment}
	execution := &terminalTestExec{identity: runtime.ExecIdentity{ContainerID: container, ExecID: spec.CreationKey,
		CreationKey: spec.CreationKey, ClaimToken: "claim-" + spec.CreationKey}, process: process, attachment: attachment}
	r.executions[spec.CreationKey] = execution
	return execution, nil
}

func (r *terminalTestRuntime) RecoverExec(_ context.Context, identity runtime.ExecIdentity) (runtime.ManagedExec, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	execution := r.executions[identity.ExecID]
	if execution == nil || execution.identity != identity {
		return nil, runtime.ErrExecUnavailable
	}
	return &terminalTestExec{identity: identity, process: execution.process}, nil
}

func (e *terminalTestExec) Identity() runtime.ExecIdentity { return e.identity }
func (e *terminalTestExec) Attachment() runtime.Attachment {
	if e.attachment == nil {
		return nil
	}
	return e.attachment
}
func (e *terminalTestExec) Status(ctx context.Context) (runtime.ExecState, error) {
	if err := ctx.Err(); err != nil {
		return runtime.ExecState{}, err
	}
	e.process.mu.Lock()
	defer e.process.mu.Unlock()
	state := runtime.ExecState{Running: e.process.exit == nil}
	if e.process.exit != nil {
		code := e.process.exit.Code
		state.Exited, state.ExitCode = true, &code
	}
	if e.attachment != nil {
		e.attachment.mu.Lock()
		state.Attached = !e.attachment.closed
		e.attachment.mu.Unlock()
	}
	if !state.Attached {
		state.UnavailableReason = "test execution attachment unavailable"
	}
	return state, nil
}
func (e *terminalTestExec) Wait(ctx context.Context) (runtime.ExitStatus, error) {
	select {
	case <-ctx.Done():
		return runtime.ExitStatus{}, ctx.Err()
	case <-e.process.done:
		e.process.mu.Lock()
		defer e.process.mu.Unlock()
		return *e.process.exit, nil
	}
}
func (e *terminalTestExec) Stop(ctx context.Context, _ time.Duration) (runtime.ExitStatus, error) {
	if err := ctx.Err(); err != nil {
		return runtime.ExitStatus{}, err
	}
	e.process.endProcess(143)
	return e.Wait(ctx)
}
func (e *terminalTestExec) Resize(ctx context.Context, cols, rows uint) error {
	if e.attachment == nil {
		return runtime.ErrExecUnavailable
	}
	return e.attachment.Resize(ctx, cols, rows)
}
func (e *terminalTestExec) Detach() error {
	if e.attachment == nil {
		return nil
	}
	return e.attachment.Close()
}

func terminalTestCall(t *testing.T, scheduler *Scheduler, run domain.RunID, principal control.Principal, method string, params any) (any, error) {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	return scheduler.DevelopmentTerminal(t.Context(), run, principal, method, raw, func() error { return nil })
}

func startTestTerminal(t *testing.T, e *testEnv, run domain.RunID, name string) protocol.DevTerminal {
	t.Helper()
	result, err := terminalTestCall(t, e.sched, run, control.Principal{Kind: control.PrincipalRunAgent, RunID: run},
		protocol.MethodDevTerminalStart, protocol.DevTerminalStartParams{Name: name, Cols: 12, Rows: 3})
	if err != nil {
		t.Fatal(err)
	}
	return result.(protocol.DevTerminalStartResult).Terminal
}

func terminalTestTarget(terminal protocol.DevTerminal) protocol.DevTerminalTarget {
	return protocol.DevTerminalTarget{TerminalID: terminal.TerminalID, Incarnation: terminal.Incarnation}
}

func terminalTestProcess(t *testing.T, e *testEnv, run domain.RunID, terminal protocol.DevTerminal) *terminalTestExec {
	t.Helper()
	lock := e.sched.lockForShell(run)
	lock.Lock()
	defer lock.Unlock()
	set, err := e.sched.loadRunTerminalsLocked(t.Context(), run)
	if err != nil {
		t.Fatal(err)
	}
	return set.Terminals[terminal.TerminalID].Exec.(*terminalTestExec)
}

func terminalTestFence(t *testing.T, e *testEnv, run domain.RunID, terminal protocol.DevTerminal, principal control.Principal, session string, takeover bool) protocol.DevControlFence {
	t.Helper()
	lease, _, err := e.sched.cfg.Control.AcquireSurface(string(run), control.Surface{Kind: control.SurfaceTerminal,
		ID: terminal.TerminalID, Incarnation: terminal.Incarnation}, principal, session, takeover, 0, func() error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	return protocol.DevControlFence{ControlSessionID: lease.SessionID, ControlGeneration: lease.Generation}
}

func TestDevelopmentTerminalSharesDockLimitAndStopsOnlyOwnedProcess(t *testing.T) {
	e := newTerminalTestEnv(t, nil)
	run, primary := e.launchFake(t, "shared terminals")
	principal := control.Principal{Kind: control.PrincipalRunAgent, RunID: run.ID}
	if err := e.sched.EnsureRunShellTab(t.Context(), run.ID, "main", 80, 24); err != nil {
		t.Fatal(err)
	}
	terminal := startTestTerminal(t, e, run.ID, "agent")
	for _, id := range []string{"third", "fourth"} {
		startTestTerminal(t, e, run.ID, id)
	}
	if err := e.sched.EnsureRunShellTab(t.Context(), run.ID, "fifth", 80, 24); !errors.Is(err, ErrRunShellTabLimit) {
		t.Fatalf("fifth = %v", err)
	}
	listed, err := terminalTestCall(t, e.sched, run.ID, principal, protocol.MethodDevTerminalList, protocol.DevTerminalListParams{})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, item := range listed.(protocol.DevTerminalListResult).Terminals {
		names = append(names, item.TerminalID)
	}
	if strings.Join(names, ",") != "agent,fourth,main,third" {
		t.Fatalf("shared list = %v", names)
	}
	fence := terminalTestFence(t, e, run.ID, terminal, principal, "agent-control", false)
	stopped, err := terminalTestCall(t, e.sched, run.ID, principal, protocol.MethodDevTerminalStop,
		protocol.DevTerminalStopParams{DevTerminalTarget: terminalTestTarget(terminal), DevControlFence: fence})
	if err != nil {
		t.Fatal(err)
	}
	result := stopped.(protocol.DevTerminalStopResult)
	if !result.Stopped || result.Terminal.Process.ExitCode == nil || *result.Terminal.Process.ExitCode != 143 {
		t.Fatalf("stop = %+v", result)
	}
	if primary.currentState() != "running" {
		t.Fatalf("terminal stop affected primary: %s", primary.currentState())
	}
	if attachErr := e.sched.EnsureRunShellTab(t.Context(), run.ID, "agent", 80, 24); attachErr == nil {
		t.Fatal("attach implicitly restarted stopped process")
	}
	replacement := startTestTerminal(t, e, run.ID, "agent")
	if replacement.Incarnation == terminal.Incarnation {
		t.Fatal("replacement reused incarnation")
	}
	_, _, err = e.sched.CaptureDevelopmentTerminal(t.Context(), run.ID, terminalTestTarget(terminal))
	if !errors.Is(err, ptyhost.ErrSessionReplaced) {
		t.Fatalf("old target = %v", err)
	}
}

func TestDevelopmentTerminalPhysicalInputTakeoverAndResize(t *testing.T) {
	e := newTerminalTestEnv(t, nil)
	run, primary := e.launchFake(t, "input")
	terminal := startTestTerminal(t, e, run.ID, "input")
	execution := terminalTestProcess(t, e, run.ID, terminal)
	agent := control.Principal{Kind: control.PrincipalRunAgent, RunID: run.ID}
	fence := terminalTestFence(t, e, run.ID, terminal, agent, "agent", false)
	oldAdmission, err := e.sched.DevelopmentTerminalAdmission(t.Context(), run.ID, agent, terminalTestTarget(terminal), fence, func() error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	human := control.Principal{Kind: control.PrincipalMember, MemberID: e.member.ID}
	humanFence := terminalTestFence(t, e, run.ID, terminal, human, "human", true)
	host := e.sched.cfg.PTY.(*ptyhost.Host)
	if writeErr := host.WriteSessionInput(t.Context(), ptyhost.RunShellSession(run.ID, terminal.TerminalID), oldAdmission, []byte("stale")); !errors.Is(writeErr, control.ErrStale) {
		t.Fatalf("stale write = %v", writeErr)
	}
	before, err := host.ObserveSession(ptyhost.RunShellSession(run.ID, terminal.TerminalID))
	if err != nil {
		t.Fatal(err)
	}
	_, err = terminalTestCall(t, e.sched, run.ID, human, protocol.MethodDevTerminalResize, protocol.DevTerminalResizeParams{
		DevTerminalTarget: terminalTestTarget(terminal), DevControlFence: humanFence, Cols: 18, Rows: 5})
	if err != nil {
		t.Fatal(err)
	}
	after, err := host.ObserveSession(ptyhost.RunShellSession(run.ID, terminal.TerminalID))
	if err != nil {
		t.Fatal(err)
	}
	if after.Cols != 18 || after.Rows != 5 || after.Revision == before.Revision || after.Position != before.Position {
		t.Fatalf("resize boundary before=%+v after=%+v", before.Position, after.Position)
	}
	_, err = terminalTestCall(t, e.sched, run.ID, human, protocol.MethodDevTerminalInput, protocol.DevTerminalInputParams{
		DevTerminalTarget: terminalTestTarget(terminal), DevControlFence: humanFence, Kind: "key", Key: "c", Modifiers: []string{"ctrl"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := execution.process.stdinString(); got != "\x03" {
		t.Fatalf("physical bytes = %q", got)
	}
	if primary.stdinString() != "" {
		t.Fatal("development input reached primary harness")
	}
}

func TestDevelopmentTerminalRecoveryDoesNotRerunAndCanStop(t *testing.T) {
	e := newTerminalTestEnv(t, nil)
	run, primary := e.launchFake(t, "recovery")
	terminal := startTestTerminal(t, e, run.ID, "server")
	execution := terminalTestProcess(t, e, run.ID, terminal)
	if err := e.sched.DetachDevelopmentTerminals(t.Context()); err != nil {
		t.Fatal(err)
	}
	if execution.process.currentState() != "running" {
		t.Fatal("shutdown detached by stopping process")
	}
	cfg := e.sched.cfg
	host, err := ptyhost.New(ptyhost.Config{TranscriptDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = host.Close() })
	cfg.PTY = host
	recovered, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = recovered.Close() })
	if recoverErr := recovered.RecoverDevelopmentTerminals(t.Context(), run.ID); recoverErr != nil {
		t.Fatal(recoverErr)
	}
	observation, err := recovered.LookupDevelopmentTerminal(t.Context(), run.ID, terminal.TerminalID)
	if err != nil {
		t.Fatal(err)
	}
	if observation.Incarnation != terminal.Incarnation || observation.Process.State != "unavailable" {
		t.Fatalf("recovery = %+v", observation)
	}
	if reattachErr := recovered.EnsureRunShellTab(t.Context(), run.ID, terminal.TerminalID, 80, 24); reattachErr == nil {
		t.Fatal("recovered PTY pretended to reattach")
	}
	if calls := len(e.rt.execTTYCalls()); calls != 1 {
		t.Fatalf("recovery reran process: %d starts", calls)
	}
	principal := control.Principal{Kind: control.PrincipalRunAgent, RunID: run.ID}
	fence := terminalTestFence(t, e, run.ID, terminal, principal, "recovered", false)
	result, err := terminalTestCall(t, recovered, run.ID, principal, protocol.MethodDevTerminalStop, protocol.DevTerminalStopParams{
		DevTerminalTarget: terminalTestTarget(terminal), DevControlFence: fence})
	if err != nil {
		t.Fatal(err)
	}
	if !result.(protocol.DevTerminalStopResult).Stopped || primary.currentState() != "running" {
		t.Fatalf("recovered stop = %+v primary=%s", result, primary.currentState())
	}
}

func TestDevelopmentTerminalWaitDoesNotTreatEOFAsExit(t *testing.T) {
	e := newTerminalTestEnv(t, nil)
	run, _ := e.launchFake(t, "EOF")
	terminal := startTestTerminal(t, e, run.ID, "stream")
	execution := terminalTestProcess(t, e, run.ID, terminal)
	if err := execution.Detach(); err != nil {
		t.Fatal(err)
	}
	principal := control.Principal{Kind: control.PrincipalRunAgent, RunID: run.ID}
	result, err := terminalTestCall(t, e.sched, run.ID, principal, protocol.MethodDevTerminalWait, protocol.DevTerminalWaitParams{
		DevTerminalTarget: terminalTestTarget(terminal), Exit: true, TimeoutMS: 20})
	if err != nil {
		t.Fatal(err)
	}
	wait := result.(protocol.DevTerminalWaitResult)
	if wait.Matched || !wait.TimedOut || wait.Terminal.Process.ExitCode != nil {
		t.Fatalf("EOF claimed exit: %+v", wait)
	}
	execution.process.exitNow(23)
	result, err = terminalTestCall(t, e.sched, run.ID, principal, protocol.MethodDevTerminalWait, protocol.DevTerminalWaitParams{
		DevTerminalTarget: terminalTestTarget(terminal), Exit: true, TimeoutMS: 20})
	if err != nil {
		t.Fatal(err)
	}
	wait = result.(protocol.DevTerminalWaitResult)
	if !wait.Matched || wait.TimedOut || wait.Terminal.Process.ExitCode == nil || *wait.Terminal.Process.ExitCode != 23 {
		t.Fatalf("exit observation = %+v", wait)
	}
}

func TestDevelopmentTerminalOutputAndScreenAreIndependentOfViewer(t *testing.T) {
	e := newTerminalTestEnv(t, nil)
	run, _ := e.launchFake(t, "observation")
	terminal := startTestTerminal(t, e, run.ID, "screen")
	execution := terminalTestProcess(t, e, run.ID, terminal)
	execution.process.output(strings.Repeat("old ", 20) + "\x1b[2J\x1b[H\x1b[1;3;4:3;38;2;12;34;56m界e\u0301")
	principal := control.Principal{Kind: control.PrincipalRunAgent, RunID: run.ID}
	_, err := terminalTestCall(t, e.sched, run.ID, principal, protocol.MethodDevTerminalWait, protocol.DevTerminalWaitParams{
		DevTerminalTarget: terminalTestTarget(terminal), Contains: "界e\u0301", TimeoutMS: 1000})
	if err != nil {
		t.Fatal(err)
	}
	result, err := terminalTestCall(t, e.sched, run.ID, principal, protocol.MethodDevTerminalOutput, protocol.DevTerminalOutputParams{
		DevTerminalTarget: terminalTestTarget(terminal), MaxBytes: 7, Format: "raw", After: &protocol.DevOutputCursor{Epoch: "lost"}})
	if err != nil {
		t.Fatal(err)
	}
	output := result.(protocol.DevTerminalOutputResult)
	if !output.MissingCursor || !output.Truncated || !output.More || len(output.Data) != 7 || output.Next.Sequence-output.Start.Sequence != 7 {
		t.Fatalf("bounded lost-cursor output = %+v", output)
	}
	result, err = terminalTestCall(t, e.sched, run.ID, principal, protocol.MethodDevTerminalScreen, protocol.DevTerminalScreenParams{
		DevTerminalTarget: terminalTestTarget(terminal), MaxCells: 1})
	if err != nil {
		t.Fatal(err)
	}
	screen := result.(protocol.DevTerminalScreenResult)
	cell := screen.Lines[0].Cells[0]
	if screen.Terminal.Cols != 12 || screen.Terminal.Rows != 3 || cell.Text != "界" || cell.Width != 2 || !cell.Bold || !cell.Italic || cell.UnderlineStyle != 3 || cell.Foreground != "#0c2238" || len(screen.Palette) != 259 {
		t.Fatalf("styled visible cell = %+v", cell)
	}
	if screen.NextRow == nil || *screen.NextRow != 0 || screen.NextColumn != 1 {
		t.Fatalf("wide-cell continuation = %+v", screen)
	}
	_, err = terminalTestCall(t, e.sched, run.ID, principal, protocol.MethodDevTerminalScreen, protocol.DevTerminalScreenParams{
		DevTerminalTarget: terminalTestTarget(terminal), RowOffset: *screen.NextRow, ColumnOffset: screen.NextColumn,
		ExpectedScreenRevision: screen.ScreenRevision + 1, MaxCells: 1})
	if !errors.Is(err, protocol.ErrDevScreenChanged) {
		t.Fatalf("stale page = %v", err)
	}
}

func TestDevelopmentTerminalSemanticInputModes(t *testing.T) {
	cases := []struct {
		name   string
		params protocol.DevTerminalInputParams
		modes  ptyhost.TerminalModes
		want   string
		refuse bool
	}{
		{name: "application cursor", params: protocol.DevTerminalInputParams{Kind: "key", Key: "up"}, modes: ptyhost.TerminalModes{ApplicationCursorKeys: true}, want: "\x1bOA"},
		{name: "modified cursor", params: protocol.DevTerminalInputParams{Kind: "key", Key: "left", Modifiers: []string{"ctrl", "shift"}}, modes: ptyhost.TerminalModes{ApplicationCursorKeys: true}, want: "\x1b[1;6D"},
		{name: "application keypad", params: protocol.DevTerminalInputParams{Kind: "key", Key: "kp1"}, modes: ptyhost.TerminalModes{ApplicationKeypad: true}, want: "\x1bOq"},
		{name: "bracketed paste", params: protocol.DevTerminalInputParams{Kind: "paste", Text: "a\nb"}, modes: ptyhost.TerminalModes{BracketedPaste: true}, want: "\x1b[200~a\rb\x1b[201~"},
		{name: "SGR release", params: protocol.DevTerminalInputParams{Kind: "mouse", Mouse: &protocol.DevTerminalMouse{Action: "release", Button: "left", X: 4, Y: 2}}, modes: ptyhost.TerminalModes{MouseTracking: "VT200", MouseEncoding: "SGR"}, want: "\x1b[<0;5;3m"},
		{name: "disabled mouse", params: protocol.DevTerminalInputParams{Kind: "mouse", Mouse: &protocol.DevTerminalMouse{Action: "press", Button: "left"}}, refuse: true},
		{name: "drag without button", params: protocol.DevTerminalInputParams{Kind: "mouse", Mouse: &protocol.DevTerminalMouse{Action: "move"}}, modes: ptyhost.TerminalModes{MouseTracking: "DRAG", MouseEncoding: "SGR"}, refuse: true},
		{name: "oversized paste framing", params: protocol.DevTerminalInputParams{Kind: "paste", Text: strings.Repeat("x", protocol.MaxDevInputBytes)}, modes: ptyhost.TerminalModes{BracketedPaste: true}, refuse: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := encodeTerminalInput(tc.params, ptyhost.ScreenObservation{Cols: 80, Rows: 24, Modes: tc.modes})
			if tc.refuse {
				if err == nil {
					t.Fatalf("accepted %q", got)
				}
				return
			}
			if err != nil || string(got) != tc.want {
				t.Fatalf("encoded = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestDevelopmentTerminalReservationRollbackDoesNotStopPrimary(t *testing.T) {
	e := newTerminalTestEnv(t, nil)
	run, primary := e.launchFake(t, "rollback")
	reservation, err := e.sched.EnsureRunShellTabReserved(t.Context(), run.ID, "pending", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	terminal, err := e.sched.LookupDevelopmentTerminal(t.Context(), run.ID, "pending")
	if err != nil {
		t.Fatal(err)
	}
	execution := terminalTestProcess(t, e, run.ID, terminal)
	if rollbackErr := reservation.Rollback(t.Context()); rollbackErr != nil {
		t.Fatal(rollbackErr)
	}
	if execution.process.currentState() != "stopped" || primary.currentState() != "running" {
		t.Fatalf("rollback child=%s parent=%s", execution.process.currentState(), primary.currentState())
	}
	_, err = e.sched.LookupDevelopmentTerminal(t.Context(), run.ID, "pending")
	if !errors.Is(err, ptyhost.ErrNoSession) {
		t.Fatalf("rolled-back terminal still exposed: %v", err)
	}
}

func TestDevelopmentTerminalReauthorizationRejectsPhysicalInput(t *testing.T) {
	e := newTerminalTestEnv(t, nil)
	run, _ := e.launchFake(t, "permissions")
	terminal := startTestTerminal(t, e, run.ID, "permissions")
	principal := control.Principal{Kind: control.PrincipalRunAgent, RunID: run.ID}
	fence := terminalTestFence(t, e, run.ID, terminal, principal, "controller", false)
	denied := fmt.Errorf("steer withdrawn")
	admission, err := e.sched.DevelopmentTerminalAdmission(t.Context(), run.ID, principal, terminalTestTarget(terminal), fence, func() error { return denied })
	if err != nil {
		t.Fatal(err)
	}
	err = e.sched.cfg.PTY.(*ptyhost.Host).WriteSessionInput(t.Context(), ptyhost.RunShellSession(run.ID, terminal.TerminalID), admission, []byte("forbidden"))
	if !errors.Is(err, denied) {
		t.Fatalf("reauthorization = %v", err)
	}
	if terminalTestProcess(t, e, run.ID, terminal).process.stdinString() != "" {
		t.Fatal("denied input reached owned process")
	}
}
