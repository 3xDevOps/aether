package scheduler

import (
	"context"
	"log/slog"
	"time"

	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/runtime"
)

// vmDiskCheck is how often the disk guard measures: a stat of each VM disk
// and of the data filesystem.
const vmDiskCheck = 5 * time.Second

const (
	diskHeldNote     = "Paused: the server is low on disk, and this session's disks grew the most since the last check. It resumes on its own once there is space again."
	diskReleasedNote = "Resumed: the server has disk space again."
)

// guardVMDisks keeps session VMs, whose sparse disks grow as their guests
// write, from filling the data filesystem: while it is below its floor,
// each check pauses the session whose disks grew the most since the last
// one, and once it is back above the floor and reclaim's headroom the
// sessions it paused resume. A session whose disks did not grow is never
// paused.
func (s *Scheduler) guardVMDisks(ctx context.Context) {
	vms, ok := s.cfg.Runtime.(runtime.DiskUseRuntime)
	if !ok || s.cfg.MinFreeBytes < 0 {
		return
	}
	s.vmDiskMu.Lock()
	defer s.vmDiskMu.Unlock()
	use, err := vms.DiskUse(ctx)
	if err != nil {
		slog.Warn("scheduler: disk guard: measure VM disks", "error", err)
		return
	}
	last := s.vmDiskUse
	s.vmDiskUse = use
	data, err := s.dataFilesystem()
	if err != nil {
		slog.Warn("scheduler: disk guard: measure the data filesystem", "error", err)
		return
	}
	floor := diskReserve(data.TotalBytes, s.cfg.MinFreeBytes)
	switch {
	case data.FreeBytes < floor:
		s.holdGrowingVM(ctx, use, last)
	case data.FreeBytes >= floor+reclaimHeadroom || floor+reclaimHeadroom >= data.TotalBytes:
		s.releaseHeldVMs(ctx)
	}
}

func (s *Scheduler) holdGrowingVM(ctx context.Context, use, last map[runtime.ID]int64) {
	s.mu.Lock()
	var held *supervised
	var most int64
	for _, e := range s.runs {
		if e.containerID == "" || e.paused || !e.asleepSince.IsZero() || e.retained || e.finalizing || e.status.Terminal() {
			continue
		}
		before, seen := last[e.containerID]
		if grew := use[e.containerID] - before; seen && grew > most {
			held, most = e, grew
		}
	}
	s.mu.Unlock()
	if held == nil {
		return
	}
	if err := s.Pause(ctx, held.runID, ""); err != nil {
		slog.Warn("scheduler: disk guard: pause a growing session", "run", held.runID, "error", err)
		return
	}
	s.setDiskHeld(held, true)
	s.publishTimeline(ctx, held.workspaceID, held.runID, "", events.TimelineNote, diskHeldNote)
}

// releaseHeldVMs resumes the sessions the guard paused, except one resumed
// meanwhile by hand.
func (s *Scheduler) releaseHeldVMs(ctx context.Context) {
	s.mu.Lock()
	var held []*supervised
	for _, e := range s.runs {
		if e.diskHeld {
			held = append(held, e)
		}
	}
	s.mu.Unlock()
	for _, e := range held {
		if s.Paused(e.runID) {
			if err := s.Resume(ctx, e.runID, ""); err != nil {
				slog.Warn("scheduler: disk guard: resume a session", "run", e.runID, "error", err)
				continue
			}
			s.publishTimeline(ctx, e.workspaceID, e.runID, "", events.TimelineNote, diskReleasedNote)
		}
		s.setDiskHeld(e, false)
	}
}

func (s *Scheduler) setDiskHeld(entry *supervised, held bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry.diskHeld = held
	if s.runs[entry.runID] != entry {
		return
	}
	if err := s.writeSidecar(entry.sidecar()); err != nil {
		slog.Warn("scheduler: persist the disk guard's pause", "run", entry.runID, "error", err)
	}
}
