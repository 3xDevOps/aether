package scheduler

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/disk"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/runtime"
)

// diskRuntime reports the disk use the test sets for each container.
type diskRuntime struct {
	*acpRuntime
	mu  sync.Mutex
	use map[runtime.ID]int64
}

func (r *diskRuntime) DiskUse(context.Context) (map[runtime.ID]int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	use := make(map[runtime.ID]int64, len(r.use))
	for id, n := range r.use {
		use[id] = n
	}
	return use, nil
}

func TestTheDiskGuardPausesTheSessionWhoseDisksGrewMostWhileTheDiskIsLow(t *testing.T) {
	t.Parallel()
	var dr *diskRuntime
	var mu sync.Mutex
	free := uint64(50 << 30)
	e, _ := newACPEnv(t, func(cfg *Config) {
		dr = &diskRuntime{acpRuntime: cfg.Runtime.(*acpRuntime), use: map[runtime.ID]int64{}}
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
	sub := e.subscribe(t)
	check := func(freeBytes uint64, grow map[domain.RunID]int64) {
		t.Helper()
		mu.Lock()
		free = freeBytes
		mu.Unlock()
		dr.mu.Lock()
		for _, run := range runs {
			dr.use[e.cid(t, run)] += grow[run]
		}
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
	a, b, c := runs[0], runs[1], runs[2]

	check(50<<30, nil)
	check(5<<30, map[domain.RunID]int64{a: 3 << 30, b: 1 << 30})
	if got := paused(); !got[0] || got[1] || got[2] {
		t.Fatalf("below the floor, paused = %v; want only the session that grew the most", got)
	}
	if ev := waitTimelineEvent(t, sub, a, events.TimelineNote); ev.Payload.(events.TimelinePayload).Message != diskHeldNote {
		t.Errorf("the paused session's note = %+v", ev.Payload)
	}
	check(5<<30, map[domain.RunID]int64{b: 1 << 30})
	if got := paused(); !got[0] || !got[1] || got[2] {
		t.Fatalf("still below the floor, paused = %v; want the next that grew, and never the one that did not", got)
	}
	check(5<<30, nil)
	check(12<<30, nil)
	if got := paused(); !got[0] || !got[1] || got[2] {
		t.Fatalf("above the floor but within reclaim's headroom, paused = %v; want both still paused", got)
	}

	if err := e.sched.Pause(t.Context(), c, e.member.ID); err != nil {
		t.Fatal(err)
	}
	check(20<<30, nil)
	if got := paused(); got[0] || got[1] || !got[2] {
		t.Fatalf("with space back, paused = %v; want the guard's pauses ended and the member's kept", got)
	}
	released := map[domain.RunID]bool{}
	for len(released) < 2 {
		select {
		case ev := <-sub.Events():
			if p, ok := ev.Payload.(events.TimelinePayload); ok && p.Kind == events.TimelineNote && p.Message == diskReleasedNote {
				released[ev.RunID] = true
			}
		case <-time.After(waitTimeout):
			t.Fatalf("resume notes for %v, want both sessions the guard paused", released)
		}
	}
	if released[c] {
		t.Error("the guard resumed a session the member paused")
	}
}
