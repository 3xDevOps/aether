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
		if !r.isSaved(id) {
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
	var mr *memoryRuntime
	e, _ := newACPEnv(t, func(cfg *Config) {
		sr := &saveRuntime{acpRuntime: cfg.Runtime.(*acpRuntime), saved: map[runtime.ID]string{}}
		mr = &memoryRuntime{saveRuntime: sr, available: 24 << 30, vm: map[runtime.ID]uint64{}}
		cfg.Runtime = mr
		cfg.filesystemCapacity = healthyAdmissionData
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

func TestTheMemoryGuardSavesTheLongestIdleVMFirst(t *testing.T) {
	t.Parallel()
	e, mr := newMemoryEnv(t)
	older := e.launchVM(t, mr, "say pong", 1<<30)
	newer := e.launchVM(t, mr, "say pong", 4<<30)
	sub := e.subscribe(t)
	mr.setAvailable(3 << 30)

	e.sched.guardVMMemory(t.Context())
	if !mr.isSaved(older) || mr.isSaved(newer) {
		t.Fatal("the guard did not save the longest idle VM first")
	}
	// 4 GiB available: above the floor, but short of the stop level.
	e.sched.guardVMMemory(t.Context())
	if !mr.isSaved(newer) {
		t.Fatal("the guard stopped saving before the stop level")
	}
	if ev := waitTimelineEvent(t, sub, e.runOf(t, older), events.TimelineNote); ev.Payload.(events.TimelinePayload).Message != memorySavedIdleNote {
		t.Fatalf("the save's note = %+v", ev.Payload)
	}
	e.sched.guardVMMemory(t.Context())
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

	e.sched.guardVMMemory(t.Context())
	if !mr.isSaved(big) || mr.isSaved(small) || e.memorySaved(bigRun) != 3<<30 {
		t.Fatalf("the guard did not save the working VM using the most memory, marked to wake: saved %v %v, mark %d",
			mr.isSaved(big), mr.isSaved(small), e.memorySaved(bigRun))
	}
	if sc, err := e.sched.readSidecar(bigRun); err != nil || sc.MemorySaved != 3<<30 {
		t.Fatalf("the mark was not recorded: %+v, %v", sc.MemorySaved, err)
	}
	e.sched.guardVMMemory(t.Context())
	if !mr.isSaved(small) {
		t.Fatal("the guard stopped saving before the stop level")
	}
	// 7 GiB available is above the stop level, but leaves room for neither.
	e.sched.guardVMMemory(t.Context())
	if !e.asleep(bigRun) || !e.asleep(smallRun) {
		t.Fatal("the guard woke a VM with no room for its memory")
	}

	// Room for the 1 GiB VM saved second, not for the one saved first.
	mr.setAvailable(2*memoryFloor(32<<30) + 1<<30)
	e.sched.guardVMMemory(t.Context())
	if e.asleep(smallRun) || !e.asleep(bigRun) {
		t.Fatal("the guard did not wake the VM whose memory fits, alone")
	}
	mr.setAvailable(2*memoryFloor(32<<30) + 3<<30)
	e.sched.guardVMMemory(t.Context())
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

func TestTheMemoryGuardSkipsSavesThatFailedOrWouldNotFitOnDisk(t *testing.T) {
	t.Parallel()
	e, mr := newMemoryEnv(t)
	first := e.launchVM(t, mr, "say pong", 1<<30)
	second := e.launchVM(t, mr, "say pong", 1<<30)
	mr.saveMu.Lock()
	mr.saveErr = errors.New("no space left on device")
	mr.saveMu.Unlock()
	mr.setAvailable(3 << 30)

	e.sched.guardVMMemory(t.Context())
	if n := mr.saveCount(); n != 2 {
		t.Fatalf("the first check made %d saves, want one for each VM", n)
	}
	e.sched.guardVMMemory(t.Context())
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
	// A 100 GiB disk keeps 5 GiB; 5.5 GiB free has no room for 1 GiB.
	var free atomic.Uint64
	free.Store(11 << 29)
	setFreeBytes(e, &free)
	e.sched.guardVMMemory(t.Context())
	if mr.isSaved(first) || mr.isSaved(second) || mr.saveCount() != 2 {
		t.Fatal("the guard saved a VM whose memory does not fit on disk")
	}
	free.Store(80 << 30)
	e.sched.guardVMMemory(t.Context())
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
	e.sched.guardVMMemory(t.Context())
	if !mr.isSaved(working) || e.memorySaved(run) != 3<<30 {
		t.Fatal("the guard did not save the working VM")
	}

	var free atomic.Uint64
	free.Store(5 << 30)
	setFreeBytes(e, &free)
	e.sched.reclaimDisk(t.Context(), "")
	mr.setAvailable(2*memoryFloor(32<<30) + 4<<30)
	e.sched.guardVMMemory(t.Context())
	if !mr.isSaved(working) || !e.asleep(run) || e.memorySaved(run) != 3<<30 {
		t.Fatalf("with the disk at its reserve: saved %v, asleep %v, mark %d; want the saved turn kept and marked",
			mr.isSaved(working), e.asleep(run), e.memorySaved(run))
	}
	free.Store(80 << 30)
	e.sched.guardVMMemory(t.Context())
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

	e.sched.guardVMMemory(t.Context())
	if mr.isSaved(working) {
		t.Fatal("a stall with twice the floor free saved a working VM")
	}
	mr.setAvailable(5 << 30)
	e.sched.guardVMMemory(t.Context())
	if !mr.isSaved(working) || e.memorySaved(run) != 3<<30 {
		t.Fatal("a lasting stall short of twice the floor did not save the working VM")
	}
	for range 2 {
		e.sched.guardVMMemory(t.Context())
		if !e.asleep(run) {
			t.Fatal("the guard woke the VM while the stall went on")
		}
	}
	writeMemoryStall(t, proc, "10.00")
	mr.setAvailable(20 << 30)
	e.sched.guardVMMemory(t.Context())
	if e.asleep(run) {
		t.Fatal("the guard did not wake the VM once the stall eased")
	}
}
