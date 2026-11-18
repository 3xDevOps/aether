package scheduler

import (
	"context"
	"log/slog"
	"slices"
	"time"

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
	// A server that crashed left the VMs it froze frozen, and one that
	// stops leaves none behind: nothing would thaw them while it is down.
	s.thawVMs(vms)
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
// each and no memory. A frozen VM allocates nothing, so the guard's saves
// then free memory faster than it goes. At the floor or above it thaws one
// VM a check, the longest frozen first; one that takes the host below the
// floor again is frozen with the rest. It measures and freezes before it
// takes any lock, so nothing in the server can hold the freeze up.
func (s *Scheduler) brakeVMMemory(ctx context.Context, vms runtime.FreezeRuntime) {
	available, floor, err := s.hostMemory()
	if err != nil {
		return
	}
	if available >= floor {
		if !s.vmMemoryBraked {
			return
		}
		frozen := vms.Frozen()
		if len(frozen) == 0 {
			s.vmMemoryBraked = false
			slog.Info("scheduler: memory guard: every VM runs again", "available", available)
			return
		}
		if err := vms.Thaw(frozen[0]); err != nil {
			slog.Warn("scheduler: memory guard: thaw a VM", "vm", frozen[0], "error", err)
		}
		return
	}
	froze := vms.FreezeAll()
	if !s.vmMemoryBraked {
		slog.Warn("scheduler: memory guard: the host is short of memory; froze the VMs", "available", available, "floor", floor, "vms", len(froze))
	}
	s.vmMemoryBraked = true
	s.noteFrozen(ctx, vms, froze)
}

// noteFrozen tells each open session FreezeAll froze why it paused, and
// thaws the VM of a session that is ending: a kill or a finish frees its
// memory, and needs its guest to answer.
func (s *Scheduler) noteFrozen(ctx context.Context, vms runtime.FreezeRuntime, froze []runtime.ID) {
	now := time.Now()
	var ending []runtime.ID
	var noted []*supervised
	s.mu.Lock()
	for _, e := range s.runs {
		switch {
		case e.containerID == "":
		case !e.retained && (e.status.Terminal() || e.killRequested || e.finalizing):
			ending = append(ending, e.containerID)
		case slices.Contains(froze, e.containerID) && !e.retained && !e.paused && now.Sub(e.frozenNotedAt) >= memoryFrozenNoteEvery:
			e.frozenNotedAt = now
			noted = append(noted, e)
		}
	}
	s.mu.Unlock()
	for _, id := range ending {
		if err := vms.Thaw(id); err != nil {
			slog.Warn("scheduler: memory guard: thaw the VM of a session that is ending", "vm", id, "error", err)
		}
	}
	for _, e := range noted {
		s.publishTimeline(ctx, e.workspaceID, e.runID, "", events.TimelineNote, memoryFrozenNote)
	}
}

// vmFrozen reports whether the brake holds cid's VM frozen.
func (s *Scheduler) vmFrozen(cid runtime.ID) bool {
	vms, ok := s.cfg.Runtime.(runtime.FreezeRuntime)
	return ok && slices.Contains(vms.Frozen(), cid)
}
