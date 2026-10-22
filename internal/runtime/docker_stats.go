package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

const (
	containerStatsConcurrency = 4
	containerStatsTimeout     = 3 * time.Second
)

// ContainerStat is one container's raw counters from a single daemon sample.
// Rates need two samples of the same ID.
type ContainerStat struct {
	ID                  string
	CreationKey         string
	BrowserRunContainer string
	Read                time.Time
	CPUUsageNanos       uint64
	CPUPeriods          uint64
	CPUThrottledPeriods uint64
	CPULimitCores       float64
	MemoryBytes         uint64
	MemoryLimitBytes    uint64
	Pids                uint64
	PidsLimit           uint64
	NetRxBytes          uint64
	NetTxBytes          uint64
	DiskReadBytes       uint64
	DiskWriteBytes      uint64
}

// ContainerStats samples every running container that carries a creation key,
// with a bounded number of daemon calls in flight. When only some containers
// fail it returns the rest with the error. The error is safe to show.
func (d *Docker) ContainerStats(ctx context.Context) ([]ContainerStat, error) {
	ctx, cancel := context.WithTimeout(ctx, containerStatsTimeout)
	defer cancel()
	list, err := d.cli.ContainerList(ctx, client.ContainerListOptions{
		Filters: make(client.Filters).Add("label", labelCreationKey),
	})
	if err != nil {
		return nil, errors.New(dockerStorageError("container stats", err))
	}
	listed := make(map[string]bool, len(list.Items))
	for _, item := range list.Items {
		listed[item.ID] = true
	}
	d.cpuLimits.Range(func(id, _ any) bool {
		if !listed[id.(string)] {
			d.cpuLimits.Delete(id)
		}
		return true
	})
	var (
		mu       sync.Mutex
		wg       sync.WaitGroup
		stats    = make([]ContainerStat, 0, len(list.Items))
		failures []string
		slots    = make(chan struct{}, containerStatsConcurrency)
	)
	for _, item := range list.Items {
		slots <- struct{}{}
		wg.Add(1)
		go func() {
			defer func() { <-slots; wg.Done() }()
			stat, statErr := d.containerStat(ctx, item.ID, item.Labels)
			mu.Lock()
			defer mu.Unlock()
			if statErr != nil {
				failures = append(failures, statErr.Error())
				return
			}
			stats = append(stats, stat)
		}()
	}
	wg.Wait()
	if len(failures) == 0 {
		return stats, nil
	}
	err = fmt.Errorf("%d of %d containers could not be sampled: %s", len(failures), len(list.Items), failures[0])
	if len(stats) == 0 {
		return nil, err
	}
	return stats, err
}

func (d *Docker) containerStat(ctx context.Context, id string, labels map[string]string) (ContainerStat, error) {
	result, err := d.cli.ContainerStats(ctx, id, client.ContainerStatsOptions{})
	if err != nil {
		return ContainerStat{}, errors.New(dockerStorageError("container stats", err))
	}
	defer func() { _ = result.Body.Close() }()
	var resp container.StatsResponse
	if err = json.NewDecoder(result.Body).Decode(&resp); err != nil {
		return ContainerStat{}, errors.New(dockerStorageError("container stats", err))
	}
	stat := containerStatFromResponse(resp)
	stat.ID = id
	stat.CreationKey = labels[labelCreationKey]
	stat.BrowserRunContainer = labels[labelBrowserRun]
	limit, err := d.cpuLimit(ctx, id)
	if err != nil {
		return ContainerStat{}, err
	}
	stat.CPULimitCores = limit
	return stat, nil
}

// cpuLimit inspects once per container: the limit is fixed at creation.
func (d *Docker) cpuLimit(ctx context.Context, id string) (float64, error) {
	if v, ok := d.cpuLimits.Load(id); ok {
		return v.(float64), nil
	}
	result, err := d.cli.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if err != nil {
		return 0, errors.New(dockerStorageError("container inspect", err))
	}
	var limit float64
	if result.Container.HostConfig != nil {
		limit = float64(result.Container.HostConfig.NanoCPUs) / 1e9
	}
	d.cpuLimits.Store(id, limit)
	return limit, nil
}

// containerStatFromResponse reports memory the way `docker stats` does: usage
// minus the inactive file cache, named differently on cgroup v1 and v2.
func containerStatFromResponse(resp container.StatsResponse) ContainerStat {
	stat := ContainerStat{
		Read:                resp.Read,
		CPUUsageNanos:       resp.CPUStats.CPUUsage.TotalUsage,
		CPUPeriods:          resp.CPUStats.ThrottlingData.Periods,
		CPUThrottledPeriods: resp.CPUStats.ThrottlingData.ThrottledPeriods,
		MemoryBytes:         resp.MemoryStats.Usage,
		MemoryLimitBytes:    resp.MemoryStats.Limit,
		Pids:                resp.PidsStats.Current,
		PidsLimit:           resp.PidsStats.Limit,
	}
	inactive, ok := resp.MemoryStats.Stats["inactive_file"]
	if !ok {
		inactive = resp.MemoryStats.Stats["total_inactive_file"]
	}
	stat.MemoryBytes -= min(inactive, stat.MemoryBytes)
	for _, n := range resp.Networks {
		stat.NetRxBytes += n.RxBytes
		stat.NetTxBytes += n.TxBytes
	}
	for _, e := range resp.BlkioStats.IoServiceBytesRecursive {
		switch strings.ToLower(e.Op) {
		case "read":
			stat.DiskReadBytes += e.Value
		case "write":
			stat.DiskWriteBytes += e.Value
		}
	}
	return stat
}
