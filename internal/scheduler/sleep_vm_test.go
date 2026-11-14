package scheduler

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"github.com/3xDevOps/Aether/internal/acphost"
	"github.com/3xDevOps/Aether/internal/acphost/acpmock"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/runtime"
)

// saveRuntime runs every container as a session VM: a save ends it as a
// stop does but keeps its execs, the agent's keeper among them, and a
// restore starts it again with them, paused if it was saved paused.
type saveRuntime struct {
	*acpRuntime

	saveMu sync.Mutex
	// saved holds the state each saved container was saved from.
	saved   map[runtime.ID]string
	saveErr error
	// saving, when set, receives each save as it starts, which then waits
	// for release.
	saving     chan struct{}
	release    chan struct{}
	restoreErr error
	// restoreLate fails a restore only after it deleted the saved state
	// and started the guest, as a failed clock set does.
	restoreLate bool
	restores    int
}

func (r *saveRuntime) Saves(runtime.ID) bool { return true }

func (r *saveRuntime) Save(_ context.Context, id runtime.ID) error {
	r.saveMu.Lock()
	saving, release, saveErr := r.saving, r.release, r.saveErr
	r.saveMu.Unlock()
	if saving != nil {
		saving <- struct{}{}
		<-release
	}
	if saveErr != nil {
		return saveErr
	}
	c, err := r.get(id)
	if err != nil {
		return err
	}
	state := c.currentState()
	if state != "running" && state != "paused" {
		return errors.New("save from state " + state)
	}
	c.endProcess(137)
	r.saveMu.Lock()
	r.saved[id] = state
	r.saveMu.Unlock()
	return nil
}

func (r *saveRuntime) Restore(ctx context.Context, id runtime.ID) error {
	r.saveMu.Lock()
	r.restores++
	err, late, saved := r.restoreErr, r.restoreLate, r.saved[id]
	if err == nil || late {
		delete(r.saved, id)
	}
	r.saveMu.Unlock()
	switch {
	case err != nil && late:
		return errors.Join(err, r.restart(ctx, id, saved))
	case err != nil:
		return err
	case saved == "":
		return errors.New("it has no saved state")
	}
	return r.restart(ctx, id, saved)
}

func (r *saveRuntime) restart(ctx context.Context, id runtime.ID, state string) error {
	if err := r.Start(ctx, id); err != nil || state != "paused" {
		return err
	}
	return r.Pause(ctx, id)
}

func (r *saveRuntime) Stop(ctx context.Context, id runtime.ID, grace time.Duration) error {
	r.saveMu.Lock()
	delete(r.saved, id)
	r.saveMu.Unlock()
	return r.acpRuntime.Stop(ctx, id, grace)
}

func (r *saveRuntime) isSaved(id runtime.ID) bool {
	return r.savedFrom(id) != ""
}

func (r *saveRuntime) savedFrom(id runtime.ID) string {
	r.saveMu.Lock()
	defer r.saveMu.Unlock()
	return r.saved[id]
}

// newSaveEnv's agents restore their conversation in a new process when
// resumable.
func newSaveEnv(t *testing.T, resumable bool) (*testEnv, *acpRuntime, *saveRuntime) {
	t.Helper()
	var sr *saveRuntime
	e, rt := newACPEnv(t, func(cfg *Config) {
		cfg.VMSleepAfter = time.Nanosecond
		cfg.sleepAnyAgent = resumable
		sr = &saveRuntime{acpRuntime: cfg.Runtime.(*acpRuntime), saved: map[runtime.ID]string{}}
		cfg.Runtime = sr
	})
	e.onGuestCPU("0")
	return e, rt, sr
}

// onGuestCPU answers the sleep's CPU probe with percent.
func (e *testEnv) onGuestCPU(percent string) {
	e.rt.mu.Lock()
	defer e.rt.mu.Unlock()
	e.rt.execHandler = func(_ runtime.ID, argv []string) (int, string, error) {
		if slices.Contains(argv, guestCPUScript) {
			return 0, percent + "\n", nil
		}
		return 0, "", nil
	}
}

func (e *testEnv) cid(t *testing.T, run domain.RunID) runtime.ID {
	t.Helper()
	return e.container(t, run).id
}

// adopted reports that the launch's agent is the only one, and opened no
// conversation with session/resume or session/load since.
func adopted(rt *acpRuntime) bool {
	execs := rt.all()
	if len(execs) != 1 {
		return false
	}
	methods := execs[0].agent.Methods()
	return !slices.Contains(methods, acp.AgentMethodSessionResume) && !slices.Contains(methods, acp.AgentMethodSessionLoad)
}

func noticed(title, text string) func([]acphost.Item) bool {
	return func(items []acphost.Item) bool {
		return slices.ContainsFunc(items, func(it acphost.Item) bool {
			return it.Kind == acphost.KindNotice && it.Notice.Title == title && strings.Contains(it.Notice.Description, text)
		})
	}
}

func TestAVMSleepsBySavingAndWakesWithItsAgentRunning(t *testing.T) {
	t.Parallel()
	e, rt, sr := newSaveEnv(t, false)
	run := e.launchACP(t, "say pong")
	waitItems(t, e.sched, run.ID, "the task's turn", turnEnded("end_turn", 1))

	e.sleepRun(t, run.ID)
	if !sr.isSaved(e.cid(t, run.ID)) || !e.sched.Sleep(run.ID).Saved {
		t.Fatalf("an asleep VM was not saved: %+v", e.sched.Sleep(run.ID))
	}
	if rt.all()[0].exited() {
		t.Fatal("the sleep stopped the agent a save keeps")
	}
	time.Sleep(50 * time.Millisecond)
	if got, _ := e.db.GetRun(t.Context(), run.ID); got.Status.Terminal() {
		t.Fatalf("the save's exit finished the session: %s", got.Status)
	}

	if _, err := e.sched.Inject(t.Context(), run.ID, e.member.ID, domain.AgentPrompt{Text: "after sleep"}, false, nil); err != nil {
		t.Fatalf("a prompt to a saved session: %v", err)
	}
	if state := e.sched.Sleep(run.ID); state.AsleepSince != nil || sr.restores != 1 {
		t.Fatalf("the prompt did not restore the session: %+v, %d restores", state, sr.restores)
	}
	if !adopted(rt) {
		t.Fatal("the wake started another agent or reopened the conversation instead of reattaching")
	}
	waitItems(t, e.sched, run.ID, "a turn after the wake", turnEnded("end_turn", 2))
	waitItems(t, e.sched, run.ID, "the wake's notice", noticed("Woke from sleep", "carry on where they were"))
}

// Work that still runs keeps a VM awake as it keeps a container: an agent's
// background task, and in a VM also a guest that uses the CPU.
func TestRunningWorkKeepsAVMAwake(t *testing.T) {
	t.Parallel()
	e, _, sr := newSaveEnv(t, false)
	task := e.launchClaudeACP(t, acpmock.PromptDevServer)
	waitItems(t, e.sched, task.ID, "the task's turn", turnEnded("end_turn", 1))
	e.stayAwake(t, task.ID, "the agent's dev server ran")

	run := e.launchACP(t, "say pong")
	waitItems(t, e.sched, run.ID, "the turn", turnEnded("end_turn", 1))
	e.onGuestCPU("60")
	e.stayAwake(t, run.ID, "the guest was busy")
	e.onGuestCPU("1")
	e.sleepRun(t, run.ID)
	if !sr.isSaved(e.cid(t, run.ID)) {
		t.Fatal("the VM was not saved once idle")
	}
}

// A save that fails, as one of a VM that still shares a folder, leaves the
// session running with the agent it had.
func TestAFailedSaveLeavesTheSessionAwake(t *testing.T) {
	t.Parallel()
	e, rt, sr := newSaveEnv(t, false)
	run := e.launchACP(t, "say pong")
	waitItems(t, e.sched, run.ID, "the task's turn", turnEnded("end_turn", 1))
	sr.saveMu.Lock()
	sr.saveErr = errors.New("cannot save VM: it shares /workspace over virtio-fs")
	sr.saveMu.Unlock()
	e.sched.mu.Lock()
	entry := e.sched.runs[run.ID]
	e.sched.mu.Unlock()
	var err error
	waitFor(t, "a save tried", func() bool {
		err = e.sched.sleepLive(t.Context(), entry)
		return !errors.Is(err, errStayAwake)
	})
	if err == nil || !strings.Contains(err.Error(), "virtio-fs") {
		t.Fatalf("sleepLive = %v, want the save's error", err)
	}
	if e.sched.Sleep(run.ID).AsleepSince != nil || containerState(e.container(t, run.ID)) != "running" {
		t.Fatal("a failed save left the session asleep or stopped")
	}
	if _, err := e.sched.Inject(t.Context(), run.ID, e.member.ID, domain.AgentPrompt{Text: "still here"}, false, nil); err != nil {
		t.Fatal(err)
	}
	if !adopted(rt) {
		t.Fatal("the agent was restarted after the failed save")
	}
	waitItems(t, e.sched, run.ID, "a turn after the failed save", turnEnded("end_turn", 2))
}

// A server that shuts down while a VM is being saved lets the save finish
// instead of stopping the VM.
func TestAShutdownDuringASaveDoesNotStopTheVM(t *testing.T) {
	t.Parallel()
	e, _, sr := newSaveEnv(t, false)
	run := e.launchACP(t, "say pong")
	waitItems(t, e.sched, run.ID, "the task's turn", turnEnded("end_turn", 1))
	sr.saveMu.Lock()
	sr.saving, sr.release = make(chan struct{}), make(chan struct{})
	sr.saveErr = errors.New("the save was cut short")
	sr.saveMu.Unlock()
	waitFor(t, "a save started", func() bool {
		e.sched.sleepIdle(t.Context())
		select {
		case <-sr.saving:
			return true
		case <-time.After(20 * time.Millisecond):
			return false
		}
	})
	closed := make(chan error, 1)
	go func() { closed <- e.sched.Close() }()
	time.Sleep(50 * time.Millisecond)
	close(sr.release)
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	if state := containerState(e.container(t, run.ID)); state != "running" {
		t.Fatalf("container is %s after a save the shutdown cut short, want running for the next server to save", state)
	}
	sc, err := e.sched.readSidecar(run.ID)
	if err != nil || sc.AsleepSince == nil {
		t.Fatalf("sidecar after the cut save = %+v, %v; want it asleep", sc, err)
	}
}

// Killing or closing a sleeping VM wakes it first, so its processes end and
// its work is committed as in a running session.
func TestKillingOrClosingASleepingVMWakesItFirst(t *testing.T) {
	t.Parallel()
	for _, how := range []string{"kill", "close"} {
		t.Run(how, func(t *testing.T) {
			t.Parallel()
			e, _, sr := newSaveEnv(t, false)
			run := e.launchACP(t, "say pong")
			waitItems(t, e.sched, run.ID, "the task's turn", turnEnded("end_turn", 1))
			e.sleepRun(t, run.ID)
			c := e.container(t, run.ID)
			var mu sync.Mutex
			var committedUp []bool
			e.git.commitHook = func(_ domain.RunID, _ string) {
				mu.Lock()
				defer mu.Unlock()
				committedUp = append(committedUp, containerState(c) != "stopped" && !sr.isSaved(c.id))
			}
			var err error
			if how == "kill" {
				err = e.sched.Kill(t.Context(), run.ID, e.member.ID)
			} else {
				err = e.sched.CloseRun(t.Context(), run.ID, e.member.ID, domain.RunAbandoned)
			}
			if err != nil {
				t.Fatalf("%s of a sleeping VM: %v", how, err)
			}
			if sr.restores != 1 {
				t.Fatalf("%s restored %d times, want once", how, sr.restores)
			}
			if how == "close" {
				mu.Lock()
				up := slices.Clone(committedUp)
				mu.Unlock()
				if len(up) == 0 || !up[0] || e.git.published[run.ID] == 0 {
					t.Fatalf("the close committed %v (true: the VM ran) and published %d times; want a commit made in the running VM and a published branch", up, e.git.published[run.ID])
				}
			}
			waitFor(t, "the session ended", func() bool {
				got, _ := e.db.GetRun(t.Context(), run.ID)
				return got.Status.Terminal()
			})
		})
	}
}

// A kill whose wake fails still ends the run.
func TestKillingASleepingVMThatCannotWakeEndsIt(t *testing.T) {
	t.Parallel()
	e, _, sr := newSaveEnv(t, false)
	run := e.launchACP(t, "say pong")
	waitItems(t, e.sched, run.ID, "the task's turn", turnEnded("end_turn", 1))
	e.sleepRun(t, run.ID)
	sr.saveMu.Lock()
	sr.restoreErr = errors.New("restore: monitor exited")
	sr.saveMu.Unlock()
	if err := e.sched.Kill(t.Context(), run.ID, e.member.ID); err != nil {
		t.Fatalf("Kill of a VM that cannot wake: %v", err)
	}
	waitFor(t, "the session ended", func() bool {
		got, _ := e.db.GetRun(t.Context(), run.ID)
		return got.Status.Terminal()
	})
}

// A sleeping Standard session VM takes no mail on its own, so mail wakes it.
func TestMailWakesASleepingStandardVM(t *testing.T) {
	t.Parallel()
	e, _, sr := newSaveEnv(t, false)
	run, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "say pong", "fake", domain.LaunchTUI)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the container running", func() bool { return containerState(e.container(t, run.ID)) == "running" })
	e.sched.mu.Lock()
	e.sched.runs[run.ID].activity = domain.ActivityAnswered
	e.sched.mu.Unlock()
	e.sleepRun(t, run.ID)
	if !sr.isSaved(e.cid(t, run.ID)) {
		t.Fatal("a Standard VM was not saved")
	}
	if !e.sched.Wakeable(run.ID) {
		t.Fatal("a sleeping Standard VM is not wakeable for mail")
	}
	if err := e.sched.Wake(t.Context(), run.ID, "mail"); err != nil {
		t.Fatalf("Wake: %v", err)
	}
	if e.sched.Sleep(run.ID).AsleepSince != nil || sr.restores != 1 {
		t.Fatal("mail did not restore the Standard VM")
	}
}

func TestAVMWhoseStateCannotBeRestoredStartsFromItsDisk(t *testing.T) {
	t.Parallel()
	e, rt, sr := newSaveEnv(t, true)
	run := e.launchACP(t, "say pong")
	waitItems(t, e.sched, run.ID, "the task's turn", turnEnded("end_turn", 1))
	e.sleepRun(t, run.ID)
	sr.saveMu.Lock()
	sr.restoreErr = errors.New("restore: the saved state was written by another kernel")
	sr.saveMu.Unlock()

	if _, err := e.sched.Inject(t.Context(), run.ID, e.member.ID, domain.AgentPrompt{Text: "after sleep"}, false, nil); err != nil {
		t.Fatalf("a prompt to a session whose state is lost: %v", err)
	}
	if state := containerState(e.container(t, run.ID)); state != "running" {
		t.Fatalf("container is %s after the fallback, want running", state)
	}
	execs := rt.all()
	if methods := execs[len(execs)-1].agent.Methods(); len(execs) != 2 || !slices.Contains(methods, acp.AgentMethodSessionResume) {
		t.Fatalf("fell back with %d agents, the last calling %v; want a second one resuming the conversation", len(execs), methods)
	}
	waitItems(t, e.sched, run.ID, "a turn after the fallback", turnEnded("end_turn", 2))
	waitItems(t, e.sched, run.ID, "the fallback's notice", noticed("Woke from sleep", "another kernel"))
}

// An agent that cannot restore its conversation in a new process keeps its
// saved state through a failed restore, for the next wake to try again.
func TestAFailedRestoreOfAnAgentThatCannotResumeKeepsItsState(t *testing.T) {
	t.Parallel()
	e, rt, sr := newSaveEnv(t, false)
	run := e.launchACP(t, "say pong")
	waitItems(t, e.sched, run.ID, "the task's turn", turnEnded("end_turn", 1))
	e.sleepRun(t, run.ID)
	sr.saveMu.Lock()
	sr.restoreErr = errors.New("restore: monitor exited")
	sr.saveMu.Unlock()
	_, err := e.sched.Inject(t.Context(), run.ID, e.member.ID, domain.AgentPrompt{Text: "wake"}, false, nil)
	if err == nil || !strings.Contains(err.Error(), "monitor exited") || !strings.Contains(err.Error(), "cannot restore its conversation") {
		t.Fatalf("Inject = %v, want the restore's error and why it did not start from disk", err)
	}
	state := e.sched.Sleep(run.ID)
	if state.AsleepSince == nil || !state.Saved || !sr.isSaved(e.cid(t, run.ID)) || !strings.Contains(state.WakeError, "monitor exited") {
		t.Fatalf("sleep state after a failed restore = %+v", state)
	}
	sr.saveMu.Lock()
	sr.restoreErr = nil
	sr.saveMu.Unlock()
	if _, err := e.sched.Inject(t.Context(), run.ID, e.member.ID, domain.AgentPrompt{Text: "wake"}, false, nil); err != nil {
		t.Fatalf("the next prompt: %v", err)
	}
	if !adopted(rt) {
		t.Fatal("the second restore did not reattach to the saved agent")
	}
}

// A restore that failed once the guest ran left the VM running with every
// process; the wake takes it up.
func TestAWakeTakesUpAGuestAFailedRestoreLeftRunning(t *testing.T) {
	t.Parallel()
	e, rt, sr := newSaveEnv(t, false)
	run := e.launchACP(t, "say pong")
	waitItems(t, e.sched, run.ID, "the task's turn", turnEnded("end_turn", 1))
	e.sleepRun(t, run.ID)
	sr.saveMu.Lock()
	sr.restoreErr, sr.restoreLate = errors.New("restore: set the clock"), true
	sr.saveMu.Unlock()
	if _, err := e.sched.Inject(t.Context(), run.ID, e.member.ID, domain.AgentPrompt{Text: "wake"}, false, nil); err != nil {
		t.Fatalf("Inject: %v", err)
	}
	if !adopted(rt) {
		t.Fatal("the wake did not take up the running guest's agent")
	}
	waitItems(t, e.sched, run.ID, "a turn after the wake", turnEnded("end_turn", 2))
}

func TestAClosedVMIsSavedAndRelaunchRestoresIt(t *testing.T) {
	t.Parallel()
	e, rt, sr := newSaveEnv(t, false)
	run := e.launchACP(t, "say pong")
	waitItems(t, e.sched, run.ID, "the task's turn", turnEnded("end_turn", 1))
	if err := e.sched.CloseRun(t.Context(), run.ID, e.member.ID, domain.RunMerged); err != nil {
		t.Fatal(err)
	}
	cid := e.cid(t, run.ID)
	if state := e.sched.Sleep(run.ID); state.AsleepSince == nil || !state.Saved || sr.savedFrom(cid) != "paused" {
		t.Fatalf("a closed VM was not saved paused at the close: %+v, saved from %q", state, sr.savedFrom(cid))
	}
	if e.rt.byName(string(run.ID)) == nil {
		t.Fatal("the save destroyed the retained VM")
	}

	s2 := e.restartACP(t)
	if state := s2.Sleep(run.ID); state.AsleepSince == nil || !state.Saved {
		t.Fatalf("the restart lost the closed session's saved state: %+v", state)
	}
	before := len(rt.all())
	if _, err := s2.Relaunch(t.Context(), run.ID, e.member.ID); err != nil {
		t.Fatalf("Relaunch: %v", err)
	}
	if sr.restores != 1 || containerState(e.container(t, run.ID)) != "running" {
		t.Fatalf("Relaunch did not restore the VM: %d restores", sr.restores)
	}
	if methods := rt.all()[before].agent.Methods(); !slices.Contains(methods, acp.AgentMethodSessionResume) {
		t.Fatalf("reopened with %v, want session/resume", methods)
	}
	if _, err := s2.Inject(t.Context(), run.ID, e.member.ID, domain.AgentPrompt{Text: "after reopen"}, false, nil); err != nil {
		t.Fatal(err)
	}
	waitItems(t, s2, run.ID, "a turn after reopening", turnEnded("end_turn", 2))
}

func TestASavedRunWakesWithItsAgentAfterARestart(t *testing.T) {
	t.Parallel()
	e, rt, sr := newSaveEnv(t, false)
	run := e.launchACP(t, "say pong")
	waitItems(t, e.sched, run.ID, "the task's turn", turnEnded("end_turn", 1))
	e.sleepRun(t, run.ID)
	s2 := e.restartACP(t)
	if state := s2.Sleep(run.ID); state.AsleepSince == nil || !state.Saved {
		t.Fatalf("the restart lost the session's sleep: %+v", state)
	}
	if _, err := s2.Inject(t.Context(), run.ID, e.member.ID, domain.AgentPrompt{Text: "after restart"}, false, nil); err != nil {
		t.Fatal(err)
	}
	if sr.restores != 1 || !adopted(rt) {
		t.Fatalf("the wake after a restart did not reattach to the saved agent: %d restores, %d agents", sr.restores, len(rt.all()))
	}
	waitItems(t, s2, run.ID, "a turn after the restart", turnEnded("end_turn", 2))
}

func TestDiskReclaimDropsASavedStateAndTheWakeStartsFromDisk(t *testing.T) {
	t.Parallel()
	e, rt, sr := newSaveEnv(t, true)
	run := e.launchACP(t, "say pong")
	waitItems(t, e.sched, run.ID, "the task's turn", turnEnded("end_turn", 1))
	e.sleepRun(t, run.ID)
	saved := e.sched.savedByAge()
	if len(saved) != 1 {
		t.Fatalf("saved sessions = %d, want 1", len(saved))
	}
	// The sleep holds the run's lifecycle lock a moment after it is
	// recorded, and reclaim skips a run whose lock is held.
	var note string
	waitFor(t, "the saved state dropped", func() bool {
		var err error
		note, err = e.sched.dropSavedState(t.Context(), saved[0])
		if err != nil {
			t.Fatal(err)
		}
		return note != ""
	})
	if !strings.Contains(note, "starts again from its disk") {
		t.Fatalf("dropSavedState note = %q", note)
	}
	if sr.isSaved(e.cid(t, run.ID)) || e.sched.Sleep(run.ID).Saved || len(e.sched.savedByAge()) != 0 {
		t.Fatal("the saved state was not dropped")
	}
	if _, err := e.sched.Inject(t.Context(), run.ID, e.member.ID, domain.AgentPrompt{Text: "after reclaim"}, false, nil); err != nil {
		t.Fatal(err)
	}
	if len(rt.all()) != 2 {
		t.Fatalf("agents after the cold start = %d, want a second one", len(rt.all()))
	}
	waitItems(t, e.sched, run.ID, "a turn after the cold start", turnEnded("end_turn", 2))
}
