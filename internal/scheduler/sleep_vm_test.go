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
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/runtime"
)

// saveRuntime runs every container as a session VM: a save ends it as a
// stop does but keeps its execs, the agent's keeper among them, and a
// restore starts it again with them.
type saveRuntime struct {
	*acpRuntime

	saveMu     sync.Mutex
	saved      map[runtime.ID]bool
	restoreErr error
	// restoreLate fails a restore only after it deleted the saved state
	// and started the guest, as a failed clock set does.
	restoreLate bool
	restores    int
}

func (r *saveRuntime) Saves(runtime.ID) bool { return true }

func (r *saveRuntime) Save(_ context.Context, id runtime.ID) error {
	c, err := r.get(id)
	if err != nil {
		return err
	}
	if state := c.currentState(); state != "running" {
		return errors.New("save from state " + state)
	}
	c.endProcess(137)
	r.saveMu.Lock()
	r.saved[id] = true
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
		return errors.Join(err, r.Start(ctx, id))
	case err != nil:
		return err
	case !saved:
		return errors.New("it has no saved state")
	}
	return r.Start(ctx, id)
}

func (r *saveRuntime) Stop(ctx context.Context, id runtime.ID, grace time.Duration) error {
	r.saveMu.Lock()
	delete(r.saved, id)
	r.saveMu.Unlock()
	return r.acpRuntime.Stop(ctx, id, grace)
}

func (r *saveRuntime) isSaved(id runtime.ID) bool {
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
		sr = &saveRuntime{acpRuntime: cfg.Runtime.(*acpRuntime), saved: map[runtime.ID]bool{}}
		cfg.Runtime = sr
	})
	return e, rt, sr
}

func (e *testEnv) cid(t *testing.T, run domain.RunID) runtime.ID {
	t.Helper()
	return e.container(t, run).id
}

// adopted reports that no exec beyond the first n opened a conversation
// with session/resume, session/load or session/new since the launch.
func adopted(rt *acpRuntime, n int) bool {
	execs := rt.all()
	if len(execs) != n {
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
	if !adopted(rt, 1) {
		t.Fatal("the wake started another agent or reopened the conversation instead of reattaching")
	}
	waitItems(t, e.sched, run.ID, "a turn after the wake", turnEnded("end_turn", 2))
	waitItems(t, e.sched, run.ID, "the wake's notice", noticed("Woke from sleep", "carry on where they were"))
}

// A background shell such as a dev server kept a container awake, which a
// stop would end; a save keeps it.
func TestAVMSleepsWithADevServerRunning(t *testing.T) {
	t.Parallel()
	e, _, sr := newSaveEnv(t, false)
	run := e.launchClaudeACP(t, "say pong")
	waitItems(t, e.sched, run.ID, "the task's turn", turnEnded("end_turn", 1))
	e.onForegroundProbe(func() (string, error) { return "vim", nil })
	e.sleepRun(t, run.ID)
	if !sr.isSaved(e.cid(t, run.ID)) {
		t.Fatal("the VM was not saved")
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
// saved state through a failed restore, for the next wake to try again,
// also when the restore failed after it had started the guest.
func TestAFailedRestoreOfAnAgentThatCannotResumeKeepsItsState(t *testing.T) {
	t.Parallel()
	for name, late := range map[string]bool{"before the guest ran": false, "after the guest ran": true} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			e, rt, sr := newSaveEnv(t, false)
			run := e.launchACP(t, "say pong")
			waitItems(t, e.sched, run.ID, "the task's turn", turnEnded("end_turn", 1))
			e.sleepRun(t, run.ID)
			sr.saveMu.Lock()
			sr.restoreErr, sr.restoreLate = errors.New("restore: monitor exited"), late
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
			if !adopted(rt, 1) {
				t.Fatal("the second restore did not reattach to the saved agent")
			}
		})
	}
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
	if state := e.sched.Sleep(run.ID); state.AsleepSince == nil || !state.Saved || !sr.isSaved(cid) {
		t.Fatalf("a closed VM was not saved at the close: %+v", state)
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
	if sr.restores != 1 || !adopted(rt, 1) {
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
