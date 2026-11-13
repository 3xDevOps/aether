package vm

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/guest"
	"github.com/3xDevOps/Aether/internal/runtime"
)

func TestPipeExecutionClosesInputAndKeepsStdoutLossless(t *testing.T) {
	h := newHarness(t)
	h.createStarted("run-a", sessionSpec("run-a"))
	m, err := h.engine.startManagedExec(t.Context(), "run-a", runtime.ExecSpec{Argv: []string{"cat"}, CreationKey: "ssh-1"}, false)
	if err != nil {
		t.Fatal(err)
	}
	supervisor := h.guest("run-a").procs[m.Identity().ExecID]
	if !supervisor.spec.Lossless {
		t.Error("a pipe execution without Lossless may drop stdout in the guest")
	}
	stdin := m.Attachment().Stdin()
	if _, ok := stdin.(runtime.ContextWriter); !ok {
		t.Error("a pipe execution's input lost WriteContext")
	}
	if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}
	supervisor.mu.Lock()
	closed := supervisor.stdin
	supervisor.mu.Unlock()
	if !closed {
		t.Error("closing a pipe execution's input did not close the command's stdin")
	}
}

func TestExecStopsWhenItsContextEndsOrStderrFloods(t *testing.T) {
	h := newHarness(t)
	h.createStarted("run-a", sessionSpec("run-a"))
	h.guest("run-a").output = map[string]string{"hang": "hang", "flood": "errflood:"}
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, _, _, err := h.engine.exec(ctx, "run-a", []string{"hang"}, ""); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("exec of a hung command = %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Errorf("exec outlived its context by %s", time.Since(start))
	}
	done := make(chan error, 1)
	go func() {
		_, _, _, err := h.engine.exec(t.Context(), "run-a", []string{"flood"}, "")
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "byte limit") {
			t.Errorf("exec flooding stderr = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a stderr flood left exec waiting on a quiet stdout")
	}
}

func TestWaitEndsWhenTheVMDiesMidWait(t *testing.T) {
	h := newHarness(t)
	h.createStarted("run-a", sessionSpec("run-a"))
	done := make(chan runtime.ExitStatus, 1)
	go func() {
		st, _ := h.engine.wait(context.WithoutCancel(t.Context()), "run-a")
		done <- st
	}()
	time.Sleep(100 * time.Millisecond)
	_ = h.units.Stop(t.Context(), unitName("run-a"))
	select {
	case st := <-done:
		if st.Code != lostExitCode {
			t.Errorf("wait on a VM that died = %+v", st)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("wait kept waiting on a VM that died")
	}
}

func TestAnOldWaitLeavesANewBootRunning(t *testing.T) {
	h := newHarness(t)
	h.createStarted("run-a", sessionSpec("run-a"))
	if err := h.engine.stop(t.Context(), "run-a", time.Second); err != nil {
		t.Fatal(err)
	}
	if err := h.engine.start(t.Context(), "run-a"); err != nil {
		t.Fatal(err)
	}
	h.guest("run-a").procs[guest.MainID].exit(9)
	code, ok := h.engine.waitMain(t.Context(), "run-a", 1)
	if !ok || code != 143 {
		t.Errorf("a wait on boot 1 = %d, %v; want boot 1's exit 143", code, ok)
	}
	if h.units.vmm("run-a") == nil {
		t.Error("a wait on the previous boot powered off the new one")
	}
	if info, _ := h.engine.inspect(t.Context(), "run-a"); info.Exited {
		t.Errorf("the new boot inspects as exited: %+v", info)
	}
}

func TestManagedExecRecoveryKeepsTheExitRecordAndTransientErrors(t *testing.T) {
	h := newHarness(t)
	h.createStarted("run-a", sessionSpec("run-a"))
	m, err := h.engine.startManagedExec(t.Context(), "run-a", runtime.ExecSpec{Argv: []string{"bash"}, CreationKey: "terminal-1", Cols: 80, Rows: 24}, true)
	if err != nil {
		t.Fatal(err)
	}
	identity := m.Identity()
	if st, stopErr := m.Stop(t.Context(), time.Second); stopErr != nil || st.Code != 143 {
		t.Fatalf("stop = %+v, %v", st, stopErr)
	}
	g := h.guest("run-a")
	g.mu.Lock()
	delete(g.procs, identity.ExecID)
	g.mu.Unlock()
	if st, stErr := m.Status(t.Context()); stErr != nil || !st.Exited || *st.ExitCode != 143 {
		t.Errorf("status once the helper forgot the supervisor = %+v, %v", st, stErr)
	}
	if _, err = h.engine.recoverExec(t.Context(), "run-a", identity); err != nil {
		t.Errorf("recovery once the helper forgot the supervisor = %v", err)
	}
	h.units.stateErr = errors.New("systemctl: connection timed out")
	_, err = h.engine.recoverExec(t.Context(), "run-a", identity)
	if !errors.Is(err, runtime.ErrExecUnavailable) || errors.Is(err, runtime.ErrNotFound) {
		t.Errorf("recovery while systemd does not answer = %v, want unavailable, not gone", err)
	}
}

func TestPausedVMsAreNotAskedForStats(t *testing.T) {
	h := newHarness(t)
	h.createStarted("run-a", sessionSpec("run-a"))
	h.guest("run-a").stats = guest.Stats{OOMKills: 3, MemoryLimitBytes: 2 << 30}
	if _, err := h.engine.memoryEvents(t.Context(), "run-a"); err != nil {
		t.Fatal(err)
	}
	if err := h.engine.pause(t.Context(), "run-a"); err != nil {
		t.Fatal(err)
	}
	h.guest("run-a").stats = guest.Stats{OOMKills: 99}
	if events, err := h.engine.memoryEvents(t.Context(), "run-a"); err != nil || events.OOMKills != 3 {
		t.Errorf("memory events of a paused VM = %+v, %v; want the last reading", events, err)
	}
	if stats, err := h.engine.stats(t.Context()); err != nil || len(stats) != 0 {
		t.Errorf("stats of a paused VM = %+v, %v", stats, err)
	}
	if _, err := h.engine.memoryEvents(t.Context(), "run-b"); err == nil || errors.Is(err, fs.ErrNotExist) {
		t.Errorf("memory events of an unknown VM = %v", err)
	}
}

func TestStopKillsAVMWhoseHelperStoppedAnswering(t *testing.T) {
	h := newHarness(t)
	h.createStarted("run-a", sessionSpec("run-a"))
	g := h.guest("run-a")
	g.unresponsive = true
	g.procs[guest.MainID].unresponsive = true
	start := time.Now()
	if err := h.engine.stop(context.Background(), "run-a", 50*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 10*time.Second {
		t.Errorf("stop took %s on an unresponsive guest", time.Since(start))
	}
	if h.units.vmm("run-a") != nil {
		t.Error("the VM still runs after stop")
	}
	if info, _ := h.engine.inspect(t.Context(), "run-a"); !info.Exited || info.ExitCode != killedExitCode {
		t.Errorf("inspect after stop = %+v", info)
	}
}

func TestWaitEndsAVMWhoseGuestRebootedOnItsOwn(t *testing.T) {
	h := newHarness(t)
	h.createStarted("run-a", sessionSpec("run-a"))
	if err := os.WriteFile(h.engine.path("run-a", eventsFile), []byte(`{"source": "vm", "event": "rebooting"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := h.engine.wait(t.Context(), "run-a")
	if err != nil || st.Code != lostExitCode {
		t.Errorf("wait after the guest rebooted = %+v, %v", st, err)
	}
	if h.units.vmm("run-a") != nil {
		t.Error("a guest that rebooted on its own kept running")
	}
}

func TestExecAttachedEndsWithItsContextOnASilentGuest(t *testing.T) {
	h := newHarness(t)
	h.createStarted("run-a", sessionSpec("run-a"))
	h.guest("run-a").output = map[string]string{"/usr/local/bin/aether-internal": "hang"}
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := h.engine.execAttached(ctx, "run-a", runtime.AttachedExec{Argv: []string{"true"}, Stdout: io.Discard, Stderr: io.Discard})
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("execAttached on a silent guest = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("execAttached outlived its context on a silent guest")
	}
}

func TestExecResolvesNamedUsersInTheGuest(t *testing.T) {
	h := newHarness(t)
	h.createStarted("run-a", sessionSpec("run-a"))
	g := h.guest("run-a")
	g.output = map[string]string{"/bin/sh": "70\n71\n"}
	if _, err := h.engine.execAttached(t.Context(), "run-a", runtime.AttachedExec{Argv: []string{"psql"}, User: "postgres", Stdout: io.Discard, Stderr: io.Discard}); err != nil {
		t.Fatal(err)
	}
	var user string
	for _, p := range g.spawned() {
		if p.Argv[len(p.Argv)-1] == "psql" {
			user = p.User
		}
	}
	if user != "70:71" {
		t.Errorf("a sudo -u postgres command ran as %q", user)
	}
	g.output["/bin/sh"] = ""
	if _, err := h.engine.execAttached(t.Context(), "run-a", runtime.AttachedExec{Argv: []string{"psql"}, User: "nobody-here", Stdout: io.Discard, Stderr: io.Discard}); err == nil || !strings.Contains(err.Error(), "unknown in the VM") {
		t.Errorf("an unknown user = %v", err)
	}
}

func TestRestoreKeepsTheGuestWhenTheResumeReplyIsLost(t *testing.T) {
	h := newHarness(t)
	h.createStarted("run-a", sessionSpec("run-a"))
	if err := h.engine.save(t.Context(), "run-a"); err != nil {
		t.Fatal(err)
	}
	h.units.failResume = true
	if err := h.engine.restore(t.Context(), "run-a"); err == nil {
		t.Fatal("restore with a lost resume reply succeeded")
	}
	if h.units.vmm("run-a") == nil {
		t.Error("a restore whose resume reply was lost killed the only copy of the guest")
	}
}

func TestStartEndsWhenTheVMDiesDuringSetup(t *testing.T) {
	h := newHarness(t)
	if _, err := h.engine.create(t.Context(), sessionSpec("run-a")); err != nil {
		t.Fatal(err)
	}
	h.guest("run-a").bootHangs = true
	done := make(chan error, 1)
	go func() { done <- h.engine.start(context.Background(), "run-a") }()
	time.Sleep(200 * time.Millisecond)
	_ = h.units.Stop(t.Context(), unitName("run-a"))
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "stopped while booting") {
			t.Errorf("start after the VM died during setup = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("start kept waiting on a VM that died during setup")
	}
}
