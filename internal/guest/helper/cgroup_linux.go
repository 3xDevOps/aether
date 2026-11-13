package helper

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/3xDevOps/Aether/internal/guest/guestpb"
)

// sessionCgroup is the cgroup whose pids.max is the session's process
// limit. Processes start in its procs child: cgroup v2 lets only a leaf hold
// processes once dockerd puts its containers beside it.
type sessionCgroup struct {
	dir   string
	procs *os.File
	limit atomic.Int64
}

// sessionCgroupParent is the cgroup dockerd puts nested containers under.
const sessionCgroupParent = "/session"

func newSessionCgroup(root string) (*sessionCgroup, error) {
	if err := os.WriteFile(filepath.Join(root, "cgroup.subtree_control"), []byte("+pids"), 0); err != nil {
		return nil, fmt.Errorf("enable the pids controller: %w", err)
	}
	dir := filepath.Join(root, sessionCgroupParent)
	procs := filepath.Join(dir, "procs")
	if err := os.MkdirAll(procs, 0o755); err != nil {
		return nil, err
	}
	f, err := os.Open(procs)
	if err != nil {
		return nil, err
	}
	return &sessionCgroup{dir: dir, procs: f}, nil
}

// setLimit sets the session's process limit; 0 is unlimited.
func (c *sessionCgroup) setLimit(limit int64) error {
	value := "max"
	if limit > 0 {
		value = strconv.FormatInt(limit, 10)
	}
	if err := os.WriteFile(filepath.Join(c.dir, "pids.max"), []byte(value), 0); err != nil {
		return fmt.Errorf("set the process limit: %w", err)
	}
	c.limit.Store(max(limit, 0))
	return nil
}

// readStats counts the whole VM, which is the session: memory in use is
// what the kernel could not hand back without reclaim.
func readStats(cgroup *sessionCgroup) (*guestpb.StatsReply, error) {
	meminfo, err := readCounters("/proc/meminfo", 1024)
	if err != nil {
		return nil, err
	}
	vmstat, err := readCounters("/proc/vmstat", 1)
	if err != nil {
		return nil, err
	}
	cpu, err := cpuNanos()
	if err != nil {
		return nil, err
	}
	stats := &guestpb.StatsReply{
		CpuUsageNanos:    cpu,
		MemoryLimitBytes: meminfo["MemTotal"],
		MemoryBytes:      meminfo["MemTotal"] - min(meminfo["MemAvailable"], meminfo["MemTotal"]),
		OomKills:         vmstat["oom_kill"],
	}
	if cgroup != nil {
		data, err := os.ReadFile(filepath.Join(cgroup.dir, "pids.current"))
		if err != nil {
			return nil, err
		}
		if stats.Pids, err = strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64); err != nil {
			return nil, fmt.Errorf("pids.current: %w", err)
		}
		stats.PidsLimit = uint64(cgroup.limit.Load())
	}
	return stats, nil
}

// readCounters reads "name value [unit]" lines, scaling each value.
func readCounters(name string, scale uint64) (map[string]uint64, error) {
	data, err := os.ReadFile(name)
	if err != nil {
		return nil, err
	}
	counters := map[string]uint64{}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		if v, err := strconv.ParseUint(fields[1], 10, 64); err == nil {
			counters[strings.TrimSuffix(fields[0], ":")] = v * scale
		}
	}
	return counters, scanner.Err()
}

// cpuNanos is the time every CPU spent outside idle and I/O wait. /proc/stat
// counts in USER_HZ, which is 100 on every Linux architecture.
func cpuNanos() (uint64, error) {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0, err
	}
	line, _, _ := strings.Cut(string(data), "\n")
	fields := strings.Fields(line)
	if len(fields) < 8 || fields[0] != "cpu" {
		return 0, fmt.Errorf("/proc/stat starts with %q", line)
	}
	var ticks uint64
	// user nice system idle iowait irq softirq: everything but idle and iowait.
	for i, f := range fields[1:8] {
		if i == 3 || i == 4 {
			continue
		}
		v, err := strconv.ParseUint(f, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("/proc/stat: %w", err)
		}
		ticks += v
	}
	return ticks * 1e7, nil
}
