package scheduler

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"

	"github.com/3xDevOps/Aether/internal/acphost/acpmock"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/runtime"
)

// freezeRuntime gives memoryRuntime the cgroup freezer: frozen holds the
// frozen VMs in the order they froze, and savedFrozen the VMs a save found
// frozen, which it thaws as the VM runtime does. A frozen VM's guest
// answers nothing.
type freezeRuntime struct {
	*memoryRuntime
	freezeMu    sync.Mutex
	frozen      []runtime.ID
	savedFrozen map[runtime.ID]bool
}

func (r *freezeRuntime) FreezeAll() []runtime.ID {
	use, _ := r.MemoryUse(context.Background())
	r.freezeMu.Lock()
	defer r.freezeMu.Unlock()
	var froze []runtime.ID
	for id := range use {
		if !slices.Contains(r.frozen, id) {
			froze = append(froze, id)
		}
	}
	slices.Sort(froze)
	r.frozen = append(r.frozen, froze...)
	return froze
}

func (r *freezeRuntime) Freeze(id runtime.ID) error {
	r.freezeMu.Lock()
	defer r.freezeMu.Unlock()
	if !slices.Contains(r.frozen, id) {
		r.frozen = append(r.frozen, id)
	}
	return nil
}

func (r *freezeRuntime) Frozen() []runtime.ID {
	r.freezeMu.Lock()
	defer r.freezeMu.Unlock()
	return slices.Clone(r.frozen)
}

func (r *freezeRuntime) Thaw(id runtime.ID) error {
	r.freezeMu.Lock()
	defer r.freezeMu.Unlock()
	r.frozen = slices.DeleteFunc(r.frozen, func(f runtime.ID) bool { return f == id })
	return nil
}

func (r *freezeRuntime) Save(ctx context.Context, id runtime.ID) error {
	r.freezeMu.Lock()
	r.savedFrozen[id] = slices.Contains(r.frozen, id)
	r.freezeMu.Unlock()
	if err := r.memoryRuntime.Save(ctx, id); err != nil {
		return err
	}
	return r.Thaw(id)
}

var errFrozenGuest = errors.New("the frozen guest does not answer")

func (r *freezeRuntime) Exec(ctx context.Context, id runtime.ID, argv []string, workDir string) (int, string, string, error) {
	if slices.Contains(r.Frozen(), id) {
		return 0, "", "", errFrozenGuest
	}
	return r.memoryRuntime.Exec(ctx, id, argv, workDir)
}

func (r *freezeRuntime) Attach(ctx context.Context, id runtime.ID) (runtime.Attachment, error) {
	if slices.Contains(r.Frozen(), id) {
		return nil, errFrozenGuest
	}
	return r.memoryRuntime.Attach(ctx, id)
}

func (r *freezeRuntime) foundFrozen(id runtime.ID) bool {
	r.freezeMu.Lock()
	defer r.freezeMu.Unlock()
	return r.savedFrozen[id]
}

func newFreezeEnv(t *testing.T) (*testEnv, *freezeRuntime) {
	t.Helper()
	var fr *freezeRuntime
	e, _ := newMemoryEnvWith(t, func(mr *memoryRuntime) runtime.Runtime {
		fr = &freezeRuntime{memoryRuntime: mr, savedFrozen: map[runtime.ID]bool{}}
		return fr
	})
	return e, fr
}

func TestTheMemoryBrakeFreezesEveryVMAndEachEndsThawedOrSavedThenWoken(t *testing.T) {
	t.Parallel()
	e, fr := newFreezeEnv(t)
	idle := e.launchVM(t, fr.memoryRuntime, "say pong", 1<<30)
	working := e.launchVM(t, fr.memoryRuntime, acpmock.PromptWait, 3<<30)
	idleRun, workingRun := e.runOf(t, idle), e.runOf(t, working)
	subs := map[domain.RunID]events.Subscription{idleRun: e.subscribe(t), workingRun: e.subscribe(t)}

	// 4 GiB is above the 3.2 GiB floor: nothing freezes.
	fr.setAvailable(4 << 30)
	e.sched.brakeVMMemory(t.Context(), fr)
	if frozen := fr.Frozen(); len(frozen) != 0 {
		t.Fatalf("the brake froze %v above the floor", frozen)
	}
	fr.setAvailable(3 << 30)
	e.sched.brakeVMMemory(t.Context(), fr)
	if frozen := fr.Frozen(); len(frozen) != 2 {
		t.Fatalf("below the floor the brake froze %v, want both VMs", frozen)
	}
	for run, sub := range subs {
		if ev := waitTimelineEvent(t, sub, run, events.TimelineNote); ev.Payload.(events.TimelinePayload).Message != memoryFrozenNote {
			t.Fatalf("the freeze's note on %s = %+v", run, ev.Payload)
		}
	}

	// The guard saves the idle VM as it stands, frozen, and marks it to
	// wake: its guest could not say whether it was busy.
	e.sched.guardVMMemory(t.Context())
	if !fr.isSaved(idle) || !fr.foundFrozen(idle) || fr.isSaved(working) || e.memorySaved(idleRun) != 1<<30 {
		t.Fatalf("the guard's save: idle saved %v frozen %v marked %d, working saved %v",
			fr.isSaved(idle), fr.foundFrozen(idle), e.memorySaved(idleRun), fr.isSaved(working))
	}
	// 4 GiB available again: the VM still frozen thaws, and the session
	// that never slept carries on.
	e.sched.brakeVMMemory(t.Context(), fr)
	if frozen := fr.Frozen(); len(frozen) != 0 {
		t.Fatalf("with memory back above the floor %v stayed frozen", frozen)
	}
	if e.asleep(workingRun) {
		t.Fatal("the thawed session was put to sleep")
	}
	e.sched.brakeVMMemory(t.Context(), fr)
	if e.sched.vmMemoryBraked {
		t.Fatal("the brake still holds with every VM running")
	}

	fr.setAvailable(2*memoryFloor(32<<30) + 1<<30)
	e.sched.guardVMMemory(t.Context())
	if e.asleep(idleRun) || e.memorySaved(idleRun) != 0 {
		t.Fatal("the guard did not wake the VM it saved frozen once memory was back")
	}
}

// A session the server is working on needs its guest to answer: the brake
// thaws it at once, and freezes it with the rest when that is done.
func TestTheMemoryBrakeLeavesASessionTheServerWorksOnRunning(t *testing.T) {
	t.Parallel()
	e, fr := newFreezeEnv(t)
	kept := e.launchVM(t, fr.memoryRuntime, acpmock.PromptWait, 1<<30)
	killed := e.launchVM(t, fr.memoryRuntime, acpmock.PromptWait, 1<<30)
	closing := e.launchVM(t, fr.memoryRuntime, acpmock.PromptWait, 1<<30)
	keptRun, killedRun, closingRun := e.runOf(t, kept), e.runOf(t, killed), e.runOf(t, closing)
	e.sched.mu.Lock()
	keptEntry, closingEntry := e.sched.runs[keptRun], e.sched.runs[closingRun]
	e.sched.runs[killedRun].killRequested = true
	e.sched.mu.Unlock()
	sub := e.subscribe(t)
	fr.setAvailable(3 << 30)

	closingEntry.lifecycleMu.Lock()
	e.sched.brakeVMMemory(t.Context(), fr)
	closingEntry.lifecycleMu.Unlock()
	if frozen := fr.Frozen(); !slices.Equal(frozen, []runtime.ID{kept}) {
		t.Fatalf("frozen = %v, want only the session nothing is being done to", frozen)
	}
	waitTimelineEvent(t, sub, keptEntry.runID, events.TimelineNote)
	e.sched.brakeVMMemory(t.Context(), fr)
	if frozen := fr.Frozen(); !slices.Equal(frozen, []runtime.ID{kept, closing}) {
		t.Fatalf("frozen = %v, want the session whose close is over frozen too", frozen)
	}

	// Thawed and frozen again as memory comes and goes: no second note.
	e.sched.mu.Lock()
	noted := keptEntry.frozenNotedAt
	e.sched.mu.Unlock()
	if err := fr.Thaw(kept); err != nil {
		t.Fatal(err)
	}
	e.sched.brakeVMMemory(t.Context(), fr)
	e.sched.mu.Lock()
	again := keptEntry.frozenNotedAt
	e.sched.mu.Unlock()
	if !slices.Contains(fr.Frozen(), kept) || !again.Equal(noted) {
		t.Fatalf("a second freeze: frozen %v, noted again %v", fr.Frozen(), !again.Equal(noted))
	}
}

// Admission saves an idle VM the brake froze, marked to wake, and never a
// working one.
func TestAMemoryRefusalSavesAFrozenIdleVM(t *testing.T) {
	t.Parallel()
	e, fr := newFreezeEnv(t)
	idle := e.launchVM(t, fr.memoryRuntime, "say pong", 4<<30)
	working := e.launchVM(t, fr.memoryRuntime, acpmock.PromptWait, 4<<30)
	fr.setAvailable(3 << 30)
	e.sched.brakeVMMemory(t.Context(), fr)

	release, err := e.sched.reserveCapacity(t.Context())
	if err != nil {
		t.Fatalf("admission with a frozen idle VM to save: %v", err)
	}
	release()
	if !fr.isSaved(idle) || !fr.foundFrozen(idle) || e.memorySaved(e.runOf(t, idle)) != 4<<30 || fr.isSaved(working) {
		t.Fatalf("admission saved idle %v frozen %v marked %d, working %v; want only the idle VM, frozen and marked to wake",
			fr.isSaved(idle), fr.foundFrozen(idle), e.memorySaved(e.runOf(t, idle)), fr.isSaved(working))
	}
}

func TestAGuardSaveFreezesItsVMAndThawsOneItCouldNotSave(t *testing.T) {
	t.Parallel()
	e, fr := newFreezeEnv(t)
	working := e.launchVM(t, fr.memoryRuntime, acpmock.PromptWait, 3<<30)
	run := e.runOf(t, working)
	fr.saveMu.Lock()
	fr.saveErr = errors.New("cannot save VM: it shares /workspace over virtio-fs")
	fr.saveMu.Unlock()
	fr.setAvailable(3 << 30)

	e.sched.guardVMMemory(t.Context())
	if !fr.foundFrozen(working) {
		t.Fatal("the guard saved a working VM that was still running")
	}
	if len(fr.Frozen()) != 0 || e.asleep(run) {
		t.Fatalf("after a save that failed: frozen %v, asleep %v; want the session running", fr.Frozen(), e.asleep(run))
	}
	if sess := e.sched.acp.session(run); sess == nil || !sess.State().TurnInFlight {
		t.Fatal("the session's turn did not carry on after the failed save")
	}
}

// A VM the kernel killed while the guard was saving it has no saved state to
// wake from: its run ends as that of any VM that died.
func TestAVMThatDiesWhileTheGuardSavesItEndsItsRun(t *testing.T) {
	t.Parallel()
	e, fr := newFreezeEnv(t)
	working := e.launchVM(t, fr.memoryRuntime, acpmock.PromptWait, 3<<30)
	run := e.runOf(t, working)
	sub := e.subscribe(t)
	fr.saveMu.Lock()
	fr.saving, fr.release = make(chan struct{}), make(chan struct{})
	fr.saveErr = errors.New("vm runtime: save VM: it is not running")
	fr.saveMu.Unlock()
	fr.setAvailable(3 << 30)

	guarded := make(chan struct{})
	go func() {
		defer close(guarded)
		e.sched.guardVMMemory(context.WithoutCancel(t.Context()))
	}()
	<-fr.saving
	e.container(t, run).endProcess(137)
	close(fr.release)
	<-guarded
	waitStatusEvent(t, sub, run, domain.RunFailed)
	if e.sched.Sleep(run).AsleepSince != nil {
		t.Fatal("a run whose VM died during its save is still asleep")
	}
}
