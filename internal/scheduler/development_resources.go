package scheduler

import (
	"errors"
	"os"
	"strconv"
	"strings"
)

// Linux reports available memory including reclaimable page cache. Bound the
// reservation by the server's cgroup too, rather than admitting against a host
// total that a containerized server cannot use.
func developmentMemory() (available, total int64, err error) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		value, parseErr := strconv.ParseInt(fields[1], 10, 64)
		if parseErr != nil {
			continue
		}
		switch fields[0] {
		case "MemAvailable:":
			available = value * 1024
		case "MemTotal:":
			total = value * 1024
		}
	}
	if available <= 0 || total <= 0 {
		return 0, 0, errors.New("host memory capacity is unavailable")
	}
	maxData, maxErr := os.ReadFile("/sys/fs/cgroup/memory.max")
	currentData, currentErr := os.ReadFile("/sys/fs/cgroup/memory.current")
	if maxErr == nil && currentErr == nil {
		maximum, parseErr := strconv.ParseInt(strings.TrimSpace(string(maxData)), 10, 64)
		current, currentErr := strconv.ParseInt(strings.TrimSpace(string(currentData)), 10, 64)
		if parseErr == nil && currentErr == nil && maximum > 0 {
			if maximum < total {
				total = maximum
			}
			free := maximum - current
			if free < available {
				available = free
			}
		}
	}
	return available, total, nil
}
