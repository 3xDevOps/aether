package scheduler

import (
	"context"
	"errors"
	"maps"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/disk"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/runtime"
)

// diskRuntime reports the disk use the test sets for each container and
// records the VMs the guard stopped.
type diskRuntime struct {
	*acpRuntime
	mu      sync.Mutex
	use     map[runtime.ID]int64
	held    map[runtime.ID]bool
	failUse atomic.Bool
}

func (r *diskRuntime) DiskUse(context.Context) (map[runtime.ID]int64, error) {
	if r.failUse.Load() {
		return nil, errors.New("stat: input/output error")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return maps.Clone(r.use), nil
}

func (r *diskRuntime) HoldDisks(_ context.Context, id runtime.ID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.held[id] = true
	return nil
}

func TestTheDiskGuardPausesTheSessionsFillingALowDisk(t *testing.T) {
	t.Parallel()
	var dr *diskRuntime
	var mu sync.Mutex
	free := uint64(50 << 30)
	e, _ := newACPEnv(t, func(cfg *Config) {
		dr = &diskRuntime{acpRuntime: cfg.Runtime.(*acpRuntime), use: map[runtime.ID]int64{}, held: map[runtime.ID]bool{}}
		cfg.Runtime = dr
		cfg.MinFreeBytes = 10 << 30
		cfg.filesystemCapacity = func(string) (disk.Usage, error) {
			mu.Lock()
			defer mu.Unlock()
			return disk.Usage{TotalBytes: 100 << 30, FreeBytes: free}, nil
		}
	})
	runs := []domain.RunID{e.launchACP(t, "say pong").ID, e.launchACP(t, "say pong").ID, e.launchACP(t, "say pong").ID}
	for _, run := range runs {
		waitItems(t, e.sched, run, "the turn", turnEnded("end_turn", 1))
	}
	a, b, c := runs[0], runs[1], runs[2]
	const terminal = runtime.ID("vm-terminal-m1")
	sub := e.subscribe(t)
	check := func(freeBytes uint64, grow map[domain.RunID]int64, terminalGrows int64) {
		t.Helper()
		mu.Lock()
		free = freeBytes
		mu.Unlock()
		dr.mu.Lock()
		for _, run := range runs {
			dr.use[e.cid(t, run)] += grow[run]
		}
		dr.use[terminal] += terminalGrows
		dr.mu.Unlock()
		e.sched.guardVMDisks(t.Context())
	}
	paused := func() []bool {
		var out []bool
		for _, run := range runs {
			out = append(out, e.sched.Paused(run))
		}
		return out
	}
	notes := func(want string, n int) map[domain.RunID]bool {
		t.Helper()
		got := map[domain.RunID]bool{}
		for len(got) < n {
			select {
			case ev := <-sub.Events():
				if p, ok := ev.Payload.(events.TimelinePayload); ok && p.Kind == events.TimelineNote && p.Message == want {
					got[ev.RunID] = true
				}
			case <-time.After(waitTimeout):
				t.Fatalf("notes %q for %v, want %d sessions", want, got, n)
			}
		}
		return got
	}

	check(50<<30, nil, 0)
	check(5<<30, nil, 0) // the first check below the floor takes the baseline
	check(5<<30, map[domain.RunID]int64{c: 4 << 10}, 20<<30)
	if got := paused(); slices.Contains(got, true) {
		t.Fatalf("a growing terminal and a 4 KiB session below the floor paused %v; want no session paused", got)
	}
	check(5<<30, map[domain.RunID]int64{a: 3 << 30, b: 1 << 30}, 0)
	if got := paused(); !got[0] || !got[1] || got[2] {
		t.Fatalf("below the floor, paused = %v; want both sessions that grew, not the one that grew 4 KiB", got)
	}
	if got := notes(diskHeldNote, 2); !got[a] || !got[b] {
		t.Errorf("hold notes for %v", got)
	}
	dr.mu.Lock()
	stopped := dr.held[e.cid(t, a)] && dr.held[e.cid(t, b)] && !dr.held[e.cid(t, c)]
	dr.mu.Unlock()
	if !stopped {
		t.Errorf("held VMs %v; want a's and b's whole VMs stopped", dr.held)
	}
	check(12<<30, nil, 0)
	if got := paused(); !got[0] || !got[1] {
		t.Fatalf("above the floor but within reclaim's headroom, paused = %v; want both still paused", got)
	}

	// The member resumes b by hand and pauses it again: that pause is theirs.
	if err := e.sched.Resume(t.Context(), b, e.member.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.sched.Pause(t.Context(), b, e.member.ID); err != nil {
		t.Fatal(err)
	}
	check(20<<30, nil, 0)
	if got := paused(); got[0] || !got[1] {
		t.Fatalf("with space back, paused = %v; want the guard's pause ended and the member's kept", got)
	}
	if got := notes(diskReleasedNote, 1); !got[a] {
		t.Errorf("resume notes for %v", got)
	}

	// Filling the disk again soon after the guard resumed it leaves a to
	// its member.
	check(5<<30, nil, 0) // baseline again
	check(5<<30, map[domain.RunID]int64{a: 1 << 30}, 0)
	notes(diskReheldNote, 1)
	check(20<<30, nil, 0)
	if !e.sched.Paused(a) {
		t.Fatal("a session held again soon after its resume was resumed by the guard")
	}
}

func TestTheDiskGuardCatchesASlowWriterOverSeveralChecks(t *testing.T) {
	t.Parallel()
	var dr *diskRuntime
	var mu sync.Mutex
	free := uint64(50 << 30)
	e, _ := newACPEnv(t, func(cfg *Config) {
		dr = &diskRuntime{acpRuntime: cfg.Runtime.(*acpRuntime), use: map[runtime.ID]int64{}, held: map[runtime.ID]bool{}}
		cfg.Runtime = dr
		cfg.MinFreeBytes = 10 << 30
		cfg.filesystemCapacity = func(string) (disk.Usage, error) {
			mu.Lock()
			defer mu.Unlock()
			return disk.Usage{TotalBytes: 100 << 30, FreeBytes: free}, nil
		}
	})
	run := e.launchACP(t, "say pong").ID
	waitItems(t, e.sched, run, "the turn", turnEnded("end_turn", 1))
	cid := e.cid(t, run)
	grow := func(freeBytes uint64, by int64) {
		mu.Lock()
		free = freeBytes
		mu.Unlock()
		dr.mu.Lock()
		dr.use[cid] += by
		dr.mu.Unlock()
		e.sched.guardVMDisks(t.Context())
	}
	grow(50<<30, 0)
	// Below the floor, 30 MiB a check: under the 64 MiB one-check threshold,
	// but the guard measures against the baseline it took on entry, so it
	// pauses the session once its total growth reaches 64 MiB.
	grow(5<<30, 0) // baseline
	grow(5<<30, 30<<20)
	grow(5<<30, 30<<20)
	if e.sched.Paused(run) {
		t.Fatalf("a session paused after growing only 60 MiB since the baseline")
	}
	grow(5<<30, 30<<20)
	if !e.sched.Paused(run) {
		t.Fatal("a session that grew 90 MiB since the baseline was never paused")
	}
}

func TestTheDiskGuardResumesEvenWhenItCannotMeasureVMDisks(t *testing.T) {
	t.Parallel()
	var dr *diskRuntime
	var mu sync.Mutex
	free := uint64(50 << 30)
	e, _ := newACPEnv(t, func(cfg *Config) {
		dr = &diskRuntime{acpRuntime: cfg.Runtime.(*acpRuntime), use: map[runtime.ID]int64{}, held: map[runtime.ID]bool{}}
		cfg.Runtime = dr
		cfg.MinFreeBytes = 10 << 30
		cfg.filesystemCapacity = func(string) (disk.Usage, error) {
			mu.Lock()
			defer mu.Unlock()
			return disk.Usage{TotalBytes: 100 << 30, FreeBytes: free}, nil
		}
	})
	run := e.launchACP(t, "say pong").ID
	waitItems(t, e.sched, run, "the turn", turnEnded("end_turn", 1))
	dr.mu.Lock()
	dr.use[e.cid(t, run)] = 0
	dr.mu.Unlock()
	e.sched.guardVMDisks(t.Context())
	mu.Lock()
	free = 5 << 30
	mu.Unlock()
	e.sched.guardVMDisks(t.Context()) // enters low, takes the baseline
	dr.mu.Lock()
	dr.use[e.cid(t, run)] += 1 << 30
	dr.mu.Unlock()
	e.sched.guardVMDisks(t.Context())
	if !e.sched.Paused(run) {
		t.Fatal("a session filling a low disk was not paused")
	}
	mu.Lock()
	free = 20 << 30
	mu.Unlock()
	dr.mu.Lock()
	dr.use = nil
	dr.mu.Unlock()
	dr.failUse.Store(true)
	e.sched.guardVMDisks(t.Context())
	if e.sched.Paused(run) {
		t.Fatal("with space back and VM disks unmeasurable the guard kept its pause")
	}
}
