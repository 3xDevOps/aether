package scheduler

import (
	"context"
	"errors"
	"fmt"
	"math"
	goruntime "runtime"
	"sync"
	"time"

	"github.com/3xDevOps/Aether/internal/disk"
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

func resolveRunBudgets(cfg *Config) {
	if cfg.RunCPULimit == 0 {
		cfg.RunCPULimit = float64(min(8, goruntime.NumCPU()))
	}
	if cfg.RunMemoryBytes == 0 {
		cfg.RunMemoryBytes = 8 << 30
	}
	if cfg.RunPidsLimit == 0 {
		cfg.RunPidsLimit = 4096
	}
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
	if errors.Is(err, ErrDiskFull) && s.cfg.Homes != nil && ctx.Err() == nil {
		// One eligible-owned cleanup attempt, then fresh measurements. Never
		// loop, evict active owners, or reinterpret unavailable capacity.
		s.sweepCaches(ctx, true)
		return s.tryReserveCapacity(ctx)
	}
	return release, err
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
			return nil, fmt.Errorf("%w: Aether data filesystem: %w", ErrCapacityUnknown, err)
		}
		if err := checkDisk(runtime.FilesystemCapacity{Name: "Aether data filesystem", TotalBytes: data.TotalBytes, FreeBytes: data.FreeBytes}); err != nil {
			return nil, err
		}
	}
	if provider, ok := s.cfg.Runtime.(runtime.CapacityRuntime); ok {
		capacity, err := provider.Capacity(probeCtx)
		if err != nil {
			return nil, fmt.Errorf("%w: runtime filesystems and host memory: %w", ErrCapacityUnknown, err)
		}
		if s.cfg.MinFreeBytes >= 0 && len(capacity.Filesystems) == 0 {
			return nil, fmt.Errorf("%w: runtime filesystems were not measured", ErrCapacityUnknown)
		}
		for _, fs := range capacity.Filesystems {
			if err := checkDisk(fs); err != nil {
				return nil, err
			}
		}
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
