package scheduler

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/acphost/acpmock"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/runtime"
)

// memoryRuntime gives saveRuntime a host whose available memory a save
// raises, and a restore lowers, by the VM's memory, as on a real host.
type memoryRuntime struct {
	*saveRuntime
	memMu     sync.Mutex
	available uint64
	vm        map[runtime.ID]uint64
	saves     int
	// lazy makes a restore take no memory until load, as a VM restored on
	// demand; loading holds the VMs restored and not loaded yet.
	lazy    bool
	loading map[runtime.ID]bool
	// savedErr fails SavedMemory, which then reports the 8 GiB cap.
	savedErr error
}

func (r *memoryRuntime) SavedMemory(id runtime.ID) (uint64, error) {
	r.memMu.Lock()
	defer r.memMu.Unlock()
	if r.savedErr != nil {
		return 8 << 30, r.savedErr
	}
	if !r.isSaved(id) {
		return 0, nil
	}
	return r.vm[id], nil
}

func (r *memoryRuntime) load(id runtime.ID) {
	r.memMu.Lock()
	defer r.memMu.Unlock()
	delete(r.loading, id)
	r.available -= r.vm[id]
}

func (r *memoryRuntime) Capacity(context.Context) (runtime.HostCapacity, error) {
	r.memMu.Lock()
	defer r.memMu.Unlock()
	host := healthyAdmissionHost()
	host.MemoryAvailableBytes = r.available
	return host, nil
}

func (r *memoryRuntime) hostMemory() (total, available uint64, err error) {
	r.memMu.Lock()
	defer r.memMu.Unlock()
	return 32 << 30, r.available, nil
}

func (r *memoryRuntime) MemoryUse(context.Context) (map[runtime.ID]uint64, error) {
	r.memMu.Lock()
	defer r.memMu.Unlock()
	out := map[runtime.ID]uint64{}
	for id, n := range r.vm {
		c, err := r.get(id)
		switch {
		case r.loading[id]:
			out[id] = 0
		case !r.isSaved(id) && (err != nil || containerState(c) != "stopped"):
			out[id] = n
		}
	}
	return out, nil
}

func (r *memoryRuntime) Save(ctx context.Context, id runtime.ID) error {
	r.memMu.Lock()
	r.saves++
	r.memMu.Unlock()
	if err := r.saveRuntime.Save(ctx, id); err != nil {
		return err
	}
	r.memMu.Lock()
	defer r.memMu.Unlock()
	r.available += r.vm[id]
	return nil
}

func (r *memoryRuntime) Restore(ctx context.Context, id runtime.ID) error {
	if err := r.saveRuntime.Restore(ctx, id); err != nil {
		return err
	}
	r.memMu.Lock()
	defer r.memMu.Unlock()
	if r.lazy {
		r.loading[id] = true
		return nil
	}
	r.available -= r.vm[id]
	return nil
}

func (r *memoryRuntime) setAvailable(n uint64) {
	r.memMu.Lock()
	defer r.memMu.Unlock()
	r.available = n
}

func (r *memoryRuntime) saveCount() int {
	r.memMu.Lock()
	defer r.memMu.Unlock()
	return r.saves
}

// newMemoryEnv's host has 32 GiB, so the guard starts below 3.2 GiB
// available and stops at 6.4 GiB. Its agents restore their conversation in
// a new process, as Claude Code does.
func newMemoryEnv(t *testing.T) (*testEnv, *memoryRuntime) {
	t.Helper()
	return newMemoryEnvWith(t, func(mr *memoryRuntime) runtime.Runtime { return mr })
}

// newMemoryEnvWith is newMemoryEnv with wrap's runtime around the host's.
func newMemoryEnvWith(t *testing.T, wrap func(*memoryRuntime) runtime.Runtime) (*testEnv, *memoryRuntime) {
	t.Helper()
	var mr *memoryRuntime
	e, _ := newACPEnv(t, func(cfg *Config) {
		sr := &saveRuntime{acpRuntime: cfg.Runtime.(*acpRuntime), saved: map[runtime.ID]string{}}
		mr = &memoryRuntime{saveRuntime: sr, available: 24 << 30, vm: map[runtime.ID]uint64{}, loading: map[runtime.ID]bool{}}
		cfg.Runtime = wrap(mr)
		cfg.hostMemory = mr.hostMemory
		cfg.sleepAnyAgent = true
	})
	e.onGuestCPU("0")
	return e, mr
}

// launchVM launches an Enhanced session using memory of the host's, and
// waits for its agent to go idle, or with PromptWait, for its turn to run.
func (e *testEnv) launchVM(t *testing.T, mr *memoryRuntime, task string, memory uint64) runtime.ID {
	t.Helper()
	run := e.launchACP(t, task)
	if task == acpmock.PromptWait {
		waitFor(t, "the turn running", func() bool {
			sess := e.sched.acp.session(run.ID)
			return sess != nil && sess.State().TurnInFlight
		})
	} else {
		waitFor(t, "the agent idle", func() bool {
			e.sched.mu.Lock()
			defer e.sched.mu.Unlock()
			return e.sched.liveFactsLocked(e.sched.runs[run.ID]).busy == ""
		})
	}
	cid := e.cid(t, run.ID)
	mr.memMu.Lock()
	mr.vm[cid] = memory
	mr.memMu.Unlock()
	return cid
}

func (e *testEnv) memorySaved(run domain.RunID) uint64 {
	e.sched.mu.Lock()
	defer e.sched.mu.Unlock()
	return e.sched.runs[run].memorySaved
}

func (e *testEnv) runOf(t *testing.T, cid runtime.ID) domain.RunID {
	t.Helper()
	e.sched.mu.Lock()
	defer e.sched.mu.Unlock()
	for id, entry := range e.sched.runs {
		if entry.containerID == cid {
			return id
		}
	}
	t.Fatalf("no run in %s", cid)
	return ""
}

func (e *testEnv) asleep(run domain.RunID) bool {
	return e.sched.Sleep(run).AsleepSince != nil
}

// guard is one check of the memory guard and the wake it starts, if any.
func (e *testEnv) guard(t *testing.T) {
	t.Helper()
	e.sched.guardVMMemory(t.Context())
	waitFor(t, "the guard's wake", func() bool {
		e.sched.mu.Lock()
		defer e.sched.mu.Unlock()
		return !e.sched.vmMemoryWaking
	})
}

// A save that hangs, here of a VM recorded asleep and still running as a
// restart takes one up, holds up the guard for memorySaveHold, and then goes
// on alone: that check saves no other VM, and a later one saves the next
// once the disk has room for both VMs' memory.
func TestAHungSaveHoldsUpTheGuardOnlySoLong(t *testing.T) {
	t.Parallel()
	e, mr := newMemoryEnv(t)
	big := e.launchVM(t, mr, acpmock.PromptWait, 3<<30)
	small := e.launchVM(t, mr, acpmock.PromptWait, 1<<30)
	bigRun := e.runOf(t, big)
	e.sched.mu.Lock()
	asleep := e.sched.runs[bigRun]
	e.sched.mu.Unlock()
	if err := e.sched.markAsleep(asleep); err != nil {
		t.Fatal(err)
	}
	// A 100 GiB disk keeps 5 GiB, and below 9 GiB free disk reclaim deletes
	// other sessions' saved memory: 12 GiB free holds either VM's, not both.
	var free atomic.Uint64
	free.Store(12 << 30)
	setFreeBytes(e, &free)
	hold := make(chan struct{})
	unhang := sync.OnceFunc(func() { close(hold) })
	defer unhang()
	mr.saveMu.Lock()
	mr.saving, mr.release = make(chan struct{}), hold
	mr.saveMu.Unlock()
	mr.setAvailable(3 << 30)

	start := time.Now()
	guarded := make(chan struct{})
	go func() {
		defer close(guarded)
		e.sched.guardVMMemory(t.Context())
	}()
	<-mr.saving
	mr.saveMu.Lock()
	mr.saving, mr.release = nil, nil
	mr.saveMu.Unlock()
	select {
	case <-guarded:
	case <-time.After(memorySaveHold + 10*time.Second):
		t.Fatal("the guard waited on the hung save")
	}
	if took := time.Since(start); took < memorySaveHold || mr.isSaved(small) || mr.isSaved(big) {
		t.Fatalf("after %s the guard saved the small VM %v, the hung one %v; want neither, after %s", took, mr.isSaved(small), mr.isSaved(big), memorySaveHold)
	}
	e.guard(t)
	if mr.isSaved(small) {
		t.Fatal("the guard saved a VM whose memory fits on disk only if the hung save writes nothing")
	}
	free.Store(13 << 30)
	e.guard(t)
	if !mr.isSaved(small) || mr.isSaved(big) {
		t.Fatalf("with room on disk the guard saved the small VM %v, the hung one %v; want the small one while the other hangs", mr.isSaved(small), mr.isSaved(big))
	}
	unhang()
	waitFor(t, "the hung save finishing", func() bool { return mr.isSaved(big) && e.sched.Sleep(bigRun).Saved })
}

// A wake the guard started that hangs holds up neither the guard nor a
// shutdown: the guard saves another VM meanwhile, and a stop cuts the wake
// short, leaving the session asleep, saved and marked to wake for the next
// server.
func TestAHungWakeHoldsUpNeitherTheGuardNorAShutdown(t *testing.T) {
	t.Parallel()
	e, mr := newMemoryEnv(t)
	hung := e.launchVM(t, mr, acpmock.PromptWait, 3<<30)
	other := e.launchVM(t, mr, acpmock.PromptWait, 1<<30)
	run := e.runOf(t, hung)
	mr.setAvailable(3 << 30)
	e.guard(t)
	if !mr.isSaved(hung) || mr.isSaved(other) {
		t.Fatal("the guard did not save the working VM holding the most memory")
	}
	mr.saveMu.Lock()
	mr.restoreHang = true
	mr.saveMu.Unlock()
	mr.setAvailable(16 << 30)

	ctx, stop := context.WithCancel(t.Context())
	guarded := make(chan struct{})
	go func() {
		defer close(guarded)
		e.sched.watchVMMemory(ctx)
	}()
	waitFor(t, "the guard waking the saved VM", func() bool { return e.sched.Sleep(run).Waking })
	mr.setAvailable(3 << 30)
	waitFor(t, "the guard saving the other VM while the wake hangs", func() bool { return mr.isSaved(other) })

	stop()
	closed := make(chan error, 1)
	go func() {
		<-guarded
		closed <- e.sched.Close()
	}()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the shutdown waited for the hung wake")
	}
	if sc, err := e.sched.readSidecar(run); err != nil || sc.AsleepSince == nil || !sc.AsleepSaved || sc.MemorySaved != 3<<30 || sc.WakeError != "" || !mr.isSaved(hung) {
		t.Fatalf("the cut wake left %+v, %v, saved %v; want the session asleep, saved and marked to wake", sc, err, mr.isSaved(hung))
	}
}

func TestTheMemoryGuardSavesTheLongestIdleVMFirst(t *testing.T) {
	t.Parallel()
	e, mr := newMemoryEnv(t)
	older := e.launchVM(t, mr, "say pong", 1<<30)
	newer := e.launchVM(t, mr, "say pong", 4<<30)
	sub := e.subscribe(t)
	mr.setAvailable(3 << 30)

	e.guard(t)
	if !mr.isSaved(older) || mr.isSaved(newer) {
		t.Fatal("the guard did not save the longest idle VM first")
	}
	// 4 GiB available: above the floor, but short of the stop level.
	e.guard(t)
	if !mr.isSaved(newer) {
		t.Fatal("the guard stopped saving before the stop level")
	}
	if ev := waitTimelineEvent(t, sub, e.runOf(t, older), events.TimelineNote); ev.Payload.(events.TimelinePayload).Message != memorySavedIdleNote {
		t.Fatalf("the save's note = %+v", ev.Payload)
	}
	e.guard(t)
	for _, cid := range []runtime.ID{older, newer} {
		if run := e.runOf(t, cid); !e.asleep(run) || e.memorySaved(run) != 0 {
			t.Fatalf("the guard woke, or marked to wake, an idle VM it saved: %s", run)
		}
	}
}

func TestTheMemoryGuardSavesAWorkingVMWhenNoneIsIdleAndWakesEachOnceItsMemoryFits(t *testing.T) {
	t.Parallel()
	e, mr := newMemoryEnv(t)
	small := e.launchVM(t, mr, acpmock.PromptWait, 1<<30)
	big := e.launchVM(t, mr, acpmock.PromptWait, 3<<30)
	smallRun, bigRun := e.runOf(t, small), e.runOf(t, big)
	mr.setAvailable(3 << 30)

	e.guard(t)
	if !mr.isSaved(big) || mr.isSaved(small) || e.memorySaved(bigRun) != 3<<30 {
		t.Fatalf("the guard did not save the working VM using the most memory, marked to wake: saved %v %v, mark %d",
			mr.isSaved(big), mr.isSaved(small), e.memorySaved(bigRun))
	}
	if sc, err := e.sched.readSidecar(bigRun); err != nil || sc.MemorySaved != 3<<30 {
		t.Fatalf("the mark was not recorded: %+v, %v", sc.MemorySaved, err)
	}
	e.guard(t)
	if !mr.isSaved(small) {
		t.Fatal("the guard stopped saving before the stop level")
	}
	// 7 GiB available is above the stop level, but leaves room for neither.
	e.guard(t)
	if !e.asleep(bigRun) || !e.asleep(smallRun) {
		t.Fatal("the guard woke a VM with no room for its memory")
	}

	// Room for the 1 GiB VM saved second, not for the one saved first.
	mr.setAvailable(2*memoryFloor(32<<30) + 1<<30)
	e.guard(t)
	if e.asleep(smallRun) || !e.asleep(bigRun) {
		t.Fatal("the guard did not wake the VM whose memory fits, alone")
	}
	mr.setAvailable(2*memoryFloor(32<<30) + 3<<30)
	e.guard(t)
	if e.asleep(bigRun) || e.memorySaved(bigRun) != 0 {
		t.Fatal("the guard did not wake the VM it saved first once memory was back")
	}
	if sess := e.sched.acp.session(bigRun); sess == nil || !sess.State().TurnInFlight {
		t.Fatal("the woken session's turn did not carry on")
	}
}

func TestAMemoryRefusalSavesIdleVMsAndAdmitsAgain(t *testing.T) {
	t.Parallel()
	e, mr := newMemoryEnv(t)
	idle := e.launchVM(t, mr, "say pong", 4<<30)
	working := e.launchVM(t, mr, acpmock.PromptWait, 4<<30)
	mr.setAvailable(3 << 30)

	release, err := e.sched.reserveCapacity(t.Context())
	if err != nil {
		t.Fatalf("admission after saving an idle VM: %v", err)
	}
	release()
	if !mr.isSaved(idle) || mr.isSaved(working) {
		t.Fatalf("admission saved idle %v, working %v; want only the idle VM", mr.isSaved(idle), mr.isSaved(working))
	}
	mr.setAvailable(3 << 30)
	if _, err := e.sched.reserveCapacity(t.Context()); !errors.Is(err, ErrMemoryPressure) {
		t.Fatalf("admission with only a working VM awake = %v, want the memory refusal", err)
	}
	if mr.isSaved(working) {
		t.Fatal("admission saved a working VM")
	}
}

func TestAWakeMustFitTheMemoryItLoadsBackAndALaunchOnlyAStart(t *testing.T) {
	t.Parallel()
	e, mr := newMemoryEnv(t)
	vm := e.launchVM(t, mr, "say pong", 6<<30)
	run := e.runOf(t, vm)
	mr.setAvailable(3 << 30)
	e.guard(t)
	if !mr.isSaved(vm) {
		t.Fatal("the guard did not save the idle VM")
	}

	floor := memoryFloor(32 << 30)
	mr.setAvailable(floor + 6<<30 - 1)
	if err := e.sched.WakeRun(t.Context(), run, ""); !errors.Is(err, ErrMemoryPressure) || !e.asleep(run) {
		t.Fatalf("a wake one byte short of its saved memory = %v, asleep %v; want the memory refusal", err, e.asleep(run))
	}
	release, err := e.sched.reserveCapacity(t.Context())
	if err != nil {
		t.Fatalf("a launch with 6 GiB above the floor: %v", err)
	}
	release()
	mr.setAvailable(floor + 6<<30)
	mr.memMu.Lock()
	mr.savedErr = errors.New("read the snapshot: input/output error")
	mr.memMu.Unlock()
	if err := e.sched.WakeRun(t.Context(), run, ""); !errors.Is(err, ErrMemoryPressure) || !e.asleep(run) {
		t.Fatalf("a wake whose saved memory cannot be read = %v, asleep %v; want it to count the cap", err, e.asleep(run))
	}
	mr.memMu.Lock()
	mr.savedErr = nil
	mr.memMu.Unlock()
	if err := e.sched.WakeRun(t.Context(), run, ""); err != nil || e.asleep(run) {
		t.Fatalf("a wake that fits its saved memory = %v, asleep %v", err, e.asleep(run))
	}
	mr.saveMu.Lock()
	left := mr.restoreTime
	mr.saveMu.Unlock()
	if want := wakeTimeout + 6<<30/restoreRate*time.Second; left < want-time.Minute || left > want {
		t.Errorf("the restore of 6 GiB had %s of the wake left, want about %s", left, want)
	}
}

func TestAWakeSavesIdleVMsUntilItsMemoryFits(t *testing.T) {
	t.Parallel()
	e, mr := newMemoryEnv(t)
	sleeper := e.launchVM(t, mr, "say pong", 6<<30)
	run := e.runOf(t, sleeper)
	mr.setAvailable(3 << 30)
	e.guard(t)
	if !mr.isSaved(sleeper) {
		t.Fatal("the guard did not save the idle VM")
	}
	idle := e.launchVM(t, mr, "say pong", 8<<30)
	working := e.launchVM(t, mr, acpmock.PromptWait, 8<<30)

	mr.setAvailable(memoryFloor(32<<30) + 1<<30)
	if err := e.sched.WakeRun(t.Context(), run, ""); err != nil || e.asleep(run) {
		t.Fatalf("a wake with an idle VM to save = %v, asleep %v", err, e.asleep(run))
	}
	if !mr.isSaved(idle) || mr.isSaved(working) {
		t.Fatalf("the wake saved idle %v, working %v; want only the idle VM", mr.isSaved(idle), mr.isSaved(working))
	}
}

// A wake whose save of an idle VM hangs waits for it memorySaveHold, saves
// no other VM and is refused; the save goes on, and once it lands the next
// wake fits.
func TestAWakeStopsSavingIdleVMsAtASaveThatHangs(t *testing.T) {
	t.Parallel()
	e, mr := newMemoryEnv(t)
	sleeper := e.launchVM(t, mr, "say pong", 6<<30)
	run := e.runOf(t, sleeper)
	mr.setAvailable(3 << 30)
	e.guard(t)
	if !mr.isSaved(sleeper) {
		t.Fatal("the guard did not save the idle VM")
	}
	older := e.launchVM(t, mr, "say pong", 8<<30)
	newer := e.launchVM(t, mr, "say pong", 8<<30)
	hold := make(chan struct{})
	unhang := sync.OnceFunc(func() { close(hold) })
	defer unhang()
	mr.saveMu.Lock()
	mr.saving, mr.release = make(chan struct{}), hold
	mr.saveMu.Unlock()
	mr.setAvailable(memoryFloor(32<<30) + 1<<30)

	start := time.Now()
	woke := make(chan error, 1)
	go func() { woke <- e.sched.WakeRun(t.Context(), run, "") }()
	<-mr.saving
	mr.saveMu.Lock()
	mr.saving, mr.release = nil, nil
	mr.saveMu.Unlock()
	if err := <-woke; !errors.Is(err, ErrMemoryPressure) || time.Since(start) < memorySaveHold || mr.isSaved(newer) || !e.asleep(run) {
		t.Fatalf("after %s the wake = %v, asleep %v, the next idle VM saved %v; want it refused after %s with nothing more saved",
			time.Since(start), err, e.asleep(run), mr.isSaved(newer), memorySaveHold)
	}
	unhang()
	waitFor(t, "the hung save landing", func() bool { return mr.isSaved(older) })
	if err := e.sched.WakeRun(t.Context(), run, ""); err != nil || e.asleep(run) || mr.isSaved(newer) {
		t.Fatalf("the wake after the save landed = %v, asleep %v, the other idle VM saved %v", err, e.asleep(run), mr.isSaved(newer))
	}
}

func TestAVMStillLoadingItsMemoryCountsAgainstTheNextWake(t *testing.T) {
	t.Parallel()
	e, mr := newMemoryEnv(t)
	first := e.launchVM(t, mr, acpmock.PromptWait, 4<<30)
	second := e.launchVM(t, mr, acpmock.PromptWait, 4<<30)
	firstRun, secondRun := e.runOf(t, first), e.runOf(t, second)
	mr.setAvailable(1 << 30)
	e.guard(t)
	e.guard(t)
	if !mr.isSaved(first) || !mr.isSaved(second) {
		t.Fatal("the guard did not save both working VMs")
	}
	e.sched.mu.Lock()
	e.sched.runs[firstRun].asleepSince = e.sched.runs[secondRun].asleepSince.Add(-time.Minute)
	e.sched.mu.Unlock()
	mr.memMu.Lock()
	mr.lazy = true
	mr.memMu.Unlock()

	// Room for one above the guard's stop level, which the host goes on
	// showing while the first loads.
	room := 2*memoryFloor(32<<30) + 4<<30
	mr.setAvailable(room)
	e.guard(t)
	if e.asleep(firstRun) || !e.asleep(secondRun) {
		t.Fatal("the guard did not wake the VM saved first, alone")
	}
	e.guard(t)
	if !e.asleep(secondRun) || e.memorySaved(secondRun) == 0 {
		t.Fatal("the guard woke the second VM into memory the first has yet to load, or gave up on it")
	}
	if err := e.sched.WakeRun(t.Context(), secondRun, ""); !errors.Is(err, ErrMemoryPressure) {
		t.Fatalf("a wake into memory another VM has yet to load = %v, want the memory refusal", err)
	}

	mr.load(first)
	e.guard(t)
	if !e.asleep(secondRun) {
		t.Fatal("the guard woke the second VM with no room left")
	}
	mr.setAvailable(room)
	e.guard(t)
	if e.asleep(secondRun) {
		t.Fatal("the guard did not wake the second VM once the first had loaded and memory was back")
	}
}

func TestAWakeMakesRoomForStartsInFlightAndSavesNothingThatCannotHelp(t *testing.T) {
	t.Parallel()
	e, mr := newMemoryEnv(t)
	sleeper := e.launchVM(t, mr, "say pong", 6<<30)
	run := e.runOf(t, sleeper)
	mr.setAvailable(3 << 30)
	e.guard(t)
	if !mr.isSaved(sleeper) {
		t.Fatal("the guard did not save the idle VM")
	}
	var idle []runtime.ID
	for range 4 {
		idle = append(idle, e.launchVM(t, mr, "say pong", 4<<30))
	}
	inFlight := &memoryClaim{bytes: 12 << 30}
	e.sched.capacityGate <- struct{}{}
	e.sched.memoryClaims = append(e.sched.memoryClaims, inFlight)
	<-e.sched.capacityGate
	mr.setAvailable(memoryFloor(32<<30) + 1<<30)

	// The 16 GiB the idle VMs hold leave the wake 1 GiB short.
	if err := e.sched.WakeRun(t.Context(), run, ""); !errors.Is(err, ErrMemoryPressure) {
		t.Fatalf("a wake all idle VMs cannot make room for = %v, want the memory refusal", err)
	}
	if n := mr.saveCount(); n != 1 {
		t.Fatalf("%d saves; want none beyond the guard's", n-1)
	}

	// Twice the floor and the wake's 6 GiB alone would stop after three.
	e.sched.capacityGate <- struct{}{}
	inFlight.bytes = 8 << 30
	<-e.sched.capacityGate
	if err := e.sched.WakeRun(t.Context(), run, ""); err != nil || e.asleep(run) {
		t.Fatalf("a wake once idle VMs can make room = %v, asleep %v", err, e.asleep(run))
	}
	for _, cid := range idle {
		if !mr.isSaved(cid) {
			t.Fatalf("the wake left idle VM %s awake short of the room it needs", cid)
		}
	}
}

// A boot to commit a saved VM's work claims what it loads back as a wake
// does: wakes count it, and it counts theirs.
func TestABootToCommitAndAWakeCountEachOthersMemory(t *testing.T) {
	t.Parallel()
	e, mr := newMemoryEnv(t)
	booted := e.launchVM(t, mr, "say pong", 6<<30)
	woken := e.launchVM(t, mr, "say pong", 6<<30)
	for _, cid := range []runtime.ID{booted, woken} {
		mr.setAvailable(3 << 30)
		e.guard(t)
		if !mr.isSaved(cid) {
			t.Fatal("the guard did not save the idle VMs, the longest idle first")
		}
	}
	run := e.runOf(t, woken)
	mr.memMu.Lock()
	mr.lazy = true
	mr.memMu.Unlock()
	mr.setAvailable(memoryFloor(32<<30) + 7<<30)

	release, err := e.sched.reserveBoot(t.Context(), booted)
	if err != nil {
		t.Fatalf("a boot to commit with room for its saved memory: %v", err)
	}
	if err = e.sched.WakeRun(t.Context(), run, ""); !errors.Is(err, ErrMemoryPressure) {
		t.Fatalf("a wake while a boot to commit loads 6 GiB = %v, want the memory refusal", err)
	}
	release()
	if err = e.sched.WakeRun(t.Context(), run, ""); err != nil {
		t.Fatalf("a wake once the boot ended: %v", err)
	}
	if _, err = e.sched.reserveBoot(t.Context(), booted); !errors.Is(err, ErrMemoryPressure) {
		t.Fatalf("a boot to commit while a woken VM has 6 GiB to load = %v, want the memory refusal", err)
	}
}

// useRuntime measures each VM's memory as use, or fails with err.
type useRuntime struct {
	runtime.Runtime
	use map[runtime.ID]uint64
	err error
}

func (r *useRuntime) MemoryUse(context.Context) (map[runtime.ID]uint64, error) { return r.use, r.err }

func TestAWokenVMsClaimEndsOnlyOnceItsMemoryStopsGrowing(t *testing.T) {
	t.Parallel()
	rt := &useRuntime{}
	s := &Scheduler{cfg: Config{Runtime: rt}}
	woke := func() *memoryClaim {
		claim := &memoryClaim{bytes: 6 << 30, vm: "vm"}
		s.memoryClaims = []*memoryClaim{claim}
		s.releaseClaim(claim)
		return claim
	}
	claim := woke()
	aged := func() { claim.grew = claim.grew.Add(-2 * memoryLoadTime) }
	claims := func(use map[runtime.ID]uint64, err error) uint64 {
		rt.use, rt.err = use, err
		return s.claimedMemory(t.Context())
	}

	// A large VM on a slow disk takes minutes to load back.
	for _, held := range []uint64{1 << 30, 3 << 30} {
		aged()
		if got := claims(map[runtime.ID]uint64{"vm": held}, nil); got != 6<<30-held {
			t.Fatalf("a VM that has loaded %d bytes claims %d, want the rest", held, got)
		}
	}
	// One unreadable cgroup fails the measurement and reads as its cap.
	unread := errors.New("memory of VM vm: parse memory.current")
	if got := claims(map[runtime.ID]uint64{"vm": 8 << 30}, unread); got != 3<<30 {
		t.Fatalf("with its memory unmeasured the claim is %d, want it as it was", got)
	}
	aged()
	if got := claims(nil, unread); got != 0 || len(s.memoryClaims) != 0 {
		t.Fatalf("a claim seen growing no more for %s still claims %d", memoryLoadTime, got)
	}

	claim = woke()
	if got := claims(map[runtime.ID]uint64{"vm": 4 << 30}, nil); got != 2<<30 {
		t.Fatalf("a VM holding 4 GiB claims %d, want 2 GiB", got)
	}
	aged()
	if got := claims(map[runtime.ID]uint64{"vm": 4 << 30}, nil); got != 0 || len(s.memoryClaims) != 0 {
		t.Fatalf("a VM that held no more for %s still claims %d", memoryLoadTime, got)
	}
	for name, use := range map[string]map[runtime.ID]uint64{"stopped": {}, "loaded": {"vm": 6 << 30}} {
		woke()
		if got := claims(use, nil); got != 0 || len(s.memoryClaims) != 0 {
			t.Fatalf("a %s VM still claims %d", name, got)
		}
	}
}

// A closed VM still in memory, one its close could not stop or one a
// restart left up asleep, is stopped, not saved, and needs no disk room
// for that.
func TestTheMemoryGuardStopsClosedVMsWithoutSavingThem(t *testing.T) {
	t.Parallel()
	e, mr := newMemoryEnv(t)
	open := e.launchVM(t, mr, "say pong", 1<<30)
	closed := []runtime.ID{e.launchVM(t, mr, "say pong", 1<<30), e.launchVM(t, mr, "say pong", 1<<30)}
	for _, cid := range closed {
		e.closeFrozen(t, e.runOf(t, cid))
	}
	run := e.runOf(t, closed[1])
	e.sched.mu.Lock()
	for _, entry := range e.sched.runs {
		entry.saveFailedAt = time.Time{}
	}
	asleep := e.sched.runs[run]
	e.sched.mu.Unlock()
	if err := e.sched.markAsleep(asleep); err != nil {
		t.Fatal(err)
	}
	var free atomic.Uint64
	free.Store(19 << 29)
	setFreeBytes(e, &free)
	mr.setAvailable(3 << 30)
	for range 3 {
		e.guard(t)
	}
	for _, cid := range closed {
		if state := containerState(e.container(t, e.runOf(t, cid))); mr.isSaved(cid) || state != "stopped" {
			t.Fatalf("the guard left closed VM %s %s, saved %v; want it stopped", cid, state, mr.isSaved(cid))
		}
	}
	if mr.isSaved(open) {
		t.Fatal("the guard saved an open VM whose memory does not fit on disk")
	}
}

func TestTheMemoryGuardSkipsSavesThatFailedOrWouldNotFitOnDisk(t *testing.T) {
	t.Parallel()
	e, mr := newMemoryEnv(t)
	first := e.launchVM(t, mr, "say pong", 1<<30)
	second := e.launchVM(t, mr, "say pong", 1<<30)
	mr.saveMu.Lock()
	mr.saveErr = errors.New("no space left on device")
	mr.saveMu.Unlock()
	mr.setAvailable(3 << 30)

	e.guard(t)
	if n := mr.saveCount(); n != 2 {
		t.Fatalf("the first check made %d saves, want one for each VM", n)
	}
	e.guard(t)
	if n := mr.saveCount(); n != 2 {
		t.Fatalf("the guard tried a VM whose save just failed again: %d saves", n)
	}

	e.sched.mu.Lock()
	for _, entry := range e.sched.runs {
		entry.saveFailedAt = time.Time{}
	}
	e.sched.mu.Unlock()
	mr.saveMu.Lock()
	mr.saveErr = nil
	mr.saveMu.Unlock()
	// A 100 GiB disk keeps 5 GiB, and below 9 GiB free disk reclaim deletes
	// other sessions' saved memory; 9.5 GiB free has no room for 1 GiB.
	var free atomic.Uint64
	free.Store(19 << 29)
	setFreeBytes(e, &free)
	e.guard(t)
	if mr.isSaved(first) || mr.isSaved(second) || mr.saveCount() != 2 {
		t.Fatal("the guard saved a VM whose memory does not fit on disk")
	}
	free.Store(80 << 30)
	e.guard(t)
	if !mr.isSaved(first) && !mr.isSaved(second) {
		t.Fatal("the guard did not save a VM once its memory fit")
	}
}

// A working VM's saved memory holds its turn: disk reclaim leaves it, and
// a wake admission refuses for disk is tried again, not given up.
func TestAVMSavedWhileItWorkedSurvivesLowDiskAndWakesOnceThereIsRoom(t *testing.T) {
	t.Parallel()
	e, mr := newMemoryEnv(t)
	working := e.launchVM(t, mr, acpmock.PromptWait, 3<<30)
	run := e.runOf(t, working)
	mr.setAvailable(3 << 30)
	e.guard(t)
	if !mr.isSaved(working) || e.memorySaved(run) != 3<<30 {
		t.Fatal("the guard did not save the working VM")
	}

	var free atomic.Uint64
	free.Store(5 << 30)
	setFreeBytes(e, &free)
	e.sched.reclaimDisk(t.Context(), "")
	mr.setAvailable(2*memoryFloor(32<<30) + 4<<30)
	e.guard(t)
	if !mr.isSaved(working) || !e.asleep(run) || e.memorySaved(run) != 3<<30 {
		t.Fatalf("with the disk at its reserve: saved %v, asleep %v, mark %d; want the saved turn kept and marked",
			mr.isSaved(working), e.asleep(run), e.memorySaved(run))
	}
	free.Store(80 << 30)
	e.guard(t)
	if e.asleep(run) || e.memorySaved(run) != 0 {
		t.Fatal("the guard did not wake the VM once the disk had room")
	}
	if sess := e.sched.acp.session(run); sess == nil || !sess.State().TurnInFlight {
		t.Fatal("the woken session's turn did not carry on")
	}
}

func writeMemoryStall(t *testing.T, proc, full string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(proc, "pressure"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "some avg10=95.00 avg60=0.00 avg300=0.00 total=1\nfull avg10=" + full + " avg60=0.00 avg300=0.00 total=1\n"
	if err := os.WriteFile(filepath.Join(proc, "pressure", "memory"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Not parallel: it points procRoot at a fake procfs.
func TestOnlyALastingFullMemoryStallCounts(t *testing.T) {
	proc := t.TempDir()
	procRoot = proc
	t.Cleanup(func() { procRoot = isolatedProcRoot })
	s := &Scheduler{}
	now := time.Now()
	writeMemoryStall(t, proc, "70.00")
	if stall, lasting := s.memoryStall(now); lasting || stall != 70 {
		t.Fatalf("a stall that just started = %v, %v", stall, lasting)
	}
	if _, lasting := s.memoryStall(now.Add(memoryStallFor)); !lasting {
		t.Fatal("a stall that lasted did not count")
	}
	writeMemoryStall(t, proc, "59.00")
	if _, lasting := s.memoryStall(now.Add(memoryStallFor + time.Second)); lasting {
		t.Fatal("a stall below the limit counted")
	}
	writeMemoryStall(t, proc, "70.00")
	if _, lasting := s.memoryStall(now.Add(2 * memoryStallFor)); lasting {
		t.Fatal("a stall that broke off counted from its first start")
	}
}

// A lasting stall with twice the floor free saves no working VM, since it
// may come from outside the VMs. With less free it saves one, then waits:
// the guard wakes it only once the stall eased, not on the next check. Not
// parallel: it points procRoot at a fake procfs.
func TestALastingStallSavesOneVMAndWakesItOnlyOnceTheStallEases(t *testing.T) {
	proc := t.TempDir()
	procRoot = proc
	t.Cleanup(func() { procRoot = isolatedProcRoot })
	e, mr := newMemoryEnv(t)
	working := e.launchVM(t, mr, acpmock.PromptWait, 3<<30)
	run := e.runOf(t, working)
	mr.setAvailable(20 << 30)
	writeMemoryStall(t, proc, "80.00")
	e.sched.vmMemoryMu.Lock()
	e.sched.vmMemoryStallSince = time.Now().Add(-memoryStallFor)
	e.sched.vmMemoryMu.Unlock()

	e.guard(t)
	if mr.isSaved(working) {
		t.Fatal("a stall with twice the floor free saved a working VM")
	}
	mr.setAvailable(5 << 30)
	e.guard(t)
	if !mr.isSaved(working) || e.memorySaved(run) != 3<<30 {
		t.Fatal("a lasting stall short of twice the floor did not save the working VM")
	}
	for range 2 {
		e.guard(t)
		if !e.asleep(run) {
			t.Fatal("the guard woke the VM while the stall went on")
		}
	}
	writeMemoryStall(t, proc, "10.00")
	mr.setAvailable(20 << 30)
	e.guard(t)
	if e.asleep(run) {
		t.Fatal("the guard did not wake the VM once the stall eased")
	}
}
