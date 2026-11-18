package helper

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const damonReclaimParams = "/sys/module/damon_reclaim/parameters"

// startDAMONReclaim turns on the kernel's DAMON_RECLAIM, which pages out
// memory left untouched for a minute so free page reporting returns it to
// the host; docs/vm-guest.md#memory-the-guest-returns says why each value.
// It reads /proc/swaps to skip anonymous pages in a guest without swap, so
// it runs after swap is turned on. A kernel built without it is skipped.
func startDAMONReclaim() error {
	if _, err := os.Stat(damonReclaimParams); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	end, err := ramEnd()
	if err != nil {
		return err
	}
	swaps, err := os.ReadFile("/proc/swaps")
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	skipAnon := "Y"
	if strings.Count(string(swaps), "\n") > 1 {
		skipAnon = "N"
	}
	for _, p := range []struct{ name, value string }{
		{"min_age", "60000000"},
		{"wmarks_high", "1000"}, {"wmarks_mid", "1000"}, {"wmarks_low", "0"},
		{"sample_interval", "50000"}, {"aggr_interval", "1000000"},
		{"monitor_region_end", strconv.FormatUint(end, 10)},
		{"skip_anon", skipAnon},
		{"enabled", "Y"},
	} {
		if err := os.WriteFile(filepath.Join(damonReclaimParams, p.name), []byte(p.value), 0); err != nil {
			return err
		}
	}
	return nil
}

// ramEnd is where the guest's highest System RAM ends. Cloud Hypervisor
// puts RAM past 3 GiB at 4 GiB, and DAMON_RECLAIM watches one range, by
// default only the larger piece.
func ramEnd() (uint64, error) {
	iomem, err := os.ReadFile("/proc/iomem")
	if err != nil {
		return 0, err
	}
	var end uint64
	for line := range strings.Lines(string(iomem)) {
		span, ok := strings.CutSuffix(strings.TrimSpace(line), " : System RAM")
		if !ok {
			continue
		}
		_, last, _ := strings.Cut(span, "-")
		v, err := strconv.ParseUint(last, 16, 64)
		if err != nil {
			return 0, fmt.Errorf("/proc/iomem: %w", err)
		}
		end = max(end, v+1)
	}
	if end == 0 {
		return 0, errors.New("/proc/iomem lists no System RAM")
	}
	return end, nil
}
