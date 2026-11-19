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

// reclaimStartUsed and reclaimTargetUsed are the percentages of a filesystem
// in use at which reclaim starts and down to which it frees what nobody
// loses, at most every reclaimSweepEvery.
const (
	reclaimStartUsed  = 85
	reclaimTargetUsed = 80
	reclaimSweepEvery = 5 * time.Minute
)

// reclaimDisk waits for any running pass, then runs one. keep names a member
// whose kept terminal is being opened, which the pass must not remove.
func (s *Scheduler) reclaimDisk(ctx context.Context, keep domain.MemberID) {
	s.reclaimMu.Lock()
	defer s.reclaimMu.Unlock()
	s.reclaimDiskLocked(ctx, keep)
}

// reclaimDiskLocked starts once a measured filesystem is reclaimStartUsed
// full or below its floor plus reclaimHeadroom. It first frees what nobody
// loses, unused images, caches past their budget or age and scratch, until
// every filesystem is down to reclaimTargetUsed. Below the floor's headroom
// it then frees what sessions and members would lose, and every cache,
// until that headroom is back. It stops as soon as it is. The caller holds
// reclaimMu.
func (s *Scheduler) reclaimDiskLocked(ctx context.Context, keep domain.MemberID) {
	if s.cfg.MinFreeBytes < 0 || ctx.Err() != nil {
		return
	}
	free, short, ok := s.reclaimLevel(ctx, reclaimStartUsed)
	if !ok || !short {
		return
	}
	// recovered re-measures after a step against used and logs the change.
	recovered := func(step string, used uint64) bool {
		after, short, ok := s.reclaimLevel(ctx, used)
		slog.Info("scheduler: disk reclaim", "step", step, "free_before", free, "free_after", after)
		free = after
		return !ok || !short || ctx.Err() != nil
	}
	if now := time.Now(); now.Sub(s.reclaimSweptAt) >= reclaimSweepEvery {
		s.reclaimSweptAt = now
		s.sweepCaches(ctx, false)
		s.sweepScratch(ctx)
		if recovered("unused images, old caches and scratch", reclaimTargetUsed) {
			return
		}
	}
	// A used share of 100 leaves only the floor plus reclaimHeadroom.
	if _, low, ok := s.reclaimLevel(ctx, 100); !ok || !low {
		return
	}
	floor := func(step string) bool { return recovered(step, 100) }
	if s.reclaimSessions(ctx, keep, floor) {
		return
	}
	s.sweepCaches(ctx, true)
	if floor("caches and images") {
		return
	}
	s.sweepScratch(ctx)
	floor("scratch")
}

// reclaimSessions frees, in order, closed sessions' containers, sleeping
// VMs' saved memory, finished checkouts and members' stopped terminals, and
// reports whether recovered said that was enough. A closed session's work is
// on its branch, so its container goes before anything an open one holds.
func (s *Scheduler) reclaimSessions(ctx context.Context, keep domain.MemberID, recovered func(step string) bool) bool {
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
			"Removed this closed session's container early because disk space was low; the session can no longer be reopened, and its branch and history are kept")
		if recovered("closed session container") {
			return true
		}
	}

	for _, entry := range s.savedByAge() {
		note, err := s.dropSavedState(ctx, entry)
		if err != nil {
			slog.Warn("scheduler: disk reclaim: drop a saved session state", "run", entry.runID, "error", err)
			continue
		}
		if note == "" {
			continue
		}
		s.publishTimeline(ctx, entry.workspaceID, entry.runID, "", events.TimelineNote, note)
		if recovered("saved session state") {
			return true
		}
	}

	// A negative --checkout-ttl means the operator keeps checkouts.
	if s.cfg.CheckoutTTL > 0 {
		s.sweepCheckouts(ctx, time.Now().UTC())
		if recovered("finished checkouts") {
			return true
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
				return true
			}
		}
	}
	return false
}

// reclaimLevel reports the lowest free bytes among the filesystems more than
// used percent full or below their floor plus reclaimHeadroom, or among all
// when none is. ok is false when a measurement failed.
func (s *Scheduler) reclaimLevel(ctx context.Context, used uint64) (free uint64, short, ok bool) {
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
		target := fs.TotalBytes / 100 * (100 - used)
		// A floor the filesystem cannot hold is never met, so evicting
		// toward it would only destroy reopenable runs and unsaved state.
		if floor := diskReserve(fs.TotalBytes, s.cfg.MinFreeBytes) + reclaimHeadroom; floor < fs.TotalBytes {
			target = max(target, floor)
		}
		if fs.FreeBytes < target {
			short = true
			lowShort = min(lowShort, fs.FreeBytes)
		}
	}
	if short {
		free = lowShort
	}
	return free, short, true
}

// savedByAge returns the runs asleep in a saved VM, the longest asleep
// first. One the memory guard saved while it worked is left out: its saved
// memory holds work in flight, not an idle session.
func (s *Scheduler) savedByAge() []*supervised {
	s.mu.Lock()
	defer s.mu.Unlock()
	var entries []*supervised
	for _, entry := range s.runs {
		if _, saving := s.saver(entry.containerID); saving && entry.asleepSaved && !entry.sleeping && entry.memorySaved == 0 {
			entries = append(entries, entry)
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].asleepSince.Before(entries[j].asleepSince) })
	return entries
}

// dropSavedState stops entry's saved VM, which deletes its memory and keeps
// its disks, if it is still asleep and can start again from its disk, and
// returns the timeline note that says so. A session that cannot, such as a
// Standard one, keeps its state: without it the session could never wake.
// A run being woken is left, and so is one whose lifecycle lock is held: a
// wake holds it while its capacity reservation waits for this reclaim.
func (s *Scheduler) dropSavedState(ctx context.Context, entry *supervised) (string, error) {
	if !entry.lifecycleMu.TryLock() {
		return "", nil
	}
	defer entry.lifecycleMu.Unlock()
	s.mu.Lock()
	asleep := s.runs[entry.runID] == entry && !entry.asleepSince.IsZero() && entry.asleepSaved && entry.waking == nil
	cid := entry.containerID
	s.mu.Unlock()
	if !asleep {
		return "", nil
	}
	if refusal, err := s.coldStartRefusal(ctx, entry); refusal != "" || err != nil {
		return "", nil
	}
	if err := s.cfg.Runtime.Stop(ctx, cid, s.cfg.StopGrace); err != nil {
		return "", err
	}
	if err := s.recordSaved(entry, false); err != nil {
		return "", err
	}
	s.publishSleep(entry)
	return "Deleted this sleeping session's saved memory because disk space was low; it starts again from its disk when it next wakes, and restores its conversation", nil
}

// closedRetainedByAge returns closed runs that still hold a container and
// are not being relaunched, the one expiring soonest first.
func (s *Scheduler) closedRetainedByAge() []*supervised {
	s.mu.Lock()
	defer s.mu.Unlock()
	var entries []*supervised
	for _, entry := range s.runs {
		if entry.retained && entry.status.Terminal() && entry.relaunching == 0 {
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
	closed := s.runs[entry.runID] == entry && entry.retained && entry.status.Terminal() && entry.relaunching == 0
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
