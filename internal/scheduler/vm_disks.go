package scheduler

import (
	"cmp"
	"context"
	"log/slog"
	"maps"
	"slices"
	"time"

	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/runtime"
)

const (
	vmDiskCheck = 5 * time.Second
	// vmDiskGrowth is what a VM's disks must grow in one check to count as
	// filling the disk: every running agent writes a little.
	vmDiskGrowth = 64 << 20
	// vmDiskRehold is how soon after the guard resumed a session a second
	// hold leaves it for its member to resume, so one writer cannot drain
	// everyone's reclaimable data in steps.
	vmDiskRehold = 10 * time.Minute
)

const (
	diskHeldNote     = "Paused: the server is low on disk, and this session's disks were filling it. It resumes on its own once there is space again."
	diskReheldNote   = "Paused again: the server is still low on disk, and this session's disks filled it again soon after it resumed. Resume it yourself once there is space."
	diskReleasedNote = "Resumed: the server has disk space again."
)

func (s *Scheduler) watchVMDisks(ctx context.Context) {
	if _, ok := s.cfg.Runtime.(runtime.DiskUseRuntime); !ok {
		return
	}
	tick := time.NewTicker(vmDiskCheck)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			s.guardVMDisks(ctx)
		}
	}
}

// guardVMDisks keeps session VMs, whose sparse disks grow as their guests
// write, from filling the data filesystem. Only a session whose disks grew
// is ever paused, and only the guard's own pauses are resumed. The baseline
// it measures growth against is taken when the filesystem first falls below
// the reserve and kept until it recovers, so a session filling the disk
// slowly is caught once it has grown enough in total, not only in one check.
func (s *Scheduler) guardVMDisks(ctx context.Context) {
	vms, ok := s.cfg.Runtime.(runtime.DiskUseRuntime)
	if !ok {
		return
	}
	s.vmDiskMu.Lock()
	defer s.vmDiskMu.Unlock()
	data, err := s.dataFilesystem()
	if err != nil {
		slog.Warn("scheduler: disk guard: measure the data filesystem", "error", err)
		return
	}
	floor := diskReserve(data.TotalBytes, s.cfg.MinFreeBytes)
	recovered := s.cfg.MinFreeBytes < 0 || data.FreeBytes >= floor+reclaimHeadroom || floor+reclaimHeadroom >= data.TotalBytes
	if recovered {
		s.releaseHeldVMs(ctx)
		s.vmDiskLow, s.vmDiskBase = false, nil
		return
	}
	if data.FreeBytes >= floor {
		// Between the reserve and its headroom: hold the baseline, neither
		// pause nor resume, so the two thresholds do not flap.
		return
	}
	use, err := vms.DiskUse(ctx)
	if err != nil {
		slog.Warn("scheduler: disk guard: measure VM disks", "error", err)
		return
	}
	if !s.vmDiskLow {
		s.vmDiskLow, s.vmDiskBase = true, use
		return
	}
	s.holdGrowingVMs(ctx, vms, use, s.vmDiskBase)
}

func (s *Scheduler) holdGrowingVMs(ctx context.Context, vms runtime.DiskUseRuntime, use, last map[runtime.ID]int64) {
	grew := map[runtime.ID]int64{}
	for id, n := range use {
		if before, seen := last[id]; seen && n-before >= vmDiskGrowth {
			grew[id] = n - before
		}
	}
	growing := slices.SortedFunc(maps.Keys(grew), func(a, b runtime.ID) int { return cmp.Compare(grew[b], grew[a]) })
	s.mu.Lock()
	sessions := map[runtime.ID]*supervised{}
	for _, e := range s.runs {
		if e.containerID != "" && e.asleepSince.IsZero() && !e.retained && !e.finalizing && !e.status.Terminal() {
			sessions[e.containerID] = e
		}
	}
	s.mu.Unlock()
	for _, id := range growing {
		e, ok := sessions[id]
		if !ok {
			slog.Warn("scheduler: disk guard: a VM that is not a session is filling the data filesystem", "vm", id, "grew_bytes", grew[id])
			continue
		}
		s.holdForDisk(ctx, vms, e)
	}
}

// holdForDisk pauses a session as its member would, recording the hold in
// the same sidecar write, then stops its whole VM. A session its member
// paused that still grows only has its VM stopped.
func (s *Scheduler) holdForDisk(ctx context.Context, vms runtime.DiskUseRuntime, e *supervised) {
	s.mu.Lock()
	paused, rehold := e.paused, time.Since(e.diskReleasedAt) < vmDiskRehold
	if !paused {
		e.diskHeld = !rehold
	}
	s.mu.Unlock()
	if !paused {
		if err := s.Pause(ctx, e.runID, ""); err != nil {
			s.mu.Lock()
			e.diskHeld = false
			s.mu.Unlock()
			slog.Warn("scheduler: disk guard: pause a session filling the disk", "run", e.runID, "error", err)
			return
		}
		note := diskHeldNote
		if rehold {
			note = diskReheldNote
		}
		s.publishTimeline(ctx, e.workspaceID, e.runID, "", events.TimelineNote, note)
	}
	if err := vms.HoldDisks(ctx, e.containerID); err != nil {
		slog.Warn("scheduler: disk guard: stop the VM of a session filling the disk", "run", e.runID, "error", err)
	}
}

// releaseHeldVMs resumes the sessions the guard paused. One a member closed,
// woke or already resumed is no longer the guard's to resume: its hold is
// just dropped, so the guard never fights the member or retries forever.
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
		s.mu.Lock()
		resumable := e.paused && !e.retained && e.asleepSince.IsZero() && !e.finalizing && !e.status.Terminal()
		s.mu.Unlock()
		if !resumable {
			s.setDiskHeld(e, false)
			continue
		}
		if err := s.Resume(ctx, e.runID, ""); err != nil {
			slog.Warn("scheduler: disk guard: resume a session", "run", e.runID, "error", err)
			continue
		}
		s.mu.Lock()
		e.diskReleasedAt = time.Now()
		s.mu.Unlock()
		s.publishTimeline(ctx, e.workspaceID, e.runID, "", events.TimelineNote, diskReleasedNote)
	}
}

// setDiskHeld records whether the guard holds a session, persisting it so a
// restart does not forget, or resume, a hold.
func (s *Scheduler) setDiskHeld(e *supervised, held bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e.diskHeld == held {
		return
	}
	e.diskHeld = held
	if s.runs[e.runID] == e {
		if err := s.writeSidecar(e.sidecar()); err != nil {
			slog.Warn("scheduler: disk guard: persist the hold", "run", e.runID, "error", err)
		}
	}
}
