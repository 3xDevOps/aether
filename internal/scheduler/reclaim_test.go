package scheduler

import (
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/disk"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
)

func setFreeBytes(e *testEnv, free *atomic.Uint64) {
	e.sched.cfg.filesystemCapacity = func(string) (disk.Usage, error) {
		return disk.Usage{TotalBytes: 100 << 30, FreeBytes: free.Load()}, nil
	}
}

func closeRetained(t *testing.T, e *testEnv, task string) *domain.Run {
	t.Helper()
	run, _ := e.launchFake(t, task)
	if err := e.sched.CloseRun(t.Context(), run.ID, e.member.ID, domain.RunMerged); err != nil {
		t.Fatal(err)
	}
	e.waitStoreStatus(t, run.ID, domain.RunMerged)
	return run
}

func TestReclaimDiskLeavesDataAboveTarget(t *testing.T) {
	e := newTestEnv(t, func(cfg *Config) { cfg.RunContainerTTL = time.Hour })
	run := closeRetained(t, e, "healthy host")
	var free atomic.Uint64
	free.Store(80 << 30)
	setFreeBytes(e, &free)
	cache := writeCacheFixture(t, e, e.member.ID, "runs", 4096)
	e.sched.reclaimDisk(t.Context(), "")
	if e.rt.byName(string(run.ID)) == nil {
		t.Fatal("retained container removed while disk was healthy")
	}
	requireCacheExists(t, cache, true)
}

func TestReclaimDiskIgnoresAFloorLargerThanTheDisk(t *testing.T) {
	e := newTestEnv(t, func(cfg *Config) { cfg.RunContainerTTL = time.Hour })
	run := closeRetained(t, e, "unreachable floor")
	e.sched.cfg.MinFreeBytes = 200 << 30
	var free atomic.Uint64
	free.Store(1 << 30)
	setFreeBytes(e, &free)
	e.sched.reclaimDisk(t.Context(), "")
	if e.rt.byName(string(run.ID)) == nil {
		t.Fatal("retained container removed toward a floor larger than the disk")
	}
}

func TestReclaimDiskRemovesClosedRunContainerFirstAndStopsWhenRecovered(t *testing.T) {
	e := newTestEnv(t, func(cfg *Config) { cfg.RunContainerTTL = time.Hour })
	run := closeRetained(t, e, "closed run")
	var free atomic.Uint64
	free.Store(1 << 30)
	e.sched.cfg.filesystemCapacity = func(string) (disk.Usage, error) {
		if e.rt.byName(string(run.ID)) == nil {
			free.Store(80 << 30)
		}
		return disk.Usage{TotalBytes: 100 << 30, FreeBytes: free.Load()}, nil
	}
	cache := writeCacheFixture(t, e, e.member.ID, "runs", 4096)
	e.sched.reclaimDisk(t.Context(), "")
	if e.rt.byName(string(run.ID)) != nil {
		t.Fatal("retained container of the closed run survived reclaim")
	}
	requireCacheExists(t, cache, true)
}

func TestReclaimDiskSkipsRunNoLongerRetained(t *testing.T) {
	e := newTestEnv(t, func(cfg *Config) { cfg.RunContainerTTL = time.Hour })
	run := closeRetained(t, e, "reopened after snapshot")
	sub := e.subscribe(t)
	var free atomic.Uint64
	free.Store(1 << 30)
	setFreeBytes(e, &free)
	e.sched.mu.Lock()
	e.sched.runs[run.ID].retained = false
	e.sched.mu.Unlock()
	e.sched.reclaimDiskLocked(t.Context(), "")
	if e.rt.byName(string(run.ID)) == nil {
		t.Fatal("container of a run that is no longer retained was removed")
	}
	for {
		select {
		case ev := <-sub.Events():
			if p, ok := ev.Payload.(events.TimelinePayload); ok && ev.RunID == run.ID && strings.Contains(p.Message, "Removed this closed run's container") {
				t.Fatalf("posted removal note: %q", p.Message)
			}
		default:
			return
		}
	}
}

func TestReclaimDiskRemovesLegacyCachesOnlyUnderPressureWhileRunIsOpen(t *testing.T) {
	e := newTestEnv(t, nil)
	e.sched.cfg.Runtime = &cacheImageRuntime{fakeRuntime: e.rt}
	e.launchFake(t, "open run")
	home, err := e.sched.cfg.Homes.Path(e.member.ID)
	if err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(home, ".npm", "_cacache")
	if err = os.MkdirAll(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	e.sched.sweepCaches(t.Context(), false)
	requireCacheExists(t, legacy, true)

	var free atomic.Uint64
	free.Store(1 << 30)
	setFreeBytes(e, &free)
	e.sched.reclaimDisk(t.Context(), "")
	requireCacheExists(t, legacy, false)
}
