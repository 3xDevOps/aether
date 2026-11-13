package vm

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/3xDevOps/Aether/internal/disk"
	"github.com/3xDevOps/Aether/internal/runtime"
)

const (
	statsConcurrency = 4
	statsTimeout     = 3 * time.Second
)

// memoryEvents reports a stopped VM as fs.ErrNotExist, as the OOM watcher
// treats a stopped container's missing cgroup. A paused guest cannot answer
// and kills nothing, so it reports what it last did.
func (e *engine) memoryEvents(ctx context.Context, key string) (runtime.MemoryEvents, error) {
	if _, err := e.load(key); err != nil {
		return runtime.MemoryEvents{}, err
	}
	if up, err := e.running(ctx, key); err != nil || !up {
		return runtime.MemoryEvents{}, errors.Join(err, fmt.Errorf("vm runtime: VM %s is not running: %w", key, fs.ErrNotExist))
	}
	if e.paused(ctx, key) {
		e.mu.Lock()
		events, known := e.events[key]
		e.mu.Unlock()
		if !known {
			return events, fmt.Errorf("vm runtime: VM %s is paused: %w", key, fs.ErrNotExist)
		}
		return events, nil
	}
	h, err := e.helper(key)
	if err != nil {
		return runtime.MemoryEvents{}, err
	}
	st, err := h.Stats(ctx)
	if err != nil {
		return runtime.MemoryEvents{}, fmt.Errorf("vm runtime: memory events of VM %s: %w", key, err)
	}
	events := runtime.MemoryEvents{OOMKills: st.OOMKills, LimitBytes: st.MemoryLimitBytes}
	e.mu.Lock()
	e.events[key] = events
	e.mu.Unlock()
	return events, nil
}

func (e *engine) paused(ctx context.Context, key string) bool {
	state, err := newCHAPI(e.path(key, apiSocket)).state(ctx)
	return err == nil && state == "Paused"
}

// CPU, memory and processes come from the helper, network and disk bytes
// from Cloud Hypervisor's device counters.
func (e *engine) stats(ctx context.Context) ([]runtime.ContainerStat, error) {
	ctx, cancel := context.WithTimeout(ctx, statsTimeout)
	defer cancel()
	keys, err := e.keys()
	if err != nil {
		return nil, err
	}
	results := make([]*runtime.ContainerStat, len(keys))
	errs := make([]error, len(keys))
	var g errgroup.Group
	g.SetLimit(statsConcurrency)
	for i, key := range keys {
		g.Go(func() error {
			results[i], errs[i] = e.sample(ctx, key)
			return nil
		})
	}
	_ = g.Wait()
	var out []runtime.ContainerStat
	for _, st := range results {
		if st != nil {
			out = append(out, *st)
		}
	}
	return out, errors.Join(errs...)
}

func (e *engine) sample(ctx context.Context, key string) (*runtime.ContainerStat, error) {
	rec, err := e.load(key)
	if err != nil {
		return nil, err
	}
	if up, upErr := e.running(ctx, key); upErr != nil || !up || e.paused(ctx, key) {
		return nil, upErr
	}
	h, err := e.helper(key)
	if err != nil {
		return nil, err
	}
	g, err := h.Stats(ctx)
	if err != nil {
		return nil, fmt.Errorf("vm runtime: stats of VM %s: %w", key, err)
	}
	counters, err := newCHAPI(e.path(key, apiSocket)).counters(ctx)
	if err != nil {
		return nil, fmt.Errorf("vm runtime: device counters of VM %s: %w", key, err)
	}
	cores := rec.CPULimit
	if cores == 0 {
		cores = float64(rec.CPUs)
	}
	return &runtime.ContainerStat{
		ID: idPrefix + key, CreationKey: cmp.Or(rec.CreationKey, key), Read: time.Now(),
		CPUUsageNanos: g.CPUUsageNanos, CPULimitCores: cores,
		MemoryBytes: g.MemoryBytes, MemoryLimitBytes: g.MemoryLimitBytes, Pids: g.Pids, PidsLimit: g.PidsLimit,
		NetRxBytes: counters["net0"]["rx_bytes"], NetTxBytes: counters["net0"]["tx_bytes"],
		DiskReadBytes:  counters["root"]["read_bytes"] + counters["data"]["read_bytes"],
		DiskWriteBytes: counters["root"]["write_bytes"] + counters["data"]["write_bytes"],
	}, nil
}

func (e *engine) filesystem() (runtime.FilesystemCapacity, error) {
	if err := os.MkdirAll(e.root, 0o700); err != nil {
		return runtime.FilesystemCapacity{}, err
	}
	usage, err := disk.Filesystem(e.root)
	if err != nil {
		return runtime.FilesystemCapacity{}, err
	}
	return runtime.FilesystemCapacity{Name: "Session VM disks", TotalBytes: usage.TotalBytes, FreeBytes: usage.FreeBytes}, nil
}
