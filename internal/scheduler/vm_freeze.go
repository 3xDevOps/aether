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

// watchVMBrake runs from before recovery, so the VMs a server left frozen
// stay so while this one takes them up. A server that stops with the host
// still short of memory leaves them frozen for the next: thawed, they would
// run with nothing watching them.
func (s *Scheduler) watchVMBrake(ctx context.Context) {
	vms, ok := s.cfg.Runtime.(runtime.FreezeRuntime)
	if !ok {
		return
	}
	s.vmMemoryBraked = len(vms.Frozen()) > 0
	defer func() {
		if available, floor, err := s.hostMemory(); err == nil && available < floor {
			return
		}
		for _, id := range vms.Frozen() {
			if err := vms.Thaw(id); err != nil {
				slog.Warn("scheduler: memory guard: thaw a VM", "vm", id, "error", err)
			}
		}
	}()
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

// brakeVMMemory stops the session VMs from taking more of the host's memory
// before the memory guard has saved any: a save takes tens of seconds, and
// VMs filling their memory at once take the host's last in under ten.
// Below admission's floor it freezes the VMs the last check listed, which
// takes one write each and no memory, before it takes any lock. A frozen VM
// allocates nothing, so the guard's saves then free memory faster than it
// goes. At the floor or above it thaws one VM a check, the longest frozen
// first; one that takes the host below the floor again is frozen with the
// rest.
func (s *Scheduler) brakeVMMemory(ctx context.Context, vms runtime.FreezeRuntime) {
	available, floor, err := s.hostMemory()
	if err != nil {
		return
	}
	var froze []runtime.ID
	if available < floor {
		for _, id := range s.vmBrakeTargets {
			if ok, _ := vms.Freeze(id); ok {
				froze = append(froze, id)
			}
		}
		if !s.vmMemoryBraked {
			slog.Warn("scheduler: memory guard: the host is short of memory; froze the session VMs", "available", available, "floor", floor, "vms", len(froze))
		}
		s.vmMemoryBraked = true
	}
	s.settleFrozen(ctx, vms, froze, available >= floor)
}

// settleFrozen lists the VMs the next check freezes: those of the sessions
// the server is doing nothing to. The VM of a session it launches, wakes,
// closes, kills, finishes, saves the ordinary way or takes up after a
// restart needs its guest to answer, so it is thawed and left out until
// that is done. A VM the guard saves frozen stays so. It tells each open
// session in froze why it paused, and with thaw it thaws the VM frozen
// longest. Every thaw is made under s.mu, which the guard takes to claim a
// VM for a frozen save, so none lands on a VM it has claimed.
func (s *Scheduler) settleFrozen(ctx context.Context, vms runtime.FreezeRuntime, froze []runtime.ID, thaw bool) {
	now := time.Now()
	var saving []runtime.ID
	var noted []*supervised
	s.mu.Lock()
	s.vmBrakeTargets = s.vmBrakeTargets[:0]
	for cid := range s.guestsNeeded {
		s.thawBraked(vms, cid)
	}
	for _, e := range s.runs {
		cid := e.containerID
		if _, vm := s.saver(cid); !vm {
			continue
		}
		busy := !e.lifecycleMu.TryLock()
		if !busy {
			e.lifecycleMu.Unlock()
		}
		switch {
		case e.frozenSave:
			saving = append(saving, cid)
		case s.guestsNeeded[cid] > 0:
		case busy || e.waking != nil || e.status == domain.RunProvisioning || !e.retained && (e.status.Terminal() || e.killRequested || e.finalizing):
			s.thawBraked(vms, cid)
		default:
			s.vmBrakeTargets = append(s.vmBrakeTargets, cid)
			if slices.Contains(froze, cid) && !e.retained && !e.paused && now.Sub(e.frozenNotedAt) >= memoryFrozenNoteEvery {
				e.frozenNotedAt = now
				noted = append(noted, e)
			}
		}
	}
	if thaw && s.vmMemoryBraked {
		frozen := slices.DeleteFunc(vms.Frozen(), func(id runtime.ID) bool { return slices.Contains(saving, id) })
		if len(frozen) == 0 && len(saving) == 0 {
			s.vmMemoryBraked = false
			slog.Info("scheduler: memory guard: every session VM runs again")
		}
		for _, id := range frozen {
			err := vms.Thaw(id)
			if err == nil {
				break
			}
			slog.Warn("scheduler: memory guard: thaw a VM", "vm", id, "error", err)
		}
	}
	if len(noted) > 0 {
		s.wg.Add(1)
	}
	s.mu.Unlock()
	if len(noted) > 0 {
		// A slow event log must not hold up the next check.
		go func() {
			defer s.wg.Done()
			for _, e := range noted {
				s.publishTimeline(ctx, e.workspaceID, e.runID, "", events.TimelineNote, memoryFrozenNote)
			}
		}()
	}
}

// thawBraked thaws a VM the server needs an answer from, while the brake
// holds. Caller holds s.mu.
func (s *Scheduler) thawBraked(vms runtime.FreezeRuntime, cid runtime.ID) {
	if !s.vmMemoryBraked {
		return
	}
	if err := vms.Thaw(cid); err != nil {
		slog.Warn("scheduler: memory guard: thaw the VM of a session the server is working on", "vm", cid, "error", err)
	}
}

// needGuest keeps the brake off cid's VM until the returned function is
// called, and thaws it: recovery takes up only a guest that answers.
func (s *Scheduler) needGuest(cid runtime.ID) func() {
	vms, ok := s.cfg.Runtime.(runtime.FreezeRuntime)
	if !ok || cid == "" {
		return func() {}
	}
	s.mu.Lock()
	s.guestsNeeded[cid]++
	err := vms.Thaw(cid)
	s.mu.Unlock()
	if err != nil {
		slog.Warn("scheduler: memory guard: thaw the VM of a session to take it up", "vm", cid, "error", err)
	}
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.guestsNeeded[cid]--; s.guestsNeeded[cid] == 0 {
			delete(s.guestsNeeded, cid)
		}
	}
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
	if _, err := vms.Freeze(cid); err != nil {
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
