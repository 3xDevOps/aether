package scheduler

import (
	"cmp"
	"context"
	"errors"
	"log/slog"
	"slices"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/runtime"
	"github.com/3xDevOps/Aether/internal/sysstats"
)

const (
	vmMemoryCheck = 5 * time.Second
	// memoryStallLimit and memoryStallFor are systemd-oomd's defaults: the
	// host's every task stalled on memory for 60% of ten seconds, for 30
	// seconds on end, is where it starts killing.
	memoryStallLimit = 60.0
	memoryStallFor   = 30 * time.Second
	// memorySaveRetry is how long a save that failed keeps its VM out of
	// the guard's reach, the VMs' default sleep delay.
	memorySaveRetry = 5 * time.Minute
)

const (
	memorySavedWorkingNote = "Saved to disk while it worked: the server was running out of memory. It wakes on its own once memory is free again, with every process where it was."
	memorySavedIdleNote    = "Saved to disk early: the server was running out of memory. It wakes on its next use, with every process where it was."
)

func (s *Scheduler) watchVMMemory(ctx context.Context) {
	if _, ok := s.cfg.Runtime.(runtime.SaveRuntime); !ok {
		return
	}
	tick := time.NewTicker(vmMemoryCheck)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			s.capacityGate <- struct{}{}
			s.claimedMemory(ctx)
			<-s.capacityGate
			s.guardVMMemory(ctx)
		}
	}
}

// guardVMMemory saves session VMs to disk before the host's OOM killer ends
// one, and never stops one. It starts once available memory falls below
// admission's floor, or the host has stalled on memory as systemd-oomd
// measures it, and stops once twice the floor is free and the stall has
// eased. Until then it saves one VM a check while less than twice the floor
// is free, and one more each time a stall lasts again. After it, it wakes
// the VMs it saved while they worked, one a check, each once its memory
// fits above twice the floor. The wake runs outside vmMemoryMu: its
// admission may save idle VMs.
func (s *Scheduler) guardVMMemory(ctx context.Context) {
	if room, calm := s.checkVMMemory(ctx); calm {
		s.wakeSavedForMemory(ctx, room)
	}
}

// checkVMMemory is guardVMMemory's measure and save. It reports the room
// above twice the floor once memory is back.
func (s *Scheduler) checkVMMemory(ctx context.Context) (uint64, bool) {
	s.vmMemoryMu.Lock()
	defer s.vmMemoryMu.Unlock()
	stall, lasting := s.memoryStall(time.Now())
	available, floor, err := s.hostMemory()
	if err != nil {
		slog.Warn("scheduler: memory guard: measure host memory", "error", err)
		return 0, false
	}
	calm := available >= 2*floor && stall < memoryStallLimit
	switch {
	case (available < floor || lasting) && !s.vmMemoryLow:
		slog.Warn("scheduler: memory guard: the host is short of memory; saving session VMs to disk", "available", available, "floor", floor, "stall", stall)
		s.vmMemoryLow = true
	case calm && s.vmMemoryLow:
		slog.Info("scheduler: memory guard: the host has memory again", "available", available)
		s.vmMemoryLow = false
	}
	if s.vmMemoryLow {
		// A save restarts the stall's clock: its average takes seconds to
		// fall, so only a stall that lasts again calls for another. A stall
		// with twice the floor free may come from outside the VMs, so it
		// saves only VMs that are not working.
		if (available < 2*floor || lasting) && s.saveForMemory(ctx, available < 2*floor) {
			s.vmMemoryStallSince = time.Time{}
		}
		return 0, false
	}
	if !calm {
		return 0, false
	}
	return available - 2*floor, true
}

// hostMemory is the host's available memory, read from /proc/meminfo with
// no daemon in the way, and admission's floor.
func (s *Scheduler) hostMemory() (available, floor uint64, err error) {
	read := s.cfg.hostMemory
	if read == nil {
		read = func() (uint64, uint64, error) { return sysstats.HostMemory(procRoot) }
	}
	total, available, err := read()
	if err == nil && (total == 0 || available > total) {
		err = errors.New("the host memory measurement is invalid")
	}
	return available, memoryFloor(total), err
}

// memoryStall is the host's memory stall now, as systemd-oomd reads it, and
// whether it has stayed at memoryStallLimit or above for memoryStallFor.
// Caller holds vmMemoryMu.
func (s *Scheduler) memoryStall(now time.Time) (float64, bool) {
	stall, err := sysstats.HostMemoryStall(procRoot)
	if err != nil {
		slog.Warn("scheduler: memory guard: read the host's memory pressure", "error", err)
	}
	if stall == nil || *stall < memoryStallLimit {
		s.vmMemoryStallSince = time.Time{}
		if stall == nil {
			return 0, false
		}
		return *stall, false
	}
	if s.vmMemoryStallSince.IsZero() {
		s.vmMemoryStallSince = now
	}
	return *stall, now.Sub(s.vmMemoryStallSince) >= memoryStallFor
}

// saveIdleVMs is admission's one pass when memory refused it, with need
// the bytes the admission must find above the floor. It saves session VMs
// nobody is using, the longest idle first, until twice the floor and need
// are free or none is left, and none when all of them together hold too
// little to admit it. It reports whether it saved any. It holds
// vmMemoryMu, so concurrent admissions and the guard each measure what the
// others' saves freed.
func (s *Scheduler) saveIdleVMs(ctx context.Context, need uint64) bool {
	if _, ok := s.cfg.Runtime.(runtime.SaveRuntime); !ok {
		return false
	}
	s.vmMemoryMu.Lock()
	defer s.vmMemoryMu.Unlock()
	candidates := s.memoryCandidates(ctx)
	var idle uint64
	for _, c := range candidates {
		if c.idle {
			idle += c.memory
		}
	}
	if available, floor, err := s.hostMemory(); err != nil || available+idle < floor+need {
		return false
	}
	saved := false
	for _, c := range candidates {
		if available, floor, err := s.hostMemory(); err != nil || available >= 2*floor+need || !c.idle || ctx.Err() != nil {
			break
		}
		if s.saveFits(c) && s.saveCandidate(ctx, c, false) {
			saved = true
		}
	}
	return saved
}

// memoryCandidate is an awake session VM a save under memory pressure may
// take. idle is false while its agent works, since is its last activity and
// memory is what it holds of the host's.
type memoryCandidate struct {
	e      *supervised
	cid    runtime.ID
	closed bool
	idle   bool
	since  time.Time
	memory uint64
}

func (c memoryCandidate) rank() int {
	switch {
	case c.closed:
		return 0
	case c.idle:
		return 1
	}
	return 2
}

// memoryCandidates lists the awake session VMs in the order a save takes
// them: closed ones, then open ones whose agent is idle, the longest idle
// first, then the rest, the one holding the most memory first. A run that
// must stay awake whatever the host is short of, a Background one, and one
// whose save failed within memorySaveRetry are left out.
func (s *Scheduler) memoryCandidates(ctx context.Context) []memoryCandidate {
	type open struct {
		e        *supervised
		cid      runtime.ID
		facts    idleFacts
		enhanced bool
	}
	memory := s.vmMemoryUse(ctx)
	now := time.Now().UTC()
	var out []memoryCandidate
	var opens []open
	s.mu.Lock()
	for _, e := range s.runs {
		enhanced := e.acp && e.launchMode == domain.LaunchACP
		_, saving := s.saver(e.containerID)
		if !saving || e.sleeping || !e.asleepSince.IsZero() || !enhanced && e.launchMode != domain.LaunchTUI || now.Sub(e.saveFailedAt) < memorySaveRetry {
			continue
		}
		switch {
		case e.retained && e.paused && !e.destroyPending && !e.evidencePending && e.retainedUntil != nil:
			out = append(out, memoryCandidate{e: e, cid: e.containerID, closed: true, idle: true, memory: memory[e.containerID]})
		case heldAwakeLocked(e) == "":
			opens = append(opens, open{e: e, cid: e.containerID, facts: s.liveFactsLocked(e), enhanced: enhanced})
		}
	}
	s.mu.Unlock()
	for _, o := range opens {
		if o.enhanced {
			sess := s.acp.session(o.e.runID)
			if sess == nil || len(sess.State().Pending) > 0 {
				continue
			}
		}
		since, idle := idleSince(s.sessionFacts(o.e.runID, o.enhanced, true, o.facts))
		out = append(out, memoryCandidate{e: o.e, cid: o.cid, idle: idle, since: since, memory: memory[o.cid]})
	}
	slices.SortStableFunc(out, func(a, b memoryCandidate) int {
		if a.rank() == 2 && b.rank() == 2 {
			return cmp.Compare(b.memory, a.memory)
		}
		return cmp.Or(cmp.Compare(a.rank(), b.rank()), a.since.Compare(b.since))
	})
	return out
}

// saveForMemory saves one awake session VM, the first memoryCandidates
// gives whose memory fits on the data filesystem above its reserve; with
// working, one whose agent works too. A guest still busy on its CPU, or a
// working agent, is marked to wake again once memory is back. It reports
// whether it saved one.
func (s *Scheduler) saveForMemory(ctx context.Context, working bool) bool {
	for _, c := range s.memoryCandidates(ctx) {
		if !working && !c.idle {
			break
		}
		if s.saveFits(c) && s.saveCandidate(ctx, c, working) {
			return true
		}
	}
	return false
}

// saveFits reports whether c's saved memory fits on the data filesystem,
// which holds the VMs' disks, above the reserve that keeps them writing. A
// VM whose memory is unknown does not: its save could write anything.
func (s *Scheduler) saveFits(c memoryCandidate) bool {
	if c.memory == 0 {
		return false
	}
	if s.cfg.MinFreeBytes < 0 {
		return true
	}
	data, err := s.dataFilesystem()
	if err != nil {
		slog.Warn("scheduler: memory guard: measure the data filesystem", "error", err)
		return false
	}
	if data.FreeBytes < diskReserve(data.TotalBytes, s.cfg.MinFreeBytes)+c.memory {
		slog.Warn("scheduler: memory guard: no room on disk to save a session VM", "run", c.e.runID, "memory", c.memory, "free", data.FreeBytes)
		return false
	}
	return true
}

// saveCandidate saves c unless its run is busy with another lifecycle change,
// such as a wake waiting on admission while it holds the lock. A run it
// saves while it works carries its wake mark, the memory it holds, in the
// sidecar before the save starts, so a server that stops mid-save still
// wakes it.
func (s *Scheduler) saveCandidate(ctx context.Context, c memoryCandidate, working bool) bool {
	if !c.e.lifecycleMu.TryLock() {
		return false
	}
	defer c.e.lifecycleMu.Unlock()
	if c.closed {
		return s.savedForMemory(ctx, c.e, s.sleepClosedLocked(ctx, c.e), "")
	}
	s.mu.Lock()
	cid := c.e.containerID
	saver, saving := s.saver(cid)
	if !saving || s.runs[c.e.runID] != c.e || c.e.sleeping || !c.e.asleepSince.IsZero() || heldAwakeLocked(c.e) != "" {
		s.mu.Unlock()
		return false
	}
	c.e.sleeping = true
	c.e.sleepDone = make(chan struct{})
	s.mu.Unlock()
	wake := !c.idle
	if c.idle {
		busy, err := s.guestBusy(ctx, cid)
		wake = busy || err != nil
	}
	if wake && !working {
		s.endSleep(c.e, false)
		return false
	}
	if err := s.markAsleep(c.e); err != nil {
		s.endSleep(c.e, false)
		slog.Warn("scheduler: memory guard: save a session VM", "run", c.e.runID, "error", err)
		return false
	}
	if !working {
		return s.savedForMemory(ctx, c.e, s.saveLive(ctx, c.e, saver), memorySavedIdleNote)
	}
	// A turn the agent started since the candidates were listed carries on
	// after the wake.
	note := memorySavedIdleNote
	if wake || s.agentWorking(c.e) {
		note = memorySavedWorkingNote
		s.mu.Lock()
		c.e.memorySaved = c.memory
		err := s.writeSidecar(c.e.sidecar())
		s.mu.Unlock()
		if err != nil {
			slog.Warn("scheduler: memory guard: record the wake of a session VM", "run", c.e.runID, "error", errors.Join(err, s.unmarkAsleep(c.e)))
			return false
		}
	}
	return s.savedForMemory(ctx, c.e, s.saveMarked(ctx, c.e, saver), note)
}

// savedForMemory logs a save under memory pressure and publishes its
// timeline note, if any.
func (s *Scheduler) savedForMemory(ctx context.Context, e *supervised, err error, note string) bool {
	s.mu.Lock()
	saved := e.asleepSaved && !e.asleepSince.IsZero()
	wakeAt := e.memorySaved
	s.mu.Unlock()
	if err != nil && !errors.Is(err, errStayAwake) {
		slog.Warn("scheduler: memory guard: save a session VM", "run", e.runID, "error", err)
	}
	if saved {
		slog.Info("scheduler: memory guard: saved a session VM", "run", e.runID, "wakes_at", wakeAt)
	}
	if saved && note != "" {
		s.publishTimeline(ctx, e.workspaceID, e.runID, "", events.TimelineNote, note)
	}
	return saved
}

// vmMemoryUse is the host memory each running VM holds, by container.
func (s *Scheduler) vmMemoryUse(ctx context.Context) map[runtime.ID]uint64 {
	source, ok := s.cfg.Runtime.(runtime.MemoryUseRuntime)
	if !ok {
		return nil
	}
	use, err := source.MemoryUse(ctx)
	if err != nil {
		slog.Warn("scheduler: memory guard: measure VM memory", "error", err)
	}
	return use
}

// wakeSavedForMemory wakes the sleeping session VM the guard saved while it
// worked that has slept longest of those whose memory fits in room. A wake
// that admission refuses, or that a server shutdown cuts off, keeps its
// mark for the next check. Any other failure drops it, so the guard never
// retries one forever, and says so on the timeline: the next use wakes it
// as any sleeping session.
func (s *Scheduler) wakeSavedForMemory(ctx context.Context, room uint64) {
	s.mu.Lock()
	var next *supervised
	for _, e := range s.runs {
		ending := e.retained || e.status.Terminal() || e.killRequested || e.finalizing || e.destroyPending
		if e.memorySaved > 0 && !e.asleepSince.IsZero() && !ending && e.memorySaved <= room && (next == nil || e.asleepSince.Before(next.asleepSince)) {
			next = e
		}
	}
	s.mu.Unlock()
	if next == nil {
		return
	}
	err := s.WakeRun(ctx, next.runID, "")
	switch {
	case err == nil:
		slog.Info("scheduler: memory guard: woke a session VM it saved", "run", next.runID)
	case ctx.Err() != nil || errors.Is(err, ErrMemoryPressure) || errors.Is(err, ErrDiskFull) || errors.Is(err, ErrCapacityUnknown):
		slog.Info("scheduler: memory guard: the wake of a session VM it saved did not go ahead; trying again next check", "run", next.runID, "error", err)
	default:
		slog.Warn("scheduler: memory guard: wake a session VM it saved", "run", next.runID, "error", err)
		s.mu.Lock()
		next.memorySaved = 0
		if s.runs[next.runID] == next {
			if werr := s.writeSidecar(next.sidecar()); werr != nil {
				slog.Warn("scheduler: memory guard: forget a session VM it saved", "run", next.runID, "error", werr)
			}
		}
		s.mu.Unlock()
		s.publishTimeline(ctx, next.workspaceID, next.runID, "", events.TimelineNote, "Could not wake on its own once memory was free again: "+err.Error()+". It wakes on its next use.")
	}
}
