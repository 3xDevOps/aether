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
// With swap it pages out anonymous memory too. It skips a kernel that lacks
// any of the parameters, and one running MGLRU, under which a read no
// longer marks a page used for DAMON.
func startDAMONReclaim(swap bool) error {
	if mglru, err := os.ReadFile("/sys/kernel/mm/lru_gen/enabled"); err == nil && strings.TrimSpace(string(mglru)) != "0x0000" {
		return nil
	}
	iomem, err := os.ReadFile("/proc/iomem")
	if err != nil {
		return err
	}
	end, err := ramEnd(string(iomem))
	if err != nil {
		return err
	}
	skipAnon := "Y"
	if swap {
		skipAnon = "N"
	}
	params := []struct{ name, value string }{
		{"min_age", "60000000"},
		{"wmarks_high", "1000"}, {"wmarks_mid", "1000"}, {"wmarks_low", "0"},
		{"sample_interval", "50000"}, {"aggr_interval", "1000000"},
		{"monitor_region_end", strconv.FormatUint(end, 10)},
		{"skip_anon", skipAnon},
		{"enabled", "Y"},
	}
	for _, p := range params {
		if _, err := os.Stat(filepath.Join(damonReclaimParams, p.name)); errors.Is(err, os.ErrNotExist) {
			return nil
		}
	}
	for _, p := range params {
		if err := os.WriteFile(filepath.Join(damonReclaimParams, p.name), []byte(p.value), 0); err != nil {
			return err
		}
	}
	return nil
}

// ramEnd is where the highest System RAM range in /proc/iomem ends. Cloud
// Hypervisor puts RAM past 3 GiB at 4 GiB, and DAMON_RECLAIM watches one
// range, by default only the larger piece.
func ramEnd(iomem string) (uint64, error) {
	var end uint64
	for line := range strings.Lines(iomem) {
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
