package scheduler

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

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

func (r *freezeRuntime) Freeze(id runtime.ID) (bool, error) {
	if r.isSaved(id) {
		return false, errors.New("the VM is not running")
	}
	r.freezeMu.Lock()
	defer r.freezeMu.Unlock()
	if slices.Contains(r.frozen, id) {
		return false, nil
	}
	r.frozen = append(r.frozen, id)
	return true, nil
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
	// The saved VM ends, and every connection into it, its keeper's too.
	r.mu.Lock()
	for _, b := range r.attached {
		if b.identity.ContainerID == id {
			_ = b.conn.Close()
		}
	}
	r.mu.Unlock()
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

// brake is one check of the memory brake with available bytes free.
func (e *testEnv) brake(fr *freezeRuntime, available uint64) {
	fr.setAvailable(available)
	e.sched.brakeVMMemory(context.Background(), fr)
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
	e.brake(fr, 4<<30)
	if frozen := fr.Frozen(); len(frozen) != 0 {
		t.Fatalf("the brake froze %v above the floor", frozen)
	}
	e.brake(fr, 3<<30)
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
	e.guard(t)
	if !fr.isSaved(idle) || !fr.foundFrozen(idle) || fr.isSaved(working) || e.memorySaved(idleRun) != 1<<30 {
		t.Fatalf("the guard's save: idle saved %v frozen %v marked %d, working saved %v",
			fr.isSaved(idle), fr.foundFrozen(idle), e.memorySaved(idleRun), fr.isSaved(working))
	}
	// 4 GiB available again: the VM still frozen thaws, and the session
	// that never slept carries on.
	e.brake(fr, 4<<30)
	if frozen := fr.Frozen(); len(frozen) != 0 {
		t.Fatalf("with memory back above the floor %v stayed frozen", frozen)
	}
	if e.asleep(workingRun) {
		t.Fatal("the thawed session was put to sleep")
	}
	e.brake(fr, 4<<30)
	if e.sched.vmMemoryBraked {
		t.Fatal("the brake still holds with every VM running")
	}

	fr.setAvailable(2*memoryFloor(32<<30) + 1<<30)
	e.guard(t)
	if e.asleep(idleRun) || e.memorySaved(idleRun) != 0 {
		t.Fatal("the guard did not wake the VM it saved frozen once memory was back")
	}
}

// A session the server is working on needs its guest to answer: the brake
// leaves it running, and freezes it with the rest when that is done.
func TestTheMemoryBrakeLeavesASessionTheServerWorksOnRunning(t *testing.T) {
	t.Parallel()
	e, fr := newFreezeEnv(t)
	kept := e.launchVM(t, fr.memoryRuntime, acpmock.PromptWait, 1<<30)
	killed := e.launchVM(t, fr.memoryRuntime, acpmock.PromptWait, 1<<30)
	closing := e.launchVM(t, fr.memoryRuntime, acpmock.PromptWait, 1<<30)
	adopted := e.launchVM(t, fr.memoryRuntime, acpmock.PromptWait, 1<<30)
	keptRun, killedRun, closingRun := e.runOf(t, kept), e.runOf(t, killed), e.runOf(t, closing)
	e.sched.mu.Lock()
	keptEntry, closingEntry := e.sched.runs[keptRun], e.sched.runs[closingRun]
	e.sched.runs[killedRun].killRequested = true
	e.sched.mu.Unlock()
	taken := e.sched.needGuest(adopted)
	sub := e.subscribe(t)

	// The close begins after a check listed its VM: the next check freezes
	// it, and thaws it before it returns.
	e.brake(fr, 4<<30)
	closingEntry.lifecycleMu.Lock()
	e.brake(fr, 3<<30)
	if frozen := fr.Frozen(); !slices.Equal(frozen, []runtime.ID{kept}) {
		t.Fatalf("frozen = %v, want only the session nothing is being done to", frozen)
	}
	waitTimelineEvent(t, sub, keptRun, events.TimelineNote)
	closingEntry.lifecycleMu.Unlock()
	taken()
	e.brake(fr, 3<<30)
	e.brake(fr, 3<<30)
	frozen := fr.Frozen()
	slices.Sort(frozen)
	if want := slices.Sorted(slices.Values([]runtime.ID{kept, closing, adopted})); !slices.Equal(frozen, want) {
		t.Fatalf("frozen = %v, want %v: every session the server is done with, and not the one being killed", frozen, want)
	}

	// Thawed and frozen again as memory comes and goes: no second note.
	e.sched.mu.Lock()
	noted := keptEntry.frozenNotedAt
	e.sched.mu.Unlock()
	if err := fr.Thaw(kept); err != nil {
		t.Fatal(err)
	}
	e.brake(fr, 3<<30)
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
	e.brake(fr, 4<<30)
	e.brake(fr, 3<<30)

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

	e.guard(t)
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

// A save of a VM the brake froze lets go of its agent as an idle save does.
// The frozen guest answers nothing, so the save ends the connection to the
// agent's keeper while the server still waits on the guest, here for the
// last diff snapshot. That end is not the agent's: the wake reattaches to
// it, and its turn carries on.
func TestASaveOfAFrozenVMKeepsItsAgent(t *testing.T) {
	t.Parallel()
	e, fr := newFreezeEnv(t)
	vm := e.launchVM(t, fr.memoryRuntime, acpmock.PromptWait, 3<<30)
	run := e.runOf(t, vm)
	e.git.mu.Lock()
	e.git.stopWatchHook = func(domain.RunID) {
		for deadline := time.Now().Add(5 * time.Second); e.sched.acp.session(run) != nil && time.Now().Before(deadline); {
			time.Sleep(10 * time.Millisecond)
		}
	}
	e.git.mu.Unlock()
	e.brake(fr, 4<<30)
	e.brake(fr, 3<<30)

	e.sched.guardVMMemory(t.Context())
	if !fr.isSaved(vm) || !fr.foundFrozen(vm) {
		t.Fatalf("the guard saved the VM %v, frozen %v; want it saved as it stood", fr.isSaved(vm), fr.foundFrozen(vm))
	}
	fr.setAvailable(16 << 30)
	waitFor(t, "the session awake with its turn running", func() bool {
		e.sched.guardVMMemory(t.Context())
		sess := e.sched.acp.session(run)
		return !e.asleep(run) && sess != nil && sess.State().TurnInFlight
	})
	if !adopted(fr.acpRuntime) {
		t.Fatal("the wake restarted the agent instead of reattaching to it")
	}
	if page, err := e.sched.ACPHistory(run, 0, 0); err != nil || noticed("Enhanced agent exited", "")(page.Items) {
		t.Fatalf("the session recorded its agent's end across the save: %v", err)
	}
}

// A VM the kernel killed while the guard was saving it has no saved state to
// wake from: its run ends as that of any VM that died, also when the server
// shuts down meanwhile.
func TestAVMThatDiesWhileTheGuardSavesItEndsItsRun(t *testing.T) {
	t.Parallel()
	for _, shutdown := range []bool{false, true} {
		e, fr := newFreezeEnv(t)
		working := e.launchVM(t, fr.memoryRuntime, acpmock.PromptWait, 3<<30)
		run := e.runOf(t, working)
		sub := e.subscribe(t)
		fr.saveMu.Lock()
		fr.saving, fr.release = make(chan struct{}), make(chan struct{})
		fr.saveErr = errors.New("vm runtime: save VM: it is not running")
		fr.saveMu.Unlock()
		fr.setAvailable(3 << 30)

		ctx, cancel := context.WithCancel(t.Context())
		guarded := make(chan struct{})
		go func() {
			defer close(guarded)
			e.sched.guardVMMemory(ctx)
		}()
		<-fr.saving
		e.container(t, run).endProcess(137)
		if shutdown {
			cancel()
			e.sched.superCancel()
		}
		close(fr.release)
		<-guarded
		cancel()
		if shutdown {
			e.restartACP(t)
		} else if sc, err := e.sched.readSidecar(run); err != nil || sc.AsleepSince != nil {
			t.Fatalf("a run whose VM died during its save is recorded %+v, %v; want it awake", sc, err)
		}
		waitStatusEvent(t, sub, run, domain.RunFailed)
	}
}

// A server that stops leaves the VMs frozen unless twice the floor is free:
// the next is not watching them yet. The next takes each session up asleep
// without letting its VM run, and the guard saves or wakes it.
func TestARestartTakesUpFrozenVMsWithoutLettingThemRun(t *testing.T) {
	t.Parallel()
	e, fr := newFreezeEnv(t)
	a := e.launchVM(t, fr.memoryRuntime, acpmock.PromptWait, 1<<30)
	b := e.launchVM(t, fr.memoryRuntime, acpmock.PromptWait, 1<<30)
	runs := []domain.RunID{e.runOf(t, a), e.runOf(t, b)}
	e.brake(fr, 4<<30)
	e.brake(fr, 3<<30)
	stopped, stop := context.WithCancel(t.Context())
	stop()
	fr.setAvailable(6 << 30)
	e.sched.watchVMBrake(stopped)
	if frozen := fr.Frozen(); len(frozen) != 2 {
		t.Fatalf("a server stopping with 6 GiB free, under twice the 3.2 GiB floor, left %v frozen; want both", frozen)
	}

	fr.setAvailable(3 << 30)
	s2 := e.restartACP(t)
	if frozen := fr.Frozen(); len(frozen) != 2 {
		t.Fatalf("after the restart %v are frozen; want both VMs still frozen", frozen)
	}
	s2.mu.Lock()
	for _, run := range runs {
		if entry := s2.runs[run]; entry == nil || entry.asleepSince.IsZero() || entry.asleepSaved || entry.memorySaved != 1<<30 {
			s2.mu.Unlock()
			t.Fatalf("the restart took up %s as %+v; want it asleep, unsaved and marked to wake", run, entry)
		}
	}
	s2.mu.Unlock()

	// A wake waits on admission holding the session's lock: its VM stays
	// frozen until admission lets it go ahead. Anything else holding the
	// lock, such as a close, has the VM thawed.
	s2.mu.Lock()
	held := s2.runs[runs[0]]
	held.admitting = true
	s2.mu.Unlock()
	held.lifecycleMu.Lock()
	time.Sleep(3 * vmMemoryBrake)
	frozen := fr.Frozen()
	s2.mu.Lock()
	held.admitting = false
	s2.mu.Unlock()
	if len(frozen) != 2 {
		held.lifecycleMu.Unlock()
		t.Fatalf("with a wake waiting on admission %v are frozen; want both", frozen)
	}
	waitFor(t, "the VM of a session whose lock is held thawed", func() bool { return !slices.Contains(fr.Frozen(), a) })
	held.lifecycleMu.Unlock()
	waitFor(t, "the VM frozen again", func() bool { return slices.Contains(fr.Frozen(), a) })

	s2.guardVMMemory(t.Context())
	if fr.isSaved(a) == fr.isSaved(b) || !fr.foundFrozen(a) && !fr.foundFrozen(b) {
		t.Fatalf("the guard saved a %v, b %v; want one, saved frozen", fr.isSaved(a), fr.isSaved(b))
	}
	saved, unsaved := runs[0], runs[1]
	if fr.isSaved(b) {
		saved, unsaved = runs[1], runs[0]
	}
	awake := func(run domain.RunID) bool {
		sess := s2.acp.session(run)
		return s2.Sleep(run).AsleepSince == nil && sess != nil && sess.State().TurnInFlight
	}

	// 0.6 GiB above twice the floor is room for no saved VM, but the one
	// never saved holds its memory already: it wakes.
	fr.setAvailable(7 << 30)
	waitFor(t, "the brake thawing the VM left", func() bool { return len(fr.Frozen()) == 0 })
	waitFor(t, "the session never saved awake with its turn running", func() bool {
		s2.guardVMMemory(t.Context())
		return awake(unsaved)
	})
	if s2.Sleep(saved).AsleepSince == nil {
		t.Fatal("the guard woke a saved VM whose memory does not fit")
	}
	fr.setAvailable(16 << 30)
	waitFor(t, "the saved session awake with its turn running", func() bool {
		s2.guardVMMemory(t.Context())
		return awake(saved)
	})
}

// A VM a server left frozen with an idle save of it cut off is saved by the
// next as it stands, not let run for the save's sync.
func TestARestartSavesAFrozenVMCaughtFallingAsleepAsItStands(t *testing.T) {
	t.Parallel()
	e, fr := newFreezeEnv(t)
	vm := e.launchVM(t, fr.memoryRuntime, "say pong", 1<<30)
	run := e.runOf(t, vm)
	e.brake(fr, 4<<30)
	e.brake(fr, 3<<30)
	e.sched.mu.Lock()
	entry := e.sched.runs[run]
	entry.asleepSince = time.Now().UTC()
	err := e.sched.writeSidecar(entry.sidecar())
	e.sched.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	s2 := e.restartACP(t)
	if !fr.isSaved(vm) || !fr.foundFrozen(vm) {
		t.Fatalf("after the restart the VM is saved %v, saved frozen %v; want both", fr.isSaved(vm), fr.foundFrozen(vm))
	}
	if state := s2.Sleep(run); state.AsleepSince == nil || !state.Saved {
		t.Fatalf("the restart left the session %+v; want it asleep and saved", state)
	}
}

// The server needs the guest of a VM whose agent it restarts, or whose
// Background turn it ends: the brake thaws that VM and leaves it out, and
// the guard saves another.
func TestAVMWhoseGuestTheServerNeedsIsThawedAndNotSaved(t *testing.T) {
	t.Parallel()
	e, fr := newFreezeEnv(t)
	needed := e.launchVM(t, fr.memoryRuntime, acpmock.PromptWait, 3<<30)
	other := e.launchVM(t, fr.memoryRuntime, acpmock.PromptWait, 1<<30)
	e.brake(fr, 4<<30)
	e.brake(fr, 3<<30)
	release := e.sched.needGuest(needed)
	e.brake(fr, 3<<30)
	if frozen := fr.Frozen(); !slices.Equal(frozen, []runtime.ID{other}) {
		t.Fatalf("frozen = %v, want only the VM nothing needs", frozen)
	}
	e.guard(t)
	if fr.isSaved(needed) || !fr.isSaved(other) {
		t.Fatalf("the guard saved the needed VM %v, the other %v; want only the other", fr.isSaved(needed), fr.isSaved(other))
	}
	release()
	e.brake(fr, 3<<30)
	e.brake(fr, 3<<30)
	if !slices.Contains(fr.Frozen(), needed) {
		t.Fatal("the brake left a VM out after the server was done with it")
	}
}

// frozenUseRuntime is useRuntime with frozen frozen.
type frozenUseRuntime struct {
	useRuntime
	frozen []runtime.ID
}

func (r *frozenUseRuntime) Freeze(runtime.ID) (bool, error) { return false, nil }
func (r *frozenUseRuntime) Frozen() []runtime.ID            { return r.frozen }
func (r *frozenUseRuntime) Thaw(runtime.ID) error           { return nil }

// A woken VM the brake froze loads nothing: its claim stands however long
// it stays frozen.
func TestAFrozenWokenVMKeepsItsClaim(t *testing.T) {
	t.Parallel()
	rt := &frozenUseRuntime{useRuntime: useRuntime{use: map[runtime.ID]uint64{"vm": 1 << 30}}, frozen: []runtime.ID{"vm"}}
	s := &Scheduler{cfg: Config{Runtime: rt}}
	claim := &memoryClaim{bytes: 8 << 30, vm: "vm", held: 1 << 30}
	s.memoryClaims = []*memoryClaim{claim}
	s.releaseClaim(claim)
	claim.grew = claim.grew.Add(-2 * memoryLoadTime)
	if got := s.claimedMemory(t.Context()); got != 7<<30 {
		t.Fatalf("a frozen VM that loaded 1 of its 8 GiB claims %d, want the rest", got)
	}
	rt.frozen = nil
	claim.grew = claim.grew.Add(-2 * memoryLoadTime)
	if got := s.claimedMemory(t.Context()); got != 0 {
		t.Fatalf("thawed, and loading nothing for %s, it still claims %d", memoryLoadTime, got)
	}
}
