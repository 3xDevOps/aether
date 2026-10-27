package scheduler

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"math"
	goruntime "runtime"
	"sync"
	"time"

	"github.com/3xDevOps/Aether/internal/disk"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/runtime"
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
	startupMemoryBytes   uint64 = 512 << 20
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
		return domain.RunLimits{}, fmt.Errorf("scheduler: read server run limits: %w", err)
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

// reserveCapacity serializes the bounded probe and accounting independently of
// Scheduler.mu. Every admission sees outstanding provisioning allowances, not
// the sum of container memory ceilings. A completed reservation is released
// under the same gate so the next probe cannot reuse a pre-completion snapshot.
func (s *Scheduler) reserveCapacity(ctx context.Context) (func(), error) {
	release, err := s.tryReserveCapacity(ctx)
	if errors.Is(err, ErrDiskFull) && ctx.Err() == nil {
		// One reclaim pass, then fresh measurements. Never loop or
		// reinterpret unavailable capacity.
		s.reclaimDisk(ctx, "")
		return s.tryReserveCapacity(ctx)
	}
	return release, err
}

// measureCapacity returns the Aether data filesystem (unless the free-disk
// guard is disabled) and, when the runtime reports it, the runtime's capacity.
func (s *Scheduler) measureCapacity(ctx context.Context) ([]runtime.FilesystemCapacity, *runtime.HostCapacity, error) {
	var filesystems []runtime.FilesystemCapacity
	if s.cfg.MinFreeBytes >= 0 {
		measure := s.cfg.filesystemCapacity
		if measure == nil {
			measure = disk.Filesystem
		}
		path := s.cfg.DataDir
		if path == "" {
			path = s.cfg.StateDir
		}
		data, err := measure(path)
		if err != nil {
			return nil, nil, fmt.Errorf("%w: Aether data filesystem: %w", ErrCapacityUnknown, err)
		}
		filesystems = append(filesystems, runtime.FilesystemCapacity{Name: "Aether data filesystem", TotalBytes: data.TotalBytes, FreeBytes: data.FreeBytes})
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

func (s *Scheduler) tryReserveCapacity(ctx context.Context) (func(), error) {
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
		floor := max(uint64(2<<30), capacity.MemoryTotalBytes/10)
		if !hasProvisioningHeadroom(capacity.MemoryAvailableBytes, floor, startupMemoryBytes, s.capacityReservations) {
			return nil, fmt.Errorf("%w: %d available bytes; reserve %d bytes plus %d bytes per provisioning (%d already in flight)",
				ErrMemoryPressure, capacity.MemoryAvailableBytes, floor, startupMemoryBytes, s.capacityReservations)
		}
	}
	if err := probeCtx.Err(); err != nil {
		return nil, fmt.Errorf("%w: capacity probe: %w", ErrCapacityUnknown, err)
	}
	s.capacityReservations++
	var once sync.Once
	return func() {
		once.Do(func() {
			s.capacityGate <- struct{}{}
			s.capacityReservations--
			<-s.capacityGate
		})
	}, nil
}

// Division after subtraction avoids overflowing when a probe or override is
// near uint64's limit. The next provisioning also needs its own allowance.
func hasProvisioningHeadroom(available, floor, allowance, outstanding uint64) bool {
	return available >= floor && (available-floor)/allowance > outstanding
}
