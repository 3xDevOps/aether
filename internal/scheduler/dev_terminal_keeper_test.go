package scheduler

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/acpkeeper"
	"github.com/3xDevOps/Aether/internal/control"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/ptyhost"
	"github.com/3xDevOps/Aether/internal/runtime"
)

// testTermKeeper stands in for a terminal keeper process: term relays a
// PTY whose program the test plays.
type testTermKeeper struct {
	mu    sync.Mutex
	term  *acpkeeper.Term
	input bytes.Buffer
	sizes [][2]uint16
	exec  *terminalTestExec
}

func (k *testTermKeeper) Write(p []byte) (int, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.input.Write(p)
}

func (k *testTermKeeper) resize(cols, rows uint16) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.sizes = append(k.sizes, [2]uint16{cols, rows})
	return nil
}

func (k *testTermKeeper) output(t *testing.T, s string) {
	t.Helper()
	if _, err := k.term.Write([]byte(s)); err != nil {
		t.Fatal(err)
	}
}

func (k *testTermKeeper) eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		k.mu.Lock()
		done := ok()
		k.mu.Unlock()
		if done {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the terminal never saw %s: %q", what, k.input.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// keeperStart is the attachment of a keeper exec until it is ready: the
// keeper reads its token, then says ready.
type keeperStart struct {
	k      *testTermKeeper
	stdout *io.PipeReader
	ready  *io.PipeWriter
}

func (a *keeperStart) Stdin() io.WriteCloser { return a }
func (a *keeperStart) Write(p []byte) (int, error) {
	if a.k != nil {
		a.k.term = acpkeeper.NewTerm(acpkeeper.TermConfig{Token: strings.TrimSpace(string(p)), Input: a.k, Resize: a.k.resize, Cols: 12, Rows: 3})
		go func() { _, _ = io.WriteString(a.ready, "ready\n") }()
	}
	return len(p), nil
}
func (a *keeperStart) Stdout() io.Reader { return a.stdout }
func (a *keeperStart) Stderr() io.Reader { return bytes.NewReader(nil) }
func (a *keeperStart) Resize(context.Context, uint, uint) error {
	return errors.New("a keeper exec has no terminal")
}
func (a *keeperStart) Close() error { return a.ready.Close() }

// connAttachment is a bridge exec's stdio: a connection to the keeper.
type connAttachment struct{ net.Conn }

func (c connAttachment) Stdin() io.WriteCloser { return c.Conn }
func (c connAttachment) Stdout() io.Reader     { return c.Conn }
func (connAttachment) Stderr() io.Reader       { return bytes.NewReader(nil) }
func (connAttachment) Resize(context.Context, uint, uint) error {
	return errors.New("a bridge has no terminal")
}

func (r *terminalTestRuntime) startKeeperExec(container runtime.ID, spec runtime.ExecSpec) (runtime.ManagedExec, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	process := &fakeContainer{id: runtime.ID(spec.CreationKey), state: "running", done: make(chan struct{})}
	execution := &terminalTestExec{identity: runtime.ExecIdentity{ContainerID: container, ExecID: spec.CreationKey,
		CreationKey: spec.CreationKey, ClaimToken: "claim-" + spec.CreationKey}, process: process}
	r.executions[spec.CreationKey] = execution
	socket := spec.Argv[3]
	switch {
	case spec.Argv[2] == "term" && r.keepers:
		k := &testTermKeeper{exec: execution}
		stdout, ready := io.Pipe()
		execution.start = &keeperStart{k: k, stdout: stdout, ready: ready}
		if r.keeperTerms == nil {
			r.keeperTerms = make(map[string]*testTermKeeper)
		}
		r.keeperTerms[socket] = k
		go func() {
			<-process.done
			k.term.Exited(process.exit.Code)
		}()
	case spec.Argv[2] == "attach" && r.keeperTerms[socket] != nil:
		client, server := net.Pipe()
		go r.keeperTerms[socket].term.ServeConn(server)
		execution.start = connAttachment{client}
		go func() {
			<-process.done
			_ = client.Close()
		}()
	default:
		// A staged binary without a terminal keeper prints its usage and
		// exits.
		stdout, w := io.Pipe()
		_ = w.Close()
		execution.start = &keeperStart{stdout: stdout, ready: w}
		process.endProcess(125)
	}
	return execution, nil
}

func newKeptTerminalEnv(t *testing.T) *testEnv {
	t.Helper()
	e := newTerminalTestEnv(t, nil)
	e.sched.cfg.Runtime.(*terminalTestRuntime).keepers = true
	return e
}

// restartScheduler shuts e's scheduler down as a server process does and
// returns a new one over the same state and runtime.
func restartScheduler(t *testing.T, e *testEnv) *Scheduler {
	t.Helper()
	if err := e.sched.DetachDevelopmentTerminals(t.Context()); err != nil {
		t.Fatal(err)
	}
	cfg := e.sched.cfg
	host, err := ptyhost.New(ptyhost.Config{TranscriptDir: t.TempDir(), ReplayBytes: 1 << 10})
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
	return recovered
}

func TestDevelopmentTerminalSurvivesSchedulerRestart(t *testing.T) {
	e := newKeptTerminalEnv(t)
	rt := e.sched.cfg.Runtime.(*terminalTestRuntime)
	run, _ := e.launchFake(t, "kept")
	terminal := startTestTerminal(t, e, run.ID, "server")
	target := terminalTestTarget(terminal)
	if terminal.Process.State != "running" {
		t.Fatalf("started terminal = %+v", terminal)
	}
	var keeper *testTermKeeper
	for _, k := range rt.keeperTerms {
		keeper = k
	}
	principal := control.Principal{Kind: control.PrincipalRunAgent, RunID: run.ID}
	// Each server answers the cursor position queries it was sent once.
	replies := func() int { return strings.Count(keeper.input.String(), "R") }
	keeper.output(t, "\x1b[6n$ make\r\n")
	keeper.eventually(t, "the first reply", func() bool { return replies() == 1 })
	result, err := terminalTestCall(t, e.sched, run.ID, principal, protocol.MethodDevTerminalWait, protocol.DevTerminalWaitParams{
		DevTerminalTarget: target, Contains: "$ make", TimeoutMS: 2000})
	if err != nil || !result.(protocol.DevTerminalWaitResult).Matched {
		t.Fatalf("wait before restart = %+v, %v", result, err)
	}
	cursor := result.(protocol.DevTerminalWaitResult).Position

	recovered := restartScheduler(t, e)
	keeper.output(t, "built while the server was down\x1b[6n\r\n")
	if err = recovered.RecoverDevelopmentTerminals(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	keeper.eventually(t, "the reply to the query while no server was attached", func() bool { return replies() == 2 })
	listed, err := terminalTestCall(t, recovered, run.ID, principal, protocol.MethodDevTerminalList, protocol.DevTerminalListParams{})
	if err != nil {
		t.Fatal(err)
	}
	if got := listed.(protocol.DevTerminalListResult).Terminals; len(got) != 1 || got[0].Process.State != "running" || got[0].Incarnation != terminal.Incarnation {
		t.Fatalf("terminals after restart = %+v", got)
	}

	// The cursor from before the restart continues: no byte is lost or
	// repeated.
	output, err := terminalTestCall(t, recovered, run.ID, principal, protocol.MethodDevTerminalOutput, protocol.DevTerminalOutputParams{
		DevTerminalTarget: target, After: &cursor})
	if err != nil {
		t.Fatal(err)
	}
	if got := output.(protocol.DevTerminalOutputResult); got.MissingCursor || got.Truncated || got.Text != "built while the server was down\x1b[6n\r\n" {
		t.Fatalf("output after the pre-restart cursor = %+v", got)
	}

	fence := terminalTestFence(t, e, run.ID, terminal, principal, "after-restart", false)
	if _, err = terminalTestCall(t, recovered, run.ID, principal, protocol.MethodDevTerminalInput, protocol.DevTerminalInputParams{
		DevTerminalTarget: target, DevControlFence: fence, Kind: "text", Text: "ls\r"}); err != nil {
		t.Fatalf("input after restart: %v", err)
	}
	keeper.eventually(t, "the input", func() bool { return strings.HasSuffix(keeper.input.String(), "ls\r") })
	if _, err = terminalTestCall(t, recovered, run.ID, principal, protocol.MethodDevTerminalResize, protocol.DevTerminalResizeParams{
		DevTerminalTarget: target, DevControlFence: fence, Cols: 40, Rows: 10}); err != nil {
		t.Fatalf("resize after restart: %v", err)
	}
	keeper.eventually(t, "the resize", func() bool { return len(keeper.sizes) > 0 && keeper.sizes[len(keeper.sizes)-1] == [2]uint16{40, 10} })
	keeper.output(t, "a.txt\r\n")
	if replies() != 2 {
		t.Fatalf("terminal replies %q; want one per query", keeper.input.String())
	}
	if result, err = terminalTestCall(t, recovered, run.ID, principal, protocol.MethodDevTerminalWait, protocol.DevTerminalWaitParams{
		DevTerminalTarget: target, Contains: "a.txt", TimeoutMS: 2000}); err != nil || !result.(protocol.DevTerminalWaitResult).Matched {
		t.Fatalf("wait after restart = %+v, %v", result, err)
	}

	stopped, err := terminalTestCall(t, recovered, run.ID, principal, protocol.MethodDevTerminalStop, protocol.DevTerminalStopParams{
		DevTerminalTarget: target, DevControlFence: fence})
	if err != nil || !stopped.(protocol.DevTerminalStopResult).Stopped {
		t.Fatalf("stop after restart = %+v, %v", stopped, err)
	}
	if keeper.exec.process.currentState() != "stopped" {
		t.Fatal("stop left the keeper running")
	}
}

func TestDevelopmentTerminalWaitReportsExitAfterRestart(t *testing.T) {
	e := newKeptTerminalEnv(t)
	rt := e.sched.cfg.Runtime.(*terminalTestRuntime)
	run, _ := e.launchFake(t, "kept-exit")
	terminal := startTestTerminal(t, e, run.ID, "job")
	var keeper *testTermKeeper
	for _, k := range rt.keeperTerms {
		keeper = k
	}
	recovered := restartScheduler(t, e)
	if err := recovered.RecoverDevelopmentTerminals(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	keeper.output(t, "done\r\n")
	keeper.exec.process.exitNow(4)
	principal := control.Principal{Kind: control.PrincipalRunAgent, RunID: run.ID}
	result, err := terminalTestCall(t, recovered, run.ID, principal, protocol.MethodDevTerminalWait, protocol.DevTerminalWaitParams{
		DevTerminalTarget: terminalTestTarget(terminal), Exit: true, TimeoutMS: 2000})
	if err != nil {
		t.Fatal(err)
	}
	wait := result.(protocol.DevTerminalWaitResult)
	if !wait.Matched || wait.Terminal.Process.ExitCode == nil || *wait.Terminal.Process.ExitCode != 4 {
		t.Fatalf("exit after restart = %+v", wait)
	}
}

func TestDevelopmentTerminalWithAGoneExecDoesNotBlockCloseOrStop(t *testing.T) {
	e := newTerminalTestEnv(t, nil)
	rt := e.sched.cfg.Runtime.(*terminalTestRuntime)
	run, _ := e.launchFake(t, "gone")
	closing := startTestTerminal(t, e, run.ID, "closing")
	stopping := startTestTerminal(t, e, run.ID, "stopping")
	rt.mu.Lock()
	rt.gone = map[string]bool{}
	for id := range rt.executions {
		rt.gone[id] = true
	}
	rt.mu.Unlock()
	recovered := restartScheduler(t, e)

	// The run agent clears a terminal whose exec is gone without a control
	// lease: nothing is left to control.
	principal := control.Principal{Kind: control.PrincipalRunAgent, RunID: run.ID}
	result, err := terminalTestCall(t, recovered, run.ID, principal, protocol.MethodDevTerminalStop, protocol.DevTerminalStopParams{
		DevTerminalTarget: terminalTestTarget(stopping)})
	if err != nil {
		t.Fatalf("stop a gone terminal: %v", err)
	}
	if stop := result.(protocol.DevTerminalStopResult); !stop.Stopped || stop.Terminal.Process.State != "exited" || !strings.Contains(stop.Terminal.Process.Reason, "No such exec instance") {
		t.Fatalf("stop a gone terminal = %+v", stop)
	}
	list := func() map[string]protocol.DevProcessState {
		t.Helper()
		listed, err := terminalTestCall(t, recovered, run.ID, principal, protocol.MethodDevTerminalList, protocol.DevTerminalListParams{})
		if err != nil {
			t.Fatal(err)
		}
		states := map[string]protocol.DevProcessState{}
		for _, terminal := range listed.(protocol.DevTerminalListResult).Terminals {
			states[terminal.TerminalID] = terminal.Process
		}
		return states
	}
	if states := list(); len(states) != 1 || states[closing.TerminalID].State != "exited" {
		t.Fatalf("terminals after clearing = %+v", states)
	}

	// Closing the run, as a successful report does, stops every terminal
	// first.
	if err := recovered.StopDevelopmentTerminals(t.Context(), run.ID); err != nil {
		t.Fatalf("close with a gone terminal: %v", err)
	}
	if states := list(); states[closing.TerminalID].State != "exited" || !strings.Contains(states[closing.TerminalID].Reason, "No such exec instance") {
		t.Fatalf("closed terminals = %+v", states)
	}
}
