//go:build kvm

package vm

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/runtime"
)

// TestKVMColdMemoryReturnsToTheHost checks DAMON_RECLAIM in a guest whose
// page cache fills it, below the 20% free at which its default watermarks
// stop it: a third of a file read once leaves the guest, and the host's
// memory, within twice min_age and 3 minutes, while a file Main reads every
// 2 seconds stays cached and kdamond stays under 2% of a vCPU. The rest
// follows more slowly, as DAMON splits it from memory in use.
func TestKVMColdMemoryReturnsToTheHost(t *testing.T) {
	e := kvmEngine(t)
	key := "run-cold-memory"
	t.Cleanup(func() { _ = e.destroy(context.Background(), key) })
	spec := runtime.Spec{
		Image: "docker.io/library/ubuntu:24.04", CreationKey: key,
		Command: []string{"/bin/sh", "-c", "while :; do cat /var/tmp/hot >/dev/null 2>&1; sleep 2; done"},
		Env:     map[string]string{"AETHER_RUN_ID": key}, CPULimit: 1, MemoryLimitBytes: 1 << 30,
	}
	if _, err := e.create(t.Context(), key, spec, options{Offline: true}); err != nil {
		t.Fatal(err)
	}
	if err := e.start(t.Context(), key); err != nil {
		t.Fatal(err)
	}
	params := guestFields(t, e, key, "cd /sys/module/damon_reclaim/parameters && cat enabled min_age monitor_region_end skip_anon kdamond_pid; "+
		"grep 'System RAM' /proc/iomem | tail -1 | cut -d' ' -f1; awk '/^MemTotal:/ {print $2}' /proc/meminfo", 7)
	if params[0] != "Y" {
		t.Fatalf("DAMON_RECLAIM is not running: enabled=%s", params[0])
	}
	minAge := time.Duration(parseUint(t, params[1])) * time.Microsecond
	_, ramLast, _ := strings.Cut(params[5], "-")
	if last, err := strconv.ParseUint(ramLast, 16, 64); err != nil || last+1 != parseUint(t, params[2]) {
		t.Errorf("DAMON_RECLAIM watches up to %s, the guest's RAM ends at %s+1", params[2], ramLast)
	}
	if params[3] != "Y" {
		t.Errorf("DAMON_RECLAIM has skip_anon=%s in a guest without swap", params[3])
	}
	kdamond, memTotalKiB := params[4], parseUint(t, params[6])

	const hotMiB = 64
	coldMiB := memTotalKiB * 9 / 10 >> 10
	guestFields(t, e, key, fmt.Sprintf("dd if=/dev/urandom of=/var/tmp/cold bs=1M count=%d iflag=fullblock oflag=direct status=none && "+
		"dd if=/dev/urandom of=/var/tmp/hot bs=1M count=%d iflag=fullblock oflag=direct status=none", coldMiB, hotMiB), 0)
	time.Sleep(5 * time.Second)
	before := guestRAMOnHost(t, key)
	reclaimed := func() uint64 {
		return parseUint(t, guestFields(t, e, key, "cat /sys/module/damon_reclaim/parameters/bytes_reclaimed_regions", 1)[0])
	}
	reclaimedBefore := reclaimed()
	free := parseUint(t, guestFields(t, e, key, "cat /var/tmp/cold >/dev/null && awk '/^MemFree:/ {print $2}' /proc/meminfo", 1)[0])
	read := time.Now()
	if free*5 > memTotalKiB {
		t.Fatalf("reading %d MiB left %d of %d KiB free, not under 20%%", coldMiB, free, memTotalKiB)
	}
	held := guestRAMOnHost(t, key)
	if held < before+coldMiB<<20/2 {
		t.Fatalf("reading %d MiB took the host's share of the guest from %d to only %d bytes", coldMiB, before, held)
	}
	// The bytes read from the root disk, which holds the hot file; the
	// helper's binary, whose cold pages leave too, is on a disk of its own.
	rootReads := func() uint64 {
		return parseUint(t, guestFields(t, e, key, "awk '{print $3 * 512}' $(dirname $(grep -lx root /sys/block/vd*/serial))/stat", 1)[0])
	}
	// Main reloads within 2 seconds whatever of its file the read pushed out.
	time.Sleep(5 * time.Second)
	readIn := rootReads()
	deadline := read.Add(2*minAge + 3*time.Minute)
	for now := guestRAMOnHost(t, key); now > held-coldMiB<<20/3; now = guestRAMOnHost(t, key) {
		if time.Now().After(deadline) {
			t.Fatalf("%s after reading %d MiB the host holds %d bytes of the guest, %d right after the read and %d before it (min_age %s)",
				time.Since(read).Round(time.Second), coldMiB, now, held, before, minAge)
		}
		time.Sleep(5 * time.Second)
	}
	t.Logf("reading %d MiB left %d of %d KiB free; the host's share of the guest went %d -> %d -> %d bytes, a third back %s after the read (min_age %s)",
		coldMiB, free, memTotalKiB, before, held, guestRAMOnHost(t, key), time.Since(read).Round(time.Second), minAge)

	if n := reclaimed() - reclaimedBefore; n < coldMiB<<20/3 {
		t.Errorf("DAMON_RECLAIM paged out %d bytes after the read, under a third of the %d MiB file that left", n, coldMiB)
	}
	if n := rootReads() - readIn; n > hotMiB<<20/8 {
		t.Errorf("while the cold file left, the guest read %d bytes from its root disk: DAMON_RECLAIM evicted the file Main keeps reading", n)
	}
	cpu := guestFields(t, e, key, "cut -d' ' -f14,15 /proc/"+kdamond+"/stat; cut -d' ' -f1 /proc/uptime", 3)
	ticks := parseUint(t, cpu[0]) + parseUint(t, cpu[1])
	uptime, err := strconv.ParseFloat(cpu[2], 64)
	if err != nil {
		t.Fatal(err)
	}
	share := float64(ticks) / 100 / uptime
	if share > 0.02 {
		t.Errorf("kdamond used %.2f%% of a vCPU", share*100)
	}
	t.Logf("kdamond used %.2f%% of a vCPU over %.0f s", share*100, uptime)
}

func guestFields(t *testing.T, e *engine, key, script string, n int) []string {
	t.Helper()
	out, code := vmRun(t, e, key, script)
	fields := strings.Fields(out)
	if code != 0 || len(fields) != n {
		t.Fatalf("%q exited %d with %q, want %d fields", script, code, out, n)
	}
	return fields
}

func parseUint(t *testing.T, s string) uint64 {
	t.Helper()
	v, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// guestRAMOnHost is the guest memory the host holds for the VM: Cloud
// Hypervisor's shared memory, charged to the VM's unit.
func guestRAMOnHost(t *testing.T, key string) uint64 {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("/sys/fs/cgroup", runtime.SessionSlice, unitName(key), "memory.stat"))
	if err != nil {
		t.Fatal(err)
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		if v, ok := strings.CutPrefix(line, "shmem "); ok {
			return parseUint(t, v)
		}
	}
	t.Fatalf("memory.stat of %s has no shmem", key)
	return 0
}
