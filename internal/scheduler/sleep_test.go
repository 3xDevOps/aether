package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"github.com/3xDevOps/Aether/internal/acphost/acpmock"
	"github.com/3xDevOps/Aether/internal/control"
	"github.com/3xDevOps/Aether/internal/coordtransport"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/runtime"
)

func TestIdleSince(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
	if _, ok := idleSince(idleFacts{busy: "working", active: []time.Time{base}}); ok {
		t.Fatal("a busy run counted as idle")
	}
	last, ok := idleSince(idleFacts{active: []time.Time{base, base.Add(time.Minute), {}, base.Add(-time.Hour)}})
	if !ok || !last.Equal(base.Add(time.Minute)) {
		t.Fatalf("idleSince = %v, %v; want the latest activity", last, ok)
	}
}

func TestLiveFactsKeepWaitingRunsAwake(t *testing.T) {
	t.Parallel()
	s := &Scheduler{}
	for name, e := range map[string]*supervised{
		"pending input":  {status: domain.RunNeedsAttention, pendingInputs: []domain.RunInputRequest{{ID: "q1"}}},
		"in flight":      {status: domain.RunRunning, awake: 1},
		"working":        {status: domain.RunRunning, activity: domain.ActivityWorking},
		"process wait":   {status: domain.RunNeedsAttention, activity: domain.ActivityWaitingTools},
		"member paused":  {status: domain.RunRunning, paused: true},
		"mode switching": {status: domain.RunRunning, switching: domain.LaunchTUI},
		"ended":          {status: domain.RunCompleted},
	} {
		if s.liveFactsLocked(e).busy == "" {
			t.Errorf("%s: counted as idle", name)
		}
	}
	answered := &supervised{status: domain.RunNeedsAttention, activity: domain.ActivityNeedsYou}
	if busy := s.liveFactsLocked(answered).busy; busy != "" {
		t.Errorf("a run waiting for a person stays awake: %s", busy)
	}
}

func newSleepEnv(t *testing.T) (*testEnv, *acpRuntime) {
	t.Helper()
	return newACPEnv(t, func(cfg *Config) {
		cfg.RunSleepAfter = time.Nanosecond
		cfg.sleepAnyAgent = true
	})
}

// sleepRun runs the sleep sweep until run is asleep.
func (e *testEnv) sleepRun(t *testing.T, run domain.RunID) {
	t.Helper()
	waitFor(t, "the session asleep", func() bool {
		e.sched.sleepIdle(t.Context())
		e.sched.mu.Lock()
		defer e.sched.mu.Unlock()
		entry := e.sched.runs[run]
		return entry != nil && !entry.asleepSince.IsZero() && !entry.sleeping
	})
}

// restartACP closes the scheduler and starts another over the same state,
// store and runtime, as a server restart does.
func (e *testEnv) restartACP(t *testing.T) *Scheduler {
	t.Helper()
	if err := e.sched.Close(); err != nil {
		t.Fatal(err)
	}
	cfg := e.cfg
	cfg.PTY = newFakePTY()
	cfg.PTY.(*fakePTY).logDir = e.pty.logDir
	s2, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s2.Close() })
	startScheduler(t, s2)
	<-s2.RecoveryReady()
	return s2
}

func (e *testEnv) container(t *testing.T, run domain.RunID) *fakeContainer {
	t.Helper()
	c := e.rt.byName(string(run))
	if c == nil {
		t.Fatalf("the container of %s is gone", run)
	}
	return c
}

func containerState(c *fakeContainer) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}

func TestEnhancedRunSleepsAndAPromptWakesIt(t *testing.T) {
	t.Parallel()
	e, rt := newSleepEnv(t)
	run := e.launchACP(t, "say pong")
	waitItems(t, e.sched, run.ID, "the task's turn", turnEnded("end_turn", 1))
	stored, err := e.db.GetRun(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	conversation := stored.HarnessSessionID

	e.sleepRun(t, run.ID)
	c := e.container(t, run.ID)
	if state := containerState(c); state != "stopped" {
		t.Fatalf("an asleep session's container is %s, want stopped", state)
	}
	waitFor(t, "the agent stopped", rt.all()[0].exited)
	if e.sched.Paused(run.ID) {
		t.Fatal("an asleep session reads as paused")
	}
	time.Sleep(50 * time.Millisecond)
	if got, _ := e.db.GetRun(t.Context(), run.ID); got.Status.Terminal() {
		t.Fatalf("the sleep's container exit finished the session: %s", got.Status)
	}

	if _, err := e.sched.Inject(t.Context(), run.ID, e.member.ID, domain.AgentPrompt{Text: "after sleep"}, false, nil); err != nil {
		t.Fatalf("a prompt to a sleeping session: %v", err)
	}
	if e.sched.Sleep(run.ID).AsleepSince != nil || containerState(c) != "running" {
		t.Fatal("the prompt did not wake the session")
	}
	execs := rt.all()
	if methods := execs[len(execs)-1].agent.Methods(); !slices.Contains(methods, acp.AgentMethodSessionResume) || slices.Contains(methods, acp.AgentMethodSessionNew) {
		t.Fatalf("woke with %v, want session/resume", methods)
	}
	waitItems(t, e.sched, run.ID, "a turn after the wake", turnEnded("end_turn", 2))
	if got, _ := e.db.GetRun(t.Context(), run.ID); got.HarnessSessionID != conversation {
		t.Fatalf("woke into conversation %q, want %q", got.HarnessSessionID, conversation)
	}
}

func TestSleepWaitsForAQuestion(t *testing.T) {
	t.Parallel()
	e, _ := newSleepEnv(t)
	run := e.launchACP(t, acpmock.PromptAskPermission)
	waitFor(t, "permission request", func() bool { return len(e.sched.PendingInputs(run.ID)) == 1 })
	e.sched.sleepIdle(t.Context())
	time.Sleep(50 * time.Millisecond)
	if e.sched.Sleep(run.ID).AsleepSince != nil {
		t.Fatal("a session with an open question fell asleep")
	}
}

func TestConcurrentWakesStartTheContainerOnce(t *testing.T) {
	t.Parallel()
	e, _ := newSleepEnv(t)
	run := e.launchACP(t, "say pong")
	waitItems(t, e.sched, run.ID, "the task's turn", turnEnded("end_turn", 1))
	e.sleepRun(t, run.ID)
	var starts int
	var mu sync.Mutex
	e.rt.startHook = func(*fakeContainer) {
		mu.Lock()
		starts++
		mu.Unlock()
	}
	var wg sync.WaitGroup
	errs := make([]error, 3)
	for i := range errs {
		wg.Go(func() {
			if i == 2 {
				errs[i] = e.sched.WakeRun(t.Context(), run.ID, e.member.ID)
				return
			}
			_, errs[i] = e.sched.Inject(t.Context(), run.ID, e.member.ID, domain.AgentPrompt{Text: "wake"}, false, nil)
		})
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("wake %d: %v", i, err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if starts != 1 {
		t.Fatalf("three wakes started the container %d times, want once", starts)
	}
	waitItems(t, e.sched, run.ID, "both prompts answered", turnEnded("end_turn", 3))
}

func TestFailedWakeReturnsItsErrorAndKeepsTheRunAsleep(t *testing.T) {
	t.Parallel()
	e, _ := newSleepEnv(t)
	run := e.launchACP(t, "say pong")
	waitItems(t, e.sched, run.ID, "the task's turn", turnEnded("end_turn", 1))
	e.sleepRun(t, run.ID)
	e.rt.startErr = errors.New("no space left on device")
	_, err := e.sched.Inject(t.Context(), run.ID, e.member.ID, domain.AgentPrompt{Text: "wake"}, false, nil)
	if err == nil || !strings.Contains(err.Error(), "no space left on device") || !errors.Is(err, domain.ErrPromptNotSent) {
		t.Fatalf("Inject = %v, want the start's error and the prompt not sent", err)
	}
	state := e.sched.Sleep(run.ID)
	if state.AsleepSince == nil || !strings.Contains(state.WakeError, "no space left on device") {
		t.Fatalf("sleep state after a failed wake = %+v", state)
	}
	e.rt.startErr = nil
	if _, err := e.sched.Inject(t.Context(), run.ID, e.member.ID, domain.AgentPrompt{Text: "wake"}, false, nil); err != nil {
		t.Fatalf("the next prompt: %v", err)
	}
	if state := e.sched.Sleep(run.ID); state.AsleepSince != nil || state.WakeError != "" {
		t.Fatalf("sleep state after a wake = %+v", state)
	}
}

func TestSleepingRunSurvivesARestart(t *testing.T) {
	t.Parallel()
	e, rt := newSleepEnv(t)
	run := e.launchACP(t, "say pong")
	waitItems(t, e.sched, run.ID, "the task's turn", turnEnded("end_turn", 1))
	e.sleepRun(t, run.ID)
	s2 := e.restartACP(t)
	if s2.Sleep(run.ID).AsleepSince == nil {
		t.Fatal("the restart lost the session's sleep")
	}
	time.Sleep(50 * time.Millisecond)
	if got, _ := e.db.GetRun(t.Context(), run.ID); got.Status.Terminal() {
		t.Fatalf("the restart finished a sleeping session: %s", got.Status)
	}
	c := e.container(t, run.ID)
	if state := containerState(c); state != "stopped" {
		t.Fatalf("container is %s after the restart, want stopped", state)
	}
	before := len(rt.all())
	if _, err := s2.Inject(t.Context(), run.ID, e.member.ID, domain.AgentPrompt{Text: "after restart"}, false, nil); err != nil {
		t.Fatal(err)
	}
	if methods := rt.all()[before].agent.Methods(); !slices.Contains(methods, acp.AgentMethodSessionResume) {
		t.Fatalf("woke with %v, want session/resume", methods)
	}
	waitItems(t, s2, run.ID, "a turn after the restart", turnEnded("end_turn", 2))
}

func TestClosedRunSleepsWithoutLosingItsContainer(t *testing.T) {
	t.Parallel()
	e, rt := newSleepEnv(t)
	run := e.launchACP(t, "say pong")
	waitItems(t, e.sched, run.ID, "the task's turn", turnEnded("end_turn", 1))
	if err := e.sched.CloseRun(t.Context(), run.ID, e.member.ID, domain.RunMerged); err != nil {
		t.Fatal(err)
	}
	e.sleepRun(t, run.ID)
	c := e.container(t, run.ID)
	time.Sleep(50 * time.Millisecond)
	if state := containerState(c); state != "stopped" {
		t.Fatalf("a sleeping closed session's container is %s, want stopped", state)
	}
	if e.rt.byName(string(run.ID)) == nil {
		t.Fatal("the retained container was destroyed when it stopped")
	}
	if err := e.sched.WakeRun(t.Context(), run.ID, e.member.ID); !errors.Is(err, ErrClosedAsleep) {
		t.Fatalf("WakeRun on a closed session = %v, want ErrClosedAsleep", err)
	}

	s2 := e.restartACP(t)
	if e.rt.byName(string(run.ID)) == nil || s2.Sleep(run.ID).AsleepSince == nil {
		t.Fatal("the restart lost the sleeping closed session's container")
	}
	before := len(rt.all())
	if _, err := s2.Relaunch(t.Context(), run.ID, e.member.ID); err != nil {
		t.Fatalf("Relaunch: %v", err)
	}
	if state := containerState(c); state != "running" {
		t.Fatalf("reopened container is %s, want running", state)
	}
	if methods := rt.all()[before].agent.Methods(); !slices.Contains(methods, acp.AgentMethodSessionResume) {
		t.Fatalf("reopened with %v, want session/resume", methods)
	}
	if _, err := s2.Inject(t.Context(), run.ID, e.member.ID, domain.AgentPrompt{Text: "after reopen"}, false, nil); err != nil {
		t.Fatal(err)
	}
	waitItems(t, s2, run.ID, "a turn after reopening", turnEnded("end_turn", 2))
}

func TestKillingASleepingRunFinishesIt(t *testing.T) {
	t.Parallel()
	e, _ := newSleepEnv(t)
	run := e.launchACP(t, "say pong")
	waitItems(t, e.sched, run.ID, "the task's turn", turnEnded("end_turn", 1))
	e.sleepRun(t, run.ID)
	if err := e.sched.Kill(t.Context(), run.ID, e.member.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the killed session finished", func() bool {
		got, err := e.db.GetRun(t.Context(), run.ID)
		return err == nil && got.Status == domain.RunAbandoned
	})
}

// A server that dies after recording a sleep and before stopping the
// container finds the run asleep at restart, and stops the container then.
func TestARestartFinishesAnInterruptedSleep(t *testing.T) {
	t.Parallel()
	e, _ := newSleepEnv(t)
	run := e.launchACP(t, "say pong")
	waitItems(t, e.sched, run.ID, "the task's turn", turnEnded("end_turn", 1))
	e.sched.mu.Lock()
	entry := e.sched.runs[run.ID]
	e.sched.mu.Unlock()
	if err := e.sched.markAsleep(entry); err != nil {
		t.Fatal(err)
	}
	s2 := e.restartACP(t)
	if s2.Sleep(run.ID).AsleepSince == nil {
		t.Fatal("the restart lost the recorded sleep")
	}
	c := e.container(t, run.ID)
	waitFor(t, "the container stopped", func() bool { return containerState(c) == "stopped" })
	time.Sleep(50 * time.Millisecond)
	if got, _ := e.db.GetRun(t.Context(), run.ID); got.Status.Terminal() {
		t.Fatalf("the restart finished a session it found asleep: %s", got.Status)
	}
}

// A kill the server recorded for a sleeping run, and died before finishing,
// finishes at restart.
func TestARestartFinishesAKillRecordedWhileAsleep(t *testing.T) {
	t.Parallel()
	e, _ := newSleepEnv(t)
	run := e.launchACP(t, "say pong")
	waitItems(t, e.sched, run.ID, "the task's turn", turnEnded("end_turn", 1))
	e.sleepRun(t, run.ID)
	e.sched.mu.Lock()
	entry := e.sched.runs[run.ID]
	entry.killRequested, entry.killActor = true, e.member.ID
	err := e.sched.writeSidecar(entry.sidecar())
	e.sched.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	e.restartACP(t)
	waitFor(t, "the killed session finished", func() bool {
		got, err := e.db.GetRun(t.Context(), run.ID)
		return err == nil && got.Status == domain.RunAbandoned
	})
}

// A closed run whose sleep was recorded but whose container was never
// stopped has it stopped at restart.
func TestARestartFinishesAnInterruptedClosedSleep(t *testing.T) {
	t.Parallel()
	e, _ := newSleepEnv(t)
	run := e.launchACP(t, "say pong")
	waitItems(t, e.sched, run.ID, "the task's turn", turnEnded("end_turn", 1))
	if err := e.sched.CloseRun(t.Context(), run.ID, e.member.ID, domain.RunMerged); err != nil {
		t.Fatal(err)
	}
	e.sched.mu.Lock()
	entry := e.sched.runs[run.ID]
	e.sched.mu.Unlock()
	if err := e.sched.markAsleep(entry); err != nil {
		t.Fatal(err)
	}
	s2 := e.restartACP(t)
	c := e.container(t, run.ID)
	waitFor(t, "the container stopped", func() bool { return containerState(c) == "stopped" })
	if s2.Sleep(run.ID).AsleepSince == nil {
		t.Fatal("the restart lost the closed session's sleep")
	}
}

// An Enhanced run whose container was created for Standard, and switched
// since, would rerun its launch command on start, so it stays awake.
func TestASwitchedRunStaysAwake(t *testing.T) {
	t.Parallel()
	e, _ := newSleepEnv(t)
	run := e.launchACP(t, "say pong")
	waitItems(t, e.sched, run.ID, "the task's turn", turnEnded("end_turn", 1))
	c := e.container(t, run.ID)
	c.mu.Lock()
	delete(c.spec.Env, coordtransport.EnhancedEnv)
	c.mu.Unlock()
	e.sched.sleepIdle(t.Context())
	time.Sleep(50 * time.Millisecond)
	if e.sched.Sleep(run.ID).AsleepSince != nil || containerState(c) != "running" {
		t.Fatal("a run whose container was created for Standard fell asleep")
	}
}

// Recovery that adopted a sleeping closed run does not stop the container
// a Relaunch started first.
func TestRecoveryKeepsAReopenedContainerRunning(t *testing.T) {
	t.Parallel()
	e, _ := newSleepEnv(t)
	run := e.launchACP(t, "say pong")
	waitItems(t, e.sched, run.ID, "the task's turn", turnEnded("end_turn", 1))
	if err := e.sched.CloseRun(t.Context(), run.ID, e.member.ID, domain.RunMerged); err != nil {
		t.Fatal(err)
	}
	e.sleepRun(t, run.ID)
	s2 := e.restartACP(t)
	s2.mu.Lock()
	adopted := s2.runs[run.ID]
	s2.mu.Unlock()
	if _, err := s2.Relaunch(t.Context(), run.ID, e.member.ID); err != nil {
		t.Fatalf("Relaunch: %v", err)
	}
	s2.stopRecoveredAsleep(t.Context(), adopted)
	if state := containerState(e.container(t, run.ID)); state != "running" {
		t.Fatalf("reopened container is %s after recovery's stop, want running", state)
	}
}

// stayAwake runs the sleep sweep and fails if run falls asleep.
func (e *testEnv) stayAwake(t *testing.T, run domain.RunID, why string) {
	t.Helper()
	for range 3 {
		e.sched.sleepIdle(t.Context())
		time.Sleep(30 * time.Millisecond)
	}
	if e.sched.Sleep(run).AsleepSince != nil {
		t.Fatalf("a session fell asleep while %s", why)
	}
}

func (e *testEnv) onForegroundProbe(answer func() (string, error)) {
	e.rt.mu.Lock()
	defer e.rt.mu.Unlock()
	e.rt.execHandler = func(_ runtime.ID, argv []string) (int, string, error) {
		if !slices.Contains(argv, foregroundJobScript) {
			return 0, "", nil
		}
		stdout, err := answer()
		return 0, stdout, err
	}
}

func TestACommandInATerminalsForegroundKeepsTheSessionAwake(t *testing.T) {
	t.Parallel()
	e, _ := newSleepEnv(t)
	run := e.launchACP(t, "say pong")
	waitItems(t, e.sched, run.ID, "the task's turn", turnEnded("end_turn", 1))
	e.onForegroundProbe(func() (string, error) { return "vim", nil })
	e.stayAwake(t, run.ID, "vim ran in a shell tab")
	e.onForegroundProbe(func() (string, error) { return "", nil })
	e.sleepRun(t, run.ID)
}

func TestAnAgentShellTaskKeepsTheSessionAwake(t *testing.T) {
	t.Parallel()
	e, _ := newSleepEnv(t)
	run := e.launchACP(t, acpmock.PromptDevServer)
	waitItems(t, e.sched, run.ID, "the task's turn", turnEnded("end_turn", 1))
	e.stayAwake(t, run.ID, "the agent's dev server ran")
}

// A turn the agent starts on its own while the sleep stops the terminals
// keeps the session awake instead of dying with the container.
func TestATurnStartedDuringTheSleepKeepsTheSessionAwake(t *testing.T) {
	t.Parallel()
	e, _ := newSleepEnv(t)
	run := e.launchACP(t, "say pong")
	waitItems(t, e.sched, run.ID, "the task's turn", turnEnded("end_turn", 1))
	e.onForegroundProbe(func() (string, error) {
		sess := e.sched.acp.session(run.ID)
		if _, err := sess.PromptIdle(context.Background(), []acp.ContentBlock{acp.TextBlock(acpmock.PromptWait)}, nil); err != nil {
			return "", err
		}
		return "", nil
	})
	waitFor(t, "the started turn", func() bool {
		e.sched.sleepIdle(t.Context())
		return e.sched.acp.session(run.ID).State().TurnInFlight
	})
	time.Sleep(100 * time.Millisecond)
	if e.sched.Sleep(run.ID).AsleepSince != nil || containerState(e.container(t, run.ID)) != "running" {
		t.Fatal("the sleep stopped a session whose agent had started a turn")
	}
	if !e.sched.acp.session(run.ID).State().TurnInFlight {
		t.Fatal("the agent's turn did not survive the abandoned sleep")
	}
}

// A prompt that arrives while the session is falling asleep waits for the
// sleep, wakes the session and is answered.
func TestAPromptDuringTheSleepWakesTheSession(t *testing.T) {
	t.Parallel()
	e, _ := newSleepEnv(t)
	run := e.launchACP(t, "say pong")
	waitItems(t, e.sched, run.ID, "the task's turn", turnEnded("end_turn", 1))
	probed, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	e.onForegroundProbe(func() (string, error) {
		once.Do(func() { close(probed) })
		<-release
		return "", nil
	})
	waitFor(t, "the sleep's probe", func() bool {
		e.sched.sleepIdle(t.Context())
		select {
		case <-probed:
			return true
		default:
			return false
		}
	})
	injected := make(chan error, 1)
	go func() {
		_, err := e.sched.Inject(context.Background(), run.ID, e.member.ID, domain.AgentPrompt{Text: "during the sleep"}, false, nil)
		injected <- err
	}()
	time.Sleep(50 * time.Millisecond)
	close(release)
	if err := <-injected; err != nil {
		t.Fatalf("a prompt during the sleep: %v", err)
	}
	waitItems(t, e.sched, run.ID, "the prompt's turn", turnEnded("end_turn", 2))
}

func TestWakeTriggers(t *testing.T) {
	t.Parallel()
	member := func(e *testEnv) control.Principal {
		return control.Principal{Kind: control.PrincipalMember, MemberID: e.member.ID}
	}
	for name, trigger := range map[string]func(*testEnv, domain.RunID){
		"an answer": func(e *testEnv, run domain.RunID) { _ = e.sched.ACPAnswer(run, "gone", "allow", nil) },
		"an exec": func(e *testEnv, run domain.RunID) {
			if exec, err := e.sched.StartRunExec(t.Context(), run, member(e), RunExec{Argv: []string{"true"}}, func() error { return nil }); err == nil {
				_ = exec.Detach()
			}
		},
		"a browser operation": func(e *testEnv, run domain.RunID) {
			raw, _ := json.Marshal(protocol.DevBrowserOpenParams{DevRunParams: protocol.DevRunParams{RunID: string(run)}})
			_, _ = e.sched.CallDevelopment(t.Context(), run, member(e), protocol.MethodDevBrowserOpen, raw, func() error { return nil })
		},
		"a model choice": func(e *testEnv, run domain.RunID) { _ = e.sched.ACPSetOption(t.Context(), run, "model", "other") },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			e, _ := newACPEnv(t, func(cfg *Config) {
				cfg.RunSleepAfter = time.Nanosecond
				cfg.sleepAnyAgent = true
				cfg.Control = control.New(control.Config{})
			})
			run := e.launchACP(t, "say pong")
			waitItems(t, e.sched, run.ID, "the task's turn", turnEnded("end_turn", 1))
			e.sleepRun(t, run.ID)
			trigger(e, run.ID)
			if e.sched.Sleep(run.ID).AsleepSince != nil || containerState(e.container(t, run.ID)) != "running" {
				t.Fatalf("%s did not wake the session", name)
			}
		})
	}
}

// Coordination mail whose delivery deadline passes during the wake still
// wakes the session, and the coordination service's retry delivers it.
func TestMailPastItsDeadlineStillWakesTheSession(t *testing.T) {
	t.Parallel()
	e, _ := newSleepEnv(t)
	run := e.launchACP(t, "say pong")
	waitItems(t, e.sched, run.ID, "the task's turn", turnEnded("end_turn", 1))
	e.sleepRun(t, run.ID)
	expired, cancel := context.WithCancel(t.Context())
	cancel()
	if !e.sched.Wakeable(run.ID) {
		t.Fatal("a sleeping session is not wakeable for mail")
	}
	_ = e.sched.Wake(expired, run.ID, "you have mail")
	if e.sched.Sleep(run.ID).AsleepSince != nil {
		t.Fatal("mail past its deadline left the session asleep")
	}
	// The cut-off prompt may still have reached the agent; else the retry
	// delivers it.
	waitFor(t, "the mail delivered", func() bool {
		page, err := e.sched.ACPHistory(run.ID, 0, 0)
		return err == nil && turnEnded("end_turn", 2)(page.Items) || e.sched.Wake(t.Context(), run.ID, "you have mail") == nil
	})
	waitItems(t, e.sched, run.ID, "the mail's turn", turnEnded("end_turn", 2))
}

// A sleeping session whose container was deleted, as docker container prune
// does, ends on its next use instead of staying asleep.
func TestAWakeOfARemovedContainerEndsTheSession(t *testing.T) {
	t.Parallel()
	e, _ := newSleepEnv(t)
	run := e.launchACP(t, "say pong")
	waitItems(t, e.sched, run.ID, "the task's turn", turnEnded("end_turn", 1))
	e.sleepRun(t, run.ID)
	c := e.container(t, run.ID)
	e.rt.mu.Lock()
	delete(e.rt.containers, c.id)
	// Docker's start names no missing container; its inspect does.
	e.rt.startErr = errors.New("docker: no such container")
	e.rt.mu.Unlock()
	_, err := e.sched.Inject(t.Context(), run.ID, e.member.ID, domain.AgentPrompt{Text: "after the prune"}, false, nil)
	if err == nil || !strings.Contains(err.Error(), "removed while it slept") {
		t.Fatalf("a prompt to a pruned session = %v, want the removal named", err)
	}
	waitFor(t, "the session ended", func() bool {
		got, err := e.db.GetRun(t.Context(), run.ID)
		return err == nil && got.Status.Terminal()
	})
}

// A Relaunch that adopts a closed session recovery found asleep, and then
// fails, leaves recovery to stop the container a crash left frozen.
func TestRecoveryStopsAFrozenSleeperAFailedRelaunchAdopted(t *testing.T) {
	t.Parallel()
	e, _ := newSleepEnv(t)
	run := e.launchACP(t, "say pong")
	waitItems(t, e.sched, run.ID, "the task's turn", turnEnded("end_turn", 1))
	if err := e.sched.CloseRun(t.Context(), run.ID, e.member.ID, domain.RunMerged); err != nil {
		t.Fatal(err)
	}
	e.sched.mu.Lock()
	entry := e.sched.runs[run.ID]
	e.sched.mu.Unlock()
	if err := e.sched.markAsleep(entry); err != nil {
		t.Fatal(err)
	}
	if err := e.sched.Close(); err != nil {
		t.Fatal(err)
	}
	c := e.container(t, run.ID)
	if state := containerState(c); state != "paused" {
		t.Fatalf("the closed session's container is %s, want paused", state)
	}
	cfg := e.cfg
	cfg.PTY = newFakePTY()
	s2, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s2.Close() })
	r, err := e.db.GetRun(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	sc, err := s2.readSidecar(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	// The Relaunch adopts the run while recovery inspects its container.
	var once sync.Once
	e.rt.mu.Lock()
	e.rt.inspectHook = func(id runtime.ID) {
		if id != c.id {
			return
		}
		once.Do(func() {
			s2.mu.Lock()
			s2.runs[run.ID] = s2.entryFromSidecar(r, sc)
			s2.mu.Unlock()
		})
	}
	e.rt.mu.Unlock()
	s2.cleanupTerminalSidecars(t.Context())
	if state := containerState(c); state != "stopped" {
		t.Fatalf("the frozen sleeper's container is %s after recovery, want stopped", state)
	}
}

// A Relaunch of a closed session whose sleep a crash interrupted, with the
// container still frozen, stops it and starts it again.
func TestRelaunchFinishesAnInterruptedClosedSleep(t *testing.T) {
	t.Parallel()
	e, rt := newSleepEnv(t)
	run := e.launchACP(t, "say pong")
	waitItems(t, e.sched, run.ID, "the task's turn", turnEnded("end_turn", 1))
	if err := e.sched.CloseRun(t.Context(), run.ID, e.member.ID, domain.RunMerged); err != nil {
		t.Fatal(err)
	}
	e.sched.mu.Lock()
	entry := e.sched.runs[run.ID]
	e.sched.mu.Unlock()
	if err := e.sched.markAsleep(entry); err != nil {
		t.Fatal(err)
	}
	c := e.container(t, run.ID)
	if state := containerState(c); state != "paused" {
		t.Fatalf("the closed session's container is %s, want paused", state)
	}
	before := len(rt.all())
	if _, err := e.sched.Relaunch(t.Context(), run.ID, e.member.ID); err != nil {
		t.Fatalf("Relaunch: %v", err)
	}
	if state := containerState(c); state != "running" {
		t.Fatalf("reopened container is %s, want running", state)
	}
	if methods := rt.all()[before].agent.Methods(); !slices.Contains(methods, acp.AgentMethodSessionResume) {
		t.Fatalf("reopened with %v, want session/resume", methods)
	}
}

// A shell tab still running when a crash interrupted the sleep counts as
// ended once the container is stopped, so it does not block the wake.
func TestAWakeAfterAnInterruptedSleepEndsItsOldTerminals(t *testing.T) {
	t.Parallel()
	e, _ := newSleepEnv(t)
	run := e.launchACP(t, "say pong")
	waitItems(t, e.sched, run.ID, "the task's turn", turnEnded("end_turn", 1))
	c := e.container(t, run.ID)
	lock := e.sched.lockForShell(run.ID)
	lock.Lock()
	set, err := e.sched.loadRunTerminalsLocked(t.Context(), run.ID)
	if err == nil {
		set.ContainerID = c.id
		set.Terminals["web"] = &runTerminal{ID: "web", Incarnation: "before-sleep", State: runtime.ExecState{Running: true},
			Identity: runtime.ExecIdentity{ContainerID: c.id, ExecID: "gone", CreationKey: "terminal-before-sleep", ClaimToken: "claim"}}
		err = e.sched.persistRunTerminalsLocked(run.ID, set)
	}
	lock.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	e.sched.mu.Lock()
	entry := e.sched.runs[run.ID]
	e.sched.mu.Unlock()
	if err := e.sched.markAsleep(entry); err != nil {
		t.Fatal(err)
	}
	s2 := e.restartACP(t)
	waitFor(t, "the container stopped", func() bool { return containerState(c) == "stopped" })
	if _, err := s2.Inject(t.Context(), run.ID, e.member.ID, domain.AgentPrompt{Text: "after the crash"}, false, nil); err != nil {
		t.Fatalf("a prompt after an interrupted sleep: %v", err)
	}
	waitItems(t, s2, run.ID, "a turn after the crash", turnEnded("end_turn", 2))
}
