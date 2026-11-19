package scheduler

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"github.com/3xDevOps/Aether/internal/acphost"
	"github.com/3xDevOps/Aether/internal/acphost/acpmock"
	"github.com/3xDevOps/Aether/internal/coordtransport"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
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
	// restoreHang holds a restore until its ctx ends, keeping the saved
	// state, as a restore that never gets an answer from its VM does.
	restoreHang bool
	restores    int
	// restoreTime is what was left of the last restore's ctx as it began.
	restoreTime time.Duration
	// commands holds the command each container starts from its disk with.
	commands map[runtime.ID][]string
}

func (r *saveRuntime) Saves(runtime.ID) bool { return true }

func (r *saveRuntime) SetCommand(_ context.Context, id runtime.ID, command []string) error {
	r.saveMu.Lock()
	defer r.saveMu.Unlock()
	if r.commands == nil {
		r.commands = map[runtime.ID][]string{}
	}
	r.commands[id] = command
	return nil
}

func (r *saveRuntime) command(id runtime.ID) []string {
	r.saveMu.Lock()
	defer r.saveMu.Unlock()
	return r.commands[id]
}

func (r *saveRuntime) SavedMemory(runtime.ID) (uint64, error) { return 0, nil }

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
	if deadline, ok := ctx.Deadline(); ok {
		r.restoreTime = time.Until(deadline)
	}
	if r.restoreHang {
		r.saveMu.Unlock()
		<-ctx.Done()
		return ctx.Err()
	}
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
		return runtime.ErrNotSaved
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

// A save keeps every process, so a dev server keeps a VM awake only while
// it uses the CPU, whether the agent runs it as a background shell or a
// terminal has it in the foreground.
func TestOnlyBusyWorkKeepsAVMAwake(t *testing.T) {
	t.Parallel()
	e, _, sr := newSaveEnv(t, false)
	run := e.launchClaudeACP(t, acpmock.PromptDevServer)
	waitItems(t, e.sched, run.ID, "the task's turn", turnEnded("end_turn", 1))
	var cpu atomic.Value
	cpu.Store("150")
	e.rt.mu.Lock()
	e.rt.execHandler = func(_ runtime.ID, argv []string) (int, string, error) {
		switch {
		case slices.Contains(argv, guestCPUScript):
			return 0, cpu.Load().(string) + "\n", nil
		case slices.Contains(argv, foregroundJobScript):
			return 0, "next-server\n", nil
		}
		return 0, "", nil
	}
	e.rt.mu.Unlock()
	e.waitIdle(t, run.ID)
	e.stayAwake(t, run.ID, "the guest was busy")
	cpu.Store("20")
	e.sleepRun(t, run.ID)
	if !sr.isSaved(e.cid(t, run.ID)) {
		t.Fatal("the VM was not saved once idle")
	}
}

// Work the agent waits for, such as a workflow or a monitor, keeps a VM
// awake: a save would freeze it, and the turn that reports on it with it.
func TestWorkTheAgentWaitsForKeepsAVMAwake(t *testing.T) {
	t.Parallel()
	e, _, _ := newSaveEnv(t, false)
	run := e.launchClaudeACP(t, acpmock.PromptHold)
	waitItems(t, e.sched, run.ID, "the task's turn", turnEnded("end_turn", 1))
	waitFor(t, "the workflow held", func() bool { return e.sched.acp.session(run.ID).State().Background })
	e.stayAwake(t, run.ID, "the agent waited for its workflow")
}

// waitIdle waits until nothing in run's own state keeps it awake, so a sweep
// reaches the probes of what runs in its container or VM.
func (e *testEnv) waitIdle(t *testing.T, run domain.RunID) {
	t.Helper()
	waitFor(t, "the session idle", func() bool {
		e.sched.mu.Lock()
		defer e.sched.mu.Unlock()
		return e.sched.liveFactsLocked(e.sched.runs[run]).busy == ""
	})
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
	sr.restoreErr = errors.New("restore: the saved state was written by another kernel\nconsole.log:\nKernel panic")
	sr.saveMu.Unlock()
	sub := e.subscribe(t)

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
	note := waitTimelineEvent(t, sub, run.ID, events.TimelineNote).Payload.(events.TimelinePayload).Message
	if !strings.Contains(note, "Processes and terminals that were running were stopped") || !strings.HasSuffix(note, "another kernel") {
		t.Fatalf("the timeline says %q of a wake that lost the session's processes", note)
	}
}

// A wake that started a session from its disk and then could not reattach
// to it saves it again with no processes: the timeline and the wake's error
// still say what was lost.
func TestAColdStartWhoseReattachFailsNotesTheLostProcesses(t *testing.T) {
	t.Parallel()
	e, rt, sr := newSaveEnv(t, true)
	run := e.launchACP(t, "say pong")
	waitItems(t, e.sched, run.ID, "the task's turn", turnEnded("end_turn", 1))
	e.sleepRun(t, run.ID)
	sr.saveMu.Lock()
	sr.restoreErr = errors.New("restore: the saved state was written by another kernel\nconsole.log:\nKernel panic")
	sr.saveMu.Unlock()
	rt.mu.Lock()
	rt.startErr = errors.New("the agent did not start")
	rt.mu.Unlock()
	sub := e.subscribe(t)

	err := e.sched.WakeRun(t.Context(), run.ID, e.member.ID)
	if err == nil || !strings.Contains(err.Error(), "the agent did not start") || !strings.Contains(err.Error(), "without its processes, as its restore failed: restore: the saved state was written by another kernel") || strings.Contains(err.Error(), "Kernel panic") {
		t.Fatalf("WakeRun = %v; want the reattach's error and the first line of the restore's", err)
	}
	if !sr.isSaved(e.cid(t, run.ID)) {
		t.Fatal("the cold-started VM was not saved again")
	}
	note := waitTimelineEvent(t, sub, run.ID, events.TimelineNote).Payload.(events.TimelinePayload).Message
	if !strings.Contains(note, "Processes and terminals that were running were stopped") || !strings.HasSuffix(note, "another kernel") {
		t.Fatalf("the timeline says %q of a wake that lost the session's processes", note)
	}
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

// A VM that stopped while asleep before it was saved, as one the kernel
// killed while a server restart had it waiting for its save, starts again
// from its disk on the next wake, whatever its agent.
func TestAWakeStartsAVMThatStoppedBeforeItWasSavedFromItsDisk(t *testing.T) {
	t.Parallel()
	e, _, sr := newSaveEnv(t, false)
	run := e.launchACP(t, "say pong")
	waitItems(t, e.sched, run.ID, "the task's turn", turnEnded("end_turn", 1))
	e.sleepRun(t, run.ID)
	sr.saveMu.Lock()
	delete(sr.saved, e.cid(t, run.ID))
	sr.saveMu.Unlock()
	e.sched.mu.Lock()
	e.sched.runs[run.ID].asleepSaved = false
	e.sched.mu.Unlock()
	if _, err := e.sched.Inject(t.Context(), run.ID, e.member.ID, domain.AgentPrompt{Text: "wake"}, false, nil); err != nil {
		t.Fatalf("Inject = %v, want the VM started from its disk", err)
	}
	if got, _ := e.db.GetRun(t.Context(), run.ID); got.Status.Terminal() || containerState(e.container(t, run.ID)) != "running" {
		t.Fatalf("the session is %s in a %s VM, want it open and running", got.Status, containerState(e.container(t, run.ID)))
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

// A closed VM keeps its disks and no memory, and Relaunch starts it from
// them with a command that does not run the task again.
func TestAClosedVMKeepsOnlyItsDisksAndRelaunchStartsItFromThem(t *testing.T) {
	t.Parallel()
	e, rt, sr := newSaveEnv(t, false)
	run := e.launchACP(t, "say pong")
	waitItems(t, e.sched, run.ID, "the task's turn", turnEnded("end_turn", 1))
	if err := e.sched.CloseRun(t.Context(), run.ID, e.member.ID, domain.RunMerged); err != nil {
		t.Fatal(err)
	}
	cid := e.cid(t, run.ID)
	if state := e.sched.Sleep(run.ID); state.AsleepSince == nil || state.Saved || sr.isSaved(cid) || containerState(e.container(t, run.ID)) != "stopped" {
		t.Fatalf("a closed VM was not stopped without saved memory at the close: %+v, container %s", state, containerState(e.container(t, run.ID)))
	}

	s2 := e.restartACP(t)
	if state := s2.Sleep(run.ID); state.AsleepSince == nil || state.Saved {
		t.Fatalf("the restart lost the closed session's stop: %+v", state)
	}
	before := len(rt.all())
	if _, err := s2.Relaunch(t.Context(), run.ID, e.member.ID); err != nil {
		t.Fatalf("Relaunch: %v", err)
	}
	if containerState(e.container(t, run.ID)) != "running" || !slices.Equal(sr.command(cid), wrapTUICommand(nil)) {
		t.Fatalf("Relaunch left the VM %s with the command %q, want it running a bare shell", containerState(e.container(t, run.ID)), sr.command(cid))
	}
	if methods := rt.all()[before].agent.Methods(); !slices.Contains(methods, acp.AgentMethodSessionResume) {
		t.Fatalf("reopened with %v, want session/resume", methods)
	}
	if _, err := s2.Inject(t.Context(), run.ID, e.member.ID, domain.AgentPrompt{Text: "after reopen"}, false, nil); err != nil {
		t.Fatal(err)
	}
	waitItems(t, s2, run.ID, "a turn after reopening", turnEnded("end_turn", 2))
	waitItems(t, s2, run.ID, "the relaunch's notice", noticed("Relaunched", "from its disk"))
}

// forgetConversations makes every agent started from now on refuse to
// restore a conversation, as omp's does.
func forgetConversations(rt *acpRuntime) {
	fix := bytes.Replace(rt.fixture.Initialize, []byte(`"loadSession": true`), []byte(`"loadSession": false`), 1)
	rt.fixture.Initialize = bytes.Replace(fix, []byte(`"resume": {}`), []byte(`"list": {}`), 1)
}

// An agent that cannot restore its conversation starts a new one in a VM
// that started from its disk with no saved memory to go back to: a closed
// session's, and an open one's whose saved memory is gone.
func TestAVMStartedFromItsDiskStartsANewConversationWhenTheAgentCannotRestoreIts(t *testing.T) {
	t.Parallel()
	for _, closed := range []bool{true, false} {
		t.Run(map[bool]string{true: "closed", false: "asleep"}[closed], func(t *testing.T) {
			t.Parallel()
			e, rt, sr := newSaveEnv(t, false)
			run := e.launchACP(t, "say pong")
			waitItems(t, e.sched, run.ID, "the task's turn", turnEnded("end_turn", 1))
			forgetConversations(rt)
			var err error
			if closed {
				if err = e.sched.CloseRun(t.Context(), run.ID, e.member.ID, domain.RunMerged); err == nil {
					_, err = e.sched.Relaunch(t.Context(), run.ID, e.member.ID)
				}
			} else {
				e.sleepRun(t, run.ID)
				if err = sr.Stop(t.Context(), e.cid(t, run.ID), 0); err == nil {
					_, err = e.sched.Inject(t.Context(), run.ID, e.member.ID, domain.AgentPrompt{Text: "wake"}, false, nil)
				}
			}
			if err != nil {
				t.Fatalf("starting the session from its disk: %v", err)
			}
			execs := rt.all()
			if methods := execs[len(execs)-1].agent.Methods(); !slices.Contains(methods, acp.AgentMethodSessionNew) {
				t.Fatalf("the agent started from disk called %v, want session/new", methods)
			}
			waitItems(t, e.sched, run.ID, "the new conversation's notice", noticed("Started a new agent conversation", "could not be restored"))
			if _, err := e.sched.Inject(t.Context(), run.ID, e.member.ID, domain.AgentPrompt{Text: "carry on"}, false, nil); err != nil {
				t.Fatalf("a prompt to the new conversation: %v", err)
			}
		})
	}
}

// A closed Standard VM starts from its disk with the agent's terminal on the
// conversation it reported, with a new one ready for when that is gone, or
// on a new one when it reported none, and never with its launch command,
// which holds the task.
func TestARelaunchedStandardVMReopensItsConversationInsteadOfRunningItsTask(t *testing.T) {
	t.Parallel()
	for _, session := range []string{"sess-1", ""} {
		t.Run("session "+session, func(t *testing.T) {
			t.Parallel()
			var sr *saveRuntime
			e := newSwitchEnv(t, func(cfg *Config) {
				sr = &saveRuntime{acpRuntime: cfg.Runtime.(*acpRuntime), saved: map[runtime.ID]string{}}
				cfg.Runtime = sr
			})
			run := e.launch(t, "fix the flaky test", domain.LaunchTUI)
			waitFor(t, "the container running", func() bool { return containerState(e.container(t, run.ID)) == "running" })
			e.sched.mu.Lock()
			e.sched.runs[run.ID].agentSessionID = session
			e.sched.mu.Unlock()
			if err := e.sched.CloseRun(t.Context(), run.ID, e.member.ID, domain.RunMerged); err != nil {
				t.Fatal(err)
			}
			if _, err := e.sched.Relaunch(t.Context(), run.ID, e.member.ID); err != nil {
				t.Fatalf("Relaunch: %v", err)
			}
			supervisor := wrapTUICommand(nil)
			if session != "" {
				supervisor = wrapResumeCommand(nil)
			}
			command := sr.command(e.cid(t, run.ID))
			if len(command) < 3 || !slices.Equal(command[:len(command)-3], supervisor) {
				t.Fatalf("the VM starts from its disk with %q, want the supervisor running a script", command)
			}
			omp := func(what, script string, resume bool) {
				t.Helper()
				if strings.Contains(script, "fix the flaky test") || !strings.Contains(script, "exec 'omp' '--auto-approve'") || strings.Contains(script, "'--resume=sess-1'") != resume {
					t.Fatalf("%s is %q, want omp without the task, resuming the conversation: %v", what, script, resume)
				}
			}
			omp("the script", command[len(command)-1], session != "")
			if session != "" {
				next, err := os.ReadFile(filepath.Join(e.coord.root, string(run.ID), coordtransport.NextCommandName))
				if err != nil {
					t.Fatal(err)
				}
				omp("the new conversation for a resume that fails", string(next), false)
			}
			if got, _ := e.db.GetRun(t.Context(), run.ID); got.Status != domain.RunRunning || containerState(e.container(t, run.ID)) != "running" {
				t.Fatalf("the relaunched session is %s in a %s VM", got.Status, containerState(e.container(t, run.ID)))
			}
		})
	}
}

func TestARelaunchOfAClosedVMThatFailsCanBeRetried(t *testing.T) {
	t.Parallel()
	e, _, sr := newSaveEnv(t, false)
	run := e.launchACP(t, "say pong")
	waitItems(t, e.sched, run.ID, "the task's turn", turnEnded("end_turn", 1))
	if err := e.sched.CloseRun(t.Context(), run.ID, e.member.ID, domain.RunMerged); err != nil {
		t.Fatal(err)
	}
	e.rt.mu.Lock()
	e.rt.attachHook = func(context.Context, runtime.ID) (runtime.Attachment, error) {
		return nil, errors.New("attach refused")
	}
	e.rt.mu.Unlock()
	if _, err := e.sched.Relaunch(t.Context(), run.ID, e.member.ID); err == nil {
		t.Fatal("Relaunch succeeded with its attach refused")
	}
	if state := e.sched.Sleep(run.ID); state.AsleepSince == nil || sr.isSaved(e.cid(t, run.ID)) || containerState(e.container(t, run.ID)) != "stopped" {
		t.Fatalf("the failed Relaunch left the closed VM %s, %+v; want it stopped on its disks again", containerState(e.container(t, run.ID)), state)
	}
	e.rt.mu.Lock()
	e.rt.attachHook = nil
	e.rt.mu.Unlock()
	if _, err := e.sched.Relaunch(t.Context(), run.ID, e.member.ID); err != nil {
		t.Fatalf("Relaunch after a failed one: %v", err)
	}
	if containerState(e.container(t, run.ID)) != "running" {
		t.Fatal("the second Relaunch left the VM stopped")
	}
}

// A closed VM whose close could not stop it stays awake for the sleep sweep
// and the memory guard to stop. A restart that finds one marked asleep and
// still up, as a server stopped mid-stop leaves it, stops it without saving
// its memory.
func TestARestartStopsAClosedVMWithoutSavingIt(t *testing.T) {
	t.Parallel()
	e, _, sr := newSaveEnv(t, false)
	run := e.launchACP(t, "say pong")
	waitItems(t, e.sched, run.ID, "the task's turn", turnEnded("end_turn", 1))
	e.closeFrozen(t, run.ID)
	e.sched.mu.Lock()
	entry := e.sched.runs[run.ID]
	e.sched.mu.Unlock()
	if err := e.sched.markAsleep(entry); err != nil {
		t.Fatal(err)
	}
	s2 := e.restartACP(t)
	waitFor(t, "the closed VM stopped", func() bool { return containerState(e.container(t, run.ID)) == "stopped" })
	if state := s2.Sleep(run.ID); state.AsleepSince == nil || state.Saved || sr.isSaved(e.cid(t, run.ID)) {
		t.Fatalf("the restart left the closed VM %+v, saved %v; want it asleep on its disks", state, sr.isSaved(e.cid(t, run.ID)))
	}
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

// A stop that cuts a wake short while it reattaches to the agent's keeper
// leaves the keeper running and recorded, and the next server's wake adopts
// it instead of starting another agent beside it.
func TestAStopDuringAWakesReattachKeepsTheAgentForTheNextServer(t *testing.T) {
	t.Parallel()
	e, rt, _ := newSaveEnv(t, false)
	run := e.launchACP(t, "say pong")
	waitItems(t, e.sched, run.ID, "the task's turn", turnEnded("end_turn", 1))
	e.sleepRun(t, run.ID)
	rt.mu.Lock()
	rt.onAttach = func() error {
		e.sched.superCancel()
		return context.Canceled
	}
	rt.mu.Unlock()
	if _, err := e.sched.Inject(t.Context(), run.ID, e.member.ID, domain.AgentPrompt{Text: "during the stop"}, false, nil); err == nil {
		t.Fatal("a wake the stop cut short reported success")
	}
	rt.mu.Lock()
	rt.onAttach = nil
	rt.mu.Unlock()
	if sc, err := e.sched.readSidecar(run.ID); err != nil || sc.AgentKeeper == nil || len(rt.all()) != 1 || rt.all()[0].exited() {
		t.Fatalf("the cut wake left the sidecar %+v, %v and %d agents, the first exited %v; want the keeper recorded and running alone",
			sc, err, len(rt.all()), rt.all()[0].exited())
	}

	s2 := e.restartACP(t)
	if _, err := s2.Inject(t.Context(), run.ID, e.member.ID, domain.AgentPrompt{Text: "after restart"}, false, nil); err != nil {
		t.Fatal(err)
	}
	if !adopted(rt) {
		t.Fatalf("the next server's wake did not adopt the keeper: %d agents", len(rt.all()))
	}
	waitItems(t, s2, run.ID, "a turn after the restart", turnEnded("end_turn", 2))
}

// Low disk takes a closed session's VM, whose work is on its branch, before
// the saved memory of an open one, and leaves one a Relaunch is starting:
// its capacity reservation is what runs the reclaim.
func TestDiskReclaimRemovesAClosedVMBeforeAnOpenSessionsSavedMemory(t *testing.T) {
	t.Parallel()
	e, _, sr := newSaveEnv(t, true)
	open := e.launchACP(t, "say pong")
	waitItems(t, e.sched, open.ID, "the open session's turn", turnEnded("end_turn", 1))
	e.sleepRun(t, open.ID)
	var closed [2]*domain.Run
	for i := range closed {
		closed[i] = e.launchACP(t, "say pong")
		waitItems(t, e.sched, closed[i].ID, "the closed session's turn", turnEnded("end_turn", 1))
		if err := e.sched.CloseRun(t.Context(), closed[i].ID, e.member.ID, domain.RunMerged); err != nil {
			t.Fatal(err)
		}
	}
	e.sched.mu.Lock()
	e.sched.runs[closed[0].ID].relaunching++
	e.sched.mu.Unlock()
	var steps []string
	e.sched.reclaimSessions(t.Context(), "", func(step string) bool {
		steps = append(steps, step)
		return true
	})
	if !slices.Equal(steps, []string{"closed session container"}) || e.rt.byName(string(closed[1].ID)) != nil ||
		e.rt.byName(string(closed[0].ID)) == nil || !sr.isSaved(e.cid(t, open.ID)) {
		t.Fatalf("reclaim freed %q; want only the VM of the closed session nobody is relaunching, with the open session's saved memory kept", steps)
	}
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
