package scheduler

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	goruntime "runtime"
	"slices"
	"sync"
	"time"

	"github.com/3xDevOps/Aether/internal/disk"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/runtime"
	"github.com/3xDevOps/Aether/internal/sysstats"
)

// Capacity refusals only affect new provisioning, never evict existing work.
var (
	ErrDiskFull           = errors.New("scheduler: insufficient disk headroom")
	ErrMemoryPressure     = errors.New("scheduler: insufficient host memory headroom")
	ErrBrowserCPUCapacity = errors.New("scheduler: insufficient browser CPU capacity")
	ErrCapacityUnknown    = errors.New("scheduler: host capacity unavailable")
)

const (
	capacityProbeTimeout        = 5 * time.Second
	startupDiskBytes     uint64 = 1 << 30
	// startupMemoryBytes covers what a session VM holds of the host once it
	// has booted, which TestKVMWakeLoadsBackWhatWasSaved measures.
	startupMemoryBytes uint64 = 512 << 20
	// memoryLoadTime is how long what a woken VM holds stays flat before its
	// claim ends: Cloud Hypervisor loads the rest of the saved memory in the
	// background, as fast as the disk reads it.
	memoryLoadTime = time.Minute
	// maxMemoryPressure is the "some avg60" percentage of the server's own
	// cgroup memory.pressure at which new launches are refused.
	maxMemoryPressure = 10.0
)

// Variables so tests can point the pressure check at a fake procfs and cgroup tree.
var (
	procRoot   = "/proc"
	cgroupRoot = "/sys/fs/cgroup"
)

// ValidateRunBudgets is shared by server startup and the scheduler. Zero selects
// automatic limits; it does not disable protection for managed environments.
func ValidateRunBudgets(cpus float64, memory, pids int64) error {
	if math.IsNaN(cpus) || math.IsInf(cpus, 0) || cpus < 0 || (cpus > 0 && cpus < 0.5e-9) || cpus >= float64(math.MaxInt64)/1e9 {
		return errors.New("run-cpus must be finite, nonnegative, and representable as a CPU quota (0 = automatic)")
	}
	if memory < 0 {
		return errors.New("run-memory must be nonnegative bytes (0 = automatic)")
	}
	if pids < 0 {
		return errors.New("run-pids must be nonnegative (0 = automatic)")
	}
	return nil
}

// ResolveRunLimits fills the unset fields with the automatic limits.
func ResolveRunLimits(l domain.RunLimits) domain.RunLimits {
	return domain.RunLimits{
		CPUs:        cmp.Or(l.CPUs, float64(min(8, goruntime.NumCPU()))),
		MemoryBytes: cmp.Or(l.MemoryBytes, 8<<30),
		Pids:        cmp.Or(l.Pids, 4096),
	}
}

func resolveRunBudgets(cfg *Config) {
	resolved := ResolveRunLimits(domain.RunLimits{CPUs: cfg.RunCPULimit, MemoryBytes: cfg.RunMemoryBytes, Pids: cfg.RunPidsLimit})
	cfg.RunCPULimit, cfg.RunMemoryBytes, cfg.RunPidsLimit = resolved.CPUs, resolved.MemoryBytes, resolved.Pids
}

// runLimits is the ceiling for a container: the run's own override, else the
// limits an admin set from the dashboard, else the server's flags and
// automatic values. Each field resolves on its own.
func (s *Scheduler) runLimits(ctx context.Context, override domain.RunLimits) (domain.RunLimits, error) {
	server, err := s.cfg.Store.ServerRunLimits(ctx)
	if err != nil {
		return domain.RunLimits{}, fmt.Errorf("scheduler: read server session limits: %w", err)
	}
	return domain.RunLimits{
		CPUs:        cmp.Or(override.CPUs, server.CPUs, s.cfg.RunCPULimit),
		MemoryBytes: cmp.Or(override.MemoryBytes, server.MemoryBytes, s.cfg.RunMemoryBytes),
		Pids:        cmp.Or(override.Pids, server.Pids, s.cfg.RunPidsLimit),
	}, nil
}

func diskReserve(total uint64, override int64) uint64 {
	if override > 0 {
		return uint64(override)
	}
	return max(uint64(5<<30), min(total/20, uint64(20<<30)))
}

// memoryFloor is the host memory admission keeps available.
func memoryFloor(total uint64) uint64 {
	return max(uint64(2<<30), total/10)
}

// memoryClaim is host memory an admission set aside that the host does not
// show as used yet.
type memoryClaim struct {
	bytes uint64
	// vm is the saved VM a wake restores, which goes on loading its memory
	// once the wake returns. held is the most of bytes it has held, grew is
	// when that last rose, and released is set once the wake returned.
	vm       runtime.ID
	held     uint64
	grew     time.Time
	released bool
}

// memoryShortage is a memory refusal that saving idle VMs can lift: the
// host has available bytes, short of the floor, this start's need and what
// admitted starts have yet to take.
type memoryShortage struct{ available, floor, need, claimed uint64 }

func (e *memoryShortage) Error() string {
	return fmt.Sprintf("%v: %d available bytes; reserve %d bytes plus %d bytes for this start and %d bytes for starts in flight",
		ErrMemoryPressure, e.available, e.floor, e.need, e.claimed)
}

func (e *memoryShortage) Unwrap() error { return ErrMemoryPressure }

// reserveCapacity admits a start that brings up a new container or VM.
func (s *Scheduler) reserveCapacity(ctx context.Context) (func(), error) {
	return s.reserve(ctx, &memoryClaim{bytes: startupMemoryBytes})
}

// reserveWake admits the wake of a sleeping container: a saved VM must fit
// the memory its restore loads back.
func (s *Scheduler) reserveWake(ctx context.Context, cid runtime.ID) (func(), error) {
	return s.reserve(ctx, s.wakeClaim(cid))
}

// reserveBoot admits the boot of a stopped VM to commit its work on memory
// alone, as a wake: disk reclaim runs that boot to free checkouts, so it
// neither waits on disk nor saves idle VMs.
func (s *Scheduler) reserveBoot(ctx context.Context, cid runtime.ID) (func(), error) {
	claim := s.wakeClaim(cid)
	probeCtx, cancel := context.WithTimeout(ctx, capacityProbeTimeout)
	defer cancel()
	select {
	case s.capacityGate <- struct{}{}:
	case <-probeCtx.Done():
		return nil, fmt.Errorf("%w: waiting for capacity probe: %w", ErrCapacityUnknown, probeCtx.Err())
	}
	defer func() { <-s.capacityGate }()
	available, floor, err := s.hostMemory()
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrCapacityUnknown, err)
	}
	if err = s.fitMemory(probeCtx, available, floor, claim); err != nil {
		return nil, err
	}
	s.memoryClaims = append(s.memoryClaims, claim)
	return func() {
		s.capacityGate <- struct{}{}
		s.releaseClaim(claim)
		<-s.capacityGate
	}, nil
}

// wakeClaim is the memory waking cid takes: what its saved state holds on
// disk, or the most it may when that cannot be read, and no less than a
// start.
func (s *Scheduler) wakeClaim(cid runtime.ID) *memoryClaim {
	claim := &memoryClaim{bytes: startupMemoryBytes}
	saver, ok := s.saver(cid)
	if !ok {
		return claim
	}
	saved, err := saver.SavedMemory(cid)
	if err != nil {
		slog.Warn("scheduler: measure the saved memory of a session VM; counting the most it may load", "container", cid, "bytes", saved, "error", err)
	}
	if saved > claim.bytes {
		claim.bytes, claim.vm = saved, cid
	}
	return claim
}

// reserve serializes the bounded probe and accounting independently of
// Scheduler.mu. Every admission sees the memory outstanding admissions have
// yet to take, not the sum of container memory ceilings. A completed
// reservation is released under the same gate so the next probe cannot reuse
// a pre-completion snapshot.
func (s *Scheduler) reserve(ctx context.Context, claim *memoryClaim) (func(), error) {
	release, err := s.tryReserveCapacity(ctx, claim)
	// One reclaim or save pass, then fresh measurements. Never loop or
	// reinterpret unavailable capacity.
	var short *memoryShortage
	switch {
	case ctx.Err() != nil:
	case errors.Is(err, ErrDiskFull):
		s.reclaimDisk(ctx, "")
		return s.tryReserveCapacity(ctx, claim)
	case errors.As(err, &short) && s.saveIdleVMs(ctx, short.need+short.claimed):
		return s.tryReserveCapacity(ctx, claim)
	}
	return release, err
}

// fitMemory refuses claim unless available memory covers the floor, claim
// and what admitted claims have yet to take. Caller holds capacityGate.
func (s *Scheduler) fitMemory(ctx context.Context, available, floor uint64, claim *memoryClaim) error {
	if claimed := s.claimedMemory(ctx); available < floor+claim.bytes+claimed {
		return &memoryShortage{available: available, floor: floor, need: claim.bytes, claimed: claimed}
	}
	return nil
}

// claimedMemory is what admitted claims have yet to take from the host: all
// of a launch or wake still running, and after the wake of a saved VM what
// it has not loaded back. That claim ends once a measurement shows the VM
// stopped or holding all of it, or memoryLoadTime after what it held last
// grew; a frozen VM loads nothing, so its time does not run. A failed
// measurement changes nothing else. The memory guard calls it every check,
// so growth is seen however rarely admission runs. Caller holds
// capacityGate.
func (s *Scheduler) claimedMemory(ctx context.Context) uint64 {
	var use map[runtime.ID]uint64
	var frozen []runtime.ID
	measured := false
	source, ok := s.cfg.Runtime.(runtime.MemoryUseRuntime)
	if ok && slices.ContainsFunc(s.memoryClaims, func(c *memoryClaim) bool { return c.vm != "" }) {
		var err error
		use, err = source.MemoryUse(ctx)
		if err != nil {
			slog.Warn("scheduler: measure the memory of woken session VMs; their claims stand", "error", err)
		}
		measured = err == nil
		if vms, ok := s.cfg.Runtime.(runtime.FreezeRuntime); ok {
			frozen = vms.Frozen()
		}
	}
	now := time.Now()
	var total uint64
	s.memoryClaims = slices.DeleteFunc(s.memoryClaims, func(c *memoryClaim) bool {
		held, running := use[c.vm]
		if measured && held > c.held || slices.Contains(frozen, c.vm) {
			c.held, c.grew = max(c.held, held), now
		}
		if c.released && (measured && (!running || c.held >= c.bytes) || now.Sub(c.grew) >= memoryLoadTime) {
			return true
		}
		total += c.bytes - min(c.bytes, c.held)
		return false
	})
	return total
}

// releaseClaim ends claim's admission: a launch's claim goes, a wake's
// stays while its VM loads. Caller holds capacityGate.
func (s *Scheduler) releaseClaim(claim *memoryClaim) {
	if claim.vm == "" {
		s.memoryClaims = slices.DeleteFunc(s.memoryClaims, func(c *memoryClaim) bool { return c == claim })
		return
	}
	claim.released, claim.grew = true, time.Now()
}

// measureCapacity returns the Aether data filesystem (unless the free-disk
// guard is disabled) and, when the runtime reports it, the runtime's capacity.
func (s *Scheduler) measureCapacity(ctx context.Context) ([]runtime.FilesystemCapacity, *runtime.HostCapacity, error) {
	var filesystems []runtime.FilesystemCapacity
	if s.cfg.MinFreeBytes >= 0 {
		data, err := s.dataFilesystem()
		if err != nil {
			return nil, nil, fmt.Errorf("%w: Cloudfield data filesystem: %w", ErrCapacityUnknown, err)
		}
		filesystems = append(filesystems, runtime.FilesystemCapacity{Name: "Cloudfield data filesystem", TotalBytes: data.TotalBytes, FreeBytes: data.FreeBytes})
	}
	provider, ok := s.cfg.Runtime.(runtime.CapacityRuntime)
	if !ok {
		return filesystems, nil, nil
	}
	capacity, err := provider.Capacity(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: runtime filesystems and host memory: %w\nthe server's operator can check Docker with: sudo systemctl status docker", ErrCapacityUnknown, err)
	}
	if s.cfg.MinFreeBytes >= 0 && len(capacity.Filesystems) == 0 {
		return nil, nil, fmt.Errorf("%w: runtime filesystems were not measured", ErrCapacityUnknown)
	}
	return append(filesystems, capacity.Filesystems...), &capacity, nil
}

func (s *Scheduler) dataFilesystem() (disk.Usage, error) {
	measure := s.cfg.filesystemCapacity
	if measure == nil {
		measure = disk.Filesystem
	}
	path := s.cfg.DataDir
	if path == "" {
		path = s.cfg.StateDir
	}
	return measure(path)
}

func (s *Scheduler) tryReserveCapacity(ctx context.Context, claim *memoryClaim) (func(), error) {
	probeCtx, cancel := context.WithTimeout(ctx, capacityProbeTimeout)
	defer cancel()
	select {
	case s.capacityGate <- struct{}{}:
	case <-probeCtx.Done():
		return nil, fmt.Errorf("%w: waiting for capacity probe: %w", ErrCapacityUnknown, probeCtx.Err())
	}
	defer func() { <-s.capacityGate }()
	if err := probeCtx.Err(); err != nil {
		return nil, fmt.Errorf("%w: capacity probe: %w", ErrCapacityUnknown, err)
	}
	checkDisk := func(fs runtime.FilesystemCapacity) error {
		if s.cfg.MinFreeBytes < 0 {
			return nil
		}
		if fs.TotalBytes == 0 || fs.FreeBytes > fs.TotalBytes {
			return fmt.Errorf("%w: %s disk measurement is invalid", ErrCapacityUnknown, fs.Name)
		}
		floor := diskReserve(fs.TotalBytes, s.cfg.MinFreeBytes)
		if !hasProvisioningHeadroom(fs.FreeBytes, floor, startupDiskBytes, s.capacityReservations) {
			return fmt.Errorf("%w: %s has %d free bytes; reserve %d bytes plus %d bytes per provisioning (%d already in flight)",
				ErrDiskFull, fs.Name, fs.FreeBytes, floor, startupDiskBytes, s.capacityReservations)
		}
		return nil
	}
	filesystems, capacity, err := s.measureCapacity(probeCtx)
	if err != nil {
		return nil, err
	}
	for _, fs := range filesystems {
		if err := checkDisk(fs); err != nil {
			return nil, err
		}
	}
	if capacity != nil {
		if capacity.MemoryTotalBytes == 0 || capacity.MemoryAvailableBytes > capacity.MemoryTotalBytes {
			return nil, fmt.Errorf("%w: host memory measurement is invalid", ErrCapacityUnknown)
		}
		if err := s.fitMemory(probeCtx, capacity.MemoryAvailableBytes, memoryFloor(capacity.MemoryTotalBytes), claim); err != nil {
			return nil, err
		}
		// MemAvailable does not show a server that is stalling on memory. Only
		// the server's own cgroup counts: a session stalled inside its own
		// memory limit reclaims in its own cgroup and does not slow the server.
		// A host without cgroup v2 pressure accounting is not refused, nor is
		// a server in the root cgroup, whose file would be host-wide.
		var pressure *float64
		cgroup, err := cgroupPath(procRoot, os.Getpid())
		if err == nil && cgroup != "/" {
			pressure, err = sysstats.CgroupMemoryPressure(filepath.Join(cgroupRoot, cgroup))
		}
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%w: server memory pressure: %w", ErrCapacityUnknown, err)
		}
		if pressure == nil {
			s.pressureSkipLog.Do(func() {
				slog.Info("scheduler: skipping the memory pressure check; the server has no cgroup of its own with memory.pressure", "cgroup_root", cgroupRoot)
			})
		} else if *pressure >= maxMemoryPressure {
			return nil, fmt.Errorf("%w: the server stalled on memory for %.1f%% of the last 60s (its cgroup's memory.pressure, some avg60); launches are refused at %.0f%%",
				ErrMemoryPressure, *pressure, maxMemoryPressure)
		}
	}
	if err := probeCtx.Err(); err != nil {
		return nil, fmt.Errorf("%w: capacity probe: %w", ErrCapacityUnknown, err)
	}
	s.capacityReservations++
	s.memoryClaims = append(s.memoryClaims, claim)
	var once sync.Once
	return func() {
		once.Do(func() {
			s.capacityGate <- struct{}{}
			s.capacityReservations--
			s.releaseClaim(claim)
			<-s.capacityGate
		})
	}, nil
}

// Division after subtraction avoids overflowing when a probe or override is
// near uint64's limit. The next provisioning also needs its own allowance.
func hasProvisioningHeadroom(available, floor, allowance, outstanding uint64) bool {
	return available >= floor && (available-floor)/allowance > outstanding
}
