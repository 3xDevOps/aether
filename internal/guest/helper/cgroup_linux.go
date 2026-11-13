package helper

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

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

// sessionReserve is kept from the session on top of what was in use or held
// free when the helper started: the helper serves the file service, and
// with no memory left the session's page faults through it would thrash
// instead of ending in an OOM kill.
const sessionReserve = 48 << 20

func newSessionCgroup(root string) (*sessionCgroup, error) {
	if err := os.WriteFile(filepath.Join(root, "cgroup.subtree_control"), []byte("+pids +memory"), 0); err != nil {
		return nil, fmt.Errorf("enable the pids and memory controllers: %w", err)
	}
	dir := filepath.Join(root, sessionCgroupParent)
	procs := filepath.Join(dir, "procs")
	if err := os.MkdirAll(procs, 0o755); err != nil {
		return nil, err
	}
	meminfo, err := readCounters("/proc/meminfo", 1024)
	if err != nil {
		return nil, err
	}
	available := meminfo["MemAvailable"]
	if available <= sessionReserve {
		return nil, fmt.Errorf("cap the session's memory: %d MiB is available and the helper keeps %d MiB", available>>20, sessionReserve>>20)
	}
	limit := strconv.FormatUint(available-sessionReserve, 10)
	if err = os.WriteFile(filepath.Join(dir, "memory.max"), []byte(limit), 0); err != nil {
		return nil, fmt.Errorf("cap the session's memory: %w", err)
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

// freeze sets the session's cgroup.freeze and waits until cgroup.events
// reports it: the kernel freezes and thaws processes asynchronously, and a
// task in a killable wait, such as a slow home request, holds a freeze up.
// A freeze that does not complete is thawed back, as runc does, so a failed
// pause leaves the session running.
func (c *sessionCgroup) freeze(ctx context.Context, frozen bool) error {
	value := "0"
	if frozen {
		value = "1"
	}
	control := filepath.Join(c.dir, "cgroup.freeze")
	if err := os.WriteFile(control, []byte(value), 0); err != nil {
		return err
	}
	for {
		events, err := os.ReadFile(filepath.Join(c.dir, "cgroup.events"))
		if err == nil && slices.Contains(strings.Split(string(events), "\n"), "frozen "+value) {
			return nil
		}
		if err == nil {
			select {
			case <-ctx.Done():
				err = ctx.Err()
			case <-time.After(10 * time.Millisecond):
				continue
			}
		}
		if frozen {
			err = errors.Join(err, os.WriteFile(control, []byte("0"), 0))
		}
		return err
	}
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
