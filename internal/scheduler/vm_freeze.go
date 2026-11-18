package scheduler

import (
	"context"
	"log/slog"
	"slices"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/runtime"
)

const (
	// vmMemoryBrake is how often the brake reads the host's memory. Three
	// session VMs filling theirs at once took 1.55 GiB/s of a 29 GiB host,
	// whose floor is 2.9 GiB: the freeze lands with 2.1 GiB of it left.
	vmMemoryBrake = 500 * time.Millisecond
	// memoryFrozenNoteEvery keeps a session the brake freezes again and
	// again, as memory comes and goes, to one note.
	memoryFrozenNoteEvery = 5 * time.Minute
)

const memoryFrozenNote = "Paused: the server was running out of memory. It carries on by itself once memory is free again. If it has to be saved to disk for that, it wakes with every process where it was."

func (s *Scheduler) watchVMBrake(ctx context.Context) {
	vms, ok := s.cfg.Runtime.(runtime.FreezeRuntime)
	if !ok {
		return
	}
	// Nothing would thaw them while the server is down.
	defer s.thawVMs(vms)
	tick := time.NewTicker(vmMemoryBrake)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			s.brakeVMMemory(ctx, vms)
		}
	}
}

func (s *Scheduler) thawVMs(vms runtime.FreezeRuntime) {
	for _, id := range vms.Frozen() {
		if err := vms.Thaw(id); err != nil {
			slog.Warn("scheduler: memory guard: thaw a VM", "vm", id, "error", err)
		}
	}
}

// brakeVMMemory stops the VMs from taking more of the host's memory before
// the memory guard has saved any: a save takes tens of seconds, and VMs
// filling their memory at once take the host's last in under ten. Below
// admission's floor it freezes every running VM, which takes one write
// each and no memory, before it takes any lock. A frozen VM allocates
// nothing, so the guard's saves then free memory faster than it goes. At
// the floor or above it thaws one VM a check, the longest frozen first; one
// that takes the host below the floor again is frozen with the rest.
func (s *Scheduler) brakeVMMemory(ctx context.Context, vms runtime.FreezeRuntime) {
	available, floor, err := s.hostMemory()
	if err != nil {
		return
	}
	var froze []runtime.ID
	if available < floor {
		froze = vms.FreezeAll()
		if !s.vmMemoryBraked {
			slog.Warn("scheduler: memory guard: the host is short of memory; froze the VMs", "available", available, "floor", floor, "vms", len(froze))
		}
		s.vmMemoryBraked = true
	}
	if !s.vmMemoryBraked {
		return
	}
	saving := s.settleFrozen(ctx, vms, froze)
	if available < floor {
		return
	}
	frozen := slices.DeleteFunc(vms.Frozen(), func(id runtime.ID) bool { return slices.Contains(saving, id) })
	if len(frozen) == 0 && len(saving) == 0 {
		s.vmMemoryBraked = false
		slog.Info("scheduler: memory guard: every VM runs again", "available", available)
	}
	for _, id := range frozen {
		err := vms.Thaw(id)
		if err == nil {
			return
		}
		slog.Warn("scheduler: memory guard: thaw a VM", "vm", id, "error", err)
	}
}

// settleFrozen thaws the VM of every session the server is working on: one
// it launches, wakes, closes, kills, finishes or saves the ordinary way
// needs its guest to answer, and is frozen again once that is done. The
// VMs the guard saves frozen stay so, and are returned. It tells each other
// open session in froze why it paused.
func (s *Scheduler) settleFrozen(ctx context.Context, vms runtime.FreezeRuntime, froze []runtime.ID) (saving []runtime.ID) {
	now := time.Now()
	var answering []runtime.ID
	var noted []*supervised
	s.mu.Lock()
	for _, e := range s.runs {
		if e.containerID == "" {
			continue
		}
		busy := !e.lifecycleMu.TryLock()
		if !busy {
			e.lifecycleMu.Unlock()
		}
		switch {
		case e.frozenSave:
			saving = append(saving, e.containerID)
		case busy || e.waking != nil || e.status == domain.RunProvisioning || !e.retained && (e.status.Terminal() || e.killRequested || e.finalizing):
			answering = append(answering, e.containerID)
		case slices.Contains(froze, e.containerID) && !e.retained && !e.paused && now.Sub(e.frozenNotedAt) >= memoryFrozenNoteEvery:
			e.frozenNotedAt = now
			noted = append(noted, e)
		}
	}
	if len(noted) > 0 {
		s.wg.Add(1)
	}
	s.mu.Unlock()
	for _, id := range answering {
		if err := vms.Thaw(id); err != nil {
			slog.Warn("scheduler: memory guard: thaw the VM of a session the server is working on", "vm", id, "error", err)
		}
	}
	if len(noted) > 0 {
		// A slow event log must not hold up the next check.
		go func() {
			defer s.wg.Done()
			for _, e := range noted {
				s.publishTimeline(ctx, e.workspaceID, e.runID, "", events.TimelineNote, memoryFrozenNote)
			}
		}()
	}
	return saving
}

// vmFrozen reports whether cid's VM is frozen.
func (s *Scheduler) vmFrozen(cid runtime.ID) bool {
	vms, ok := s.cfg.Runtime.(runtime.FreezeRuntime)
	return ok && slices.Contains(vms.Frozen(), cid)
}

// freezeToSave freezes the VM of a run the guard is about to save, so it
// takes no more memory meanwhile and the save does not wait for its guest.
// frozenSave is set first: from then on the brake leaves the VM frozen. It
// reports false, with nothing frozen, for a runtime without a freezer or a
// VM the runtime is busy with. Caller holds entry.lifecycleMu.
func (s *Scheduler) freezeToSave(entry *supervised, cid runtime.ID) bool {
	vms, ok := s.cfg.Runtime.(runtime.FreezeRuntime)
	if !ok {
		return false
	}
	s.mu.Lock()
	entry.frozenSave = true
	s.mu.Unlock()
	if err := vms.Freeze(cid); err != nil {
		s.endFrozenSave(entry, cid)
		return false
	}
	return true
}

// endFrozenSave thaws the VM freezeToSave froze unless it was saved, which
// ended it; the brake freezes it again while the host is short of memory.
func (s *Scheduler) endFrozenSave(entry *supervised, cid runtime.ID) {
	s.mu.Lock()
	thaw := entry.frozenSave && !entry.asleepSaved
	entry.frozenSave = false
	s.mu.Unlock()
	vms, ok := s.cfg.Runtime.(runtime.FreezeRuntime)
	if !ok || !thaw {
		return
	}
	if err := vms.Thaw(cid); err != nil {
		slog.Warn("scheduler: memory guard: thaw a VM whose save failed", "run", entry.runID, "error", err)
	}
}
