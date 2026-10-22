package runtime

import (
	"testing"

	"github.com/moby/moby/api/types/container"
)

func TestContainerStatFromResponseSubtractsInactiveFile(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		stats map[string]uint64
		want  uint64
	}{
		"cgroup v2": {map[string]uint64{"inactive_file": 300}, 700},
		"cgroup v1": {map[string]uint64{"total_inactive_file": 400}, 600},
		"no cache":  {nil, 1000},
		"oversized": {map[string]uint64{"inactive_file": 5000}, 0},
	} {
		resp := container.StatsResponse{
			MemoryStats: container.MemoryStats{Usage: 1000, Limit: 2000, Stats: tc.stats},
			CPUStats: container.CPUStats{
				CPUUsage:       container.CPUUsage{TotalUsage: 42},
				ThrottlingData: container.ThrottlingData{Periods: 10, ThrottledPeriods: 3},
			},
			PidsStats: container.PidsStats{Current: 7, Limit: 100},
			Networks: map[string]container.NetworkStats{
				"eth0": {RxBytes: 10, TxBytes: 1},
				"eth1": {RxBytes: 5, TxBytes: 2},
			},
			BlkioStats: container.BlkioStats{IoServiceBytesRecursive: []container.BlkioStatEntry{
				{Op: "Read", Value: 100}, {Op: "write", Value: 50}, {Op: "Read", Value: 1}, {Op: "Total", Value: 999},
			}},
		}
		got := containerStatFromResponse(resp)
		if got.MemoryBytes != tc.want || got.MemoryLimitBytes != 2000 {
			t.Errorf("%s: memory = %d/%d, want %d/2000", name, got.MemoryBytes, got.MemoryLimitBytes, tc.want)
		}
		if got.NetRxBytes != 15 || got.NetTxBytes != 3 || got.DiskReadBytes != 101 || got.DiskWriteBytes != 50 {
			t.Errorf("%s: io = %+v", name, got)
		}
		if got.CPUUsageNanos != 42 || got.CPUPeriods != 10 || got.CPUThrottledPeriods != 3 || got.Pids != 7 || got.PidsLimit != 100 {
			t.Errorf("%s: cpu/pids = %+v", name, got)
		}
	}
}
