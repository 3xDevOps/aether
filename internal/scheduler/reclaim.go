package scheduler

import (
	"context"
	"log/slog"
	"math"
	"sort"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
)

// reclaimHeadroom is how far above the free-disk floor reclaim keeps free
// space, so admission rarely meets a refusal.
const reclaimHeadroom uint64 = 4 << 30

// reclaimDisk waits for any running pass, then runs one. keep names a member
// whose kept terminal is being opened, which the pass must not remove.
func (s *Scheduler) reclaimDisk(ctx context.Context, keep domain.MemberID) {
	s.reclaimMu.Lock()
	defer s.reclaimMu.Unlock()
	s.reclaimDiskLocked(ctx, keep)
}

// reclaimDiskLocked frees rebuildable data until every measured filesystem has
// the floor plus reclaimHeadroom free, evicting the data that is cheapest to
// rebuild last, and stops as soon as it does. The caller holds reclaimMu.
func (s *Scheduler) reclaimDiskLocked(ctx context.Context, keep domain.MemberID) {
	if s.cfg.MinFreeBytes < 0 || ctx.Err() != nil {
		return
	}
	free, short, ok := s.reclaimLevel(ctx)
	if !ok || !short {
		return
	}
	// recovered re-measures after a step and logs the change.
	recovered := func(step string) bool {
		after, short, ok := s.reclaimLevel(ctx)
		slog.Info("scheduler: disk reclaim", "step", step, "free_before", free, "free_after", after)
		free = after
		return !ok || !short || ctx.Err() != nil
	}

	for _, entry := range s.closedRetainedByAge() {
		removed, err := s.reclaimRetained(ctx, entry)
		if err != nil {
			slog.Warn("scheduler: disk reclaim: remove retained container", "run", entry.runID, "error", err)
			continue
		}
		if !removed {
			continue
		}
		s.publishTimeline(ctx, entry.workspaceID, entry.runID, "", events.TimelineNote,
			"Removed this closed run's container early because disk space was low; the run can no longer be reopened, and its branch and history are kept")
		if recovered("closed run container") {
			return
		}
	}

	// A negative --checkout-ttl means the operator keeps checkouts.
	if s.cfg.CheckoutTTL > 0 {
		s.sweepCheckouts(ctx, time.Now().UTC())
		if recovered("finished checkouts") {
			return
		}
	}

	if members, err := s.cfg.Store.ListMembers(ctx); err != nil {
		slog.Warn("scheduler: disk reclaim: list members", "error", err)
	} else {
		for _, member := range members {
			if member.ID == keep {
				continue
			}
			destroyed, err := s.DestroyStoppedTerminal(ctx, member.ID)
			if err != nil {
				slog.Warn("scheduler: disk reclaim: destroy stopped terminal", "member", member.ID, "error", err)
				continue
			}
			if destroyed && recovered("stopped terminal") {
				return
			}
		}
	}

	s.sweepCaches(ctx, true)
	if recovered("caches and images") {
		return
	}

	s.sweepScratch(ctx)
	recovered("scratch")
}

// reclaimLevel reports the lowest free bytes among the filesystems that are
// below their floor plus reclaimHeadroom, or among all when none is. ok is
// false when a measurement failed.
func (s *Scheduler) reclaimLevel(ctx context.Context) (free uint64, short, ok bool) {
	probeCtx, cancel := context.WithTimeout(ctx, capacityProbeTimeout)
	defer cancel()
	filesystems, _, err := s.measureCapacity(probeCtx)
	if err != nil {
		slog.Warn("scheduler: disk reclaim: measure filesystems", "error", err)
		return 0, false, false
	}
	free = math.MaxUint64
	lowShort := uint64(math.MaxUint64)
	for _, fs := range filesystems {
		free = min(free, fs.FreeBytes)
		target := diskReserve(fs.TotalBytes, s.cfg.MinFreeBytes) + reclaimHeadroom
		// A target the filesystem cannot hold is never met, so evicting
		// toward it would only destroy reopenable runs and unsaved state.
		if target < fs.TotalBytes && fs.FreeBytes < target {
			short = true
			lowShort = min(lowShort, fs.FreeBytes)
		}
	}
	if short {
		free = lowShort
	}
	return free, short, true
}

// closedRetainedByAge returns closed runs that still hold a paused container,
// the one expiring soonest first.
func (s *Scheduler) closedRetainedByAge() []*supervised {
	s.mu.Lock()
	defer s.mu.Unlock()
	var entries []*supervised
	for _, entry := range s.runs {
		if entry.retained && entry.status.Terminal() {
			entries = append(entries, entry)
		}
	}
	until := func(e *supervised) time.Time {
		if e.retainedUntil == nil {
			return time.Unix(math.MaxInt32, 0)
		}
		return *e.retainedUntil
	}
	sort.Slice(entries, func(i, j int) bool { return until(entries[i]).Before(until(entries[j])) })
	return entries
}

// reclaimRetained expires entry's container only if it is still a closed
// run's retained container, checked under the lifecycle lock that reopen
// also holds, and reports whether it did.
func (s *Scheduler) reclaimRetained(ctx context.Context, entry *supervised) (bool, error) {
	entry.lifecycleMu.Lock()
	defer entry.lifecycleMu.Unlock()
	s.mu.Lock()
	closed := s.runs[entry.runID] == entry && entry.retained && entry.status.Terminal()
	entry.reclaimed = closed
	s.mu.Unlock()
	if !closed {
		return false, nil
	}
	err := s.expireRetainedLocked(ctx, entry, "")
	if err == nil {
		return true, nil
	}
	s.mu.Lock()
	retry := s.runs[entry.runID] == entry && entry.retained && entry.destroyPending
	if !retry {
		entry.reclaimed = false
	}
	s.mu.Unlock()
	if retry {
		s.startRetainedWaitAfterDestroyFailure(ctx, entry)
	}
	return false, err
}
