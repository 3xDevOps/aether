package sysstats

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/runtime"
)

type fixture struct{ proc, sys string }

func newFixture(t *testing.T) fixture {
	t.Helper()
	f := fixture{proc: t.TempDir(), sys: t.TempDir()}
	f.write(t, "meminfo", "MemTotal: 1000 kB\nMemFree: 100 kB\nMemAvailable: 400 kB\nSwapTotal: 200 kB\nSwapFree: 150 kB\n")
	f.write(t, "loadavg", "1.50 2.50 3.50 1/200 999\n")
	f.write(t, "uptime", "1234.56 5000.00\n")
	// eth0 is physical, veth0 is virtual; sda is a disk, sda1 a partition.
	mustMkdir(t, f.sys, "devices", "pci", "net", "eth0")
	mustMkdir(t, f.sys, "devices", "virtual", "net", "veth0")
	mustMkdir(t, f.sys, "class", "net")
	mustSymlink(t, filepath.Join(f.sys, "devices", "pci", "net", "eth0"), filepath.Join(f.sys, "class", "net", "eth0"))
	mustSymlink(t, filepath.Join(f.sys, "devices", "virtual", "net", "veth0"), filepath.Join(f.sys, "class", "net", "veth0"))
	mustMkdir(t, f.sys, "block", "sda", "device")
	mustMkdir(t, f.sys, "block", "loop0")
	f.counters(t, 0, 0, 0, 0, 0)
	return f
}

func mustMkdir(t *testing.T, parts ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(parts...), 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

func (f fixture) write(t *testing.T, rel, body string) {
	t.Helper()
	path := filepath.Join(f.proc, rel)
	mustMkdir(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// counters writes cumulative values: busy CPU ticks, idle ticks, network
// bytes, and disk sectors.
func (f fixture) counters(t *testing.T, user, system, idle, netBytes, sectors uint64) {
	t.Helper()
	f.write(t, "stat", fmt.Sprintf("cpu  %d 0 %d %d 0 0 0 0 0 0\ncpu0 1 0 1 1 0 0 0 0 0 0\ncpu1 1 0 1 1 0 0 0 0 0 0\nintr 1\n", user, system, idle))
	f.write(t, "net/dev", fmt.Sprintf("Inter-|   Receive\n face |bytes packets errs drop fifo frame compressed multicast|bytes packets\n    lo: 999999 1 0 0 0 0 0 0 999999 1 0 0 0 0 0 0\n  eth0: %d 1 0 0 0 0 0 0 %d 1 0 0 0 0 0 0\n veth0: 777 1 0 0 0 0 0 0 777 1 0 0 0 0 0 0\n", netBytes, netBytes/2))
	f.write(t, "diskstats", fmt.Sprintf("   8       0 sda 1 0 %d 0 1 0 %d 0 0 0 0\n   8       1 sda1 1 0 99999 0 1 0 99999 0 0 0 0\n   7       0 loop0 1 0 99999 0 1 0 99999 0 0 0 0\n", sectors, sectors*2))
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func TestHostSamplesComeFromCounterDeltas(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	clk := &clock{t: time.Unix(1000, 0)}
	s := New(Options{ProcRoot: f.proc, SysRoot: f.sys, Now: clk.Now, GOOS: "linux"})

	f.counters(t, 100, 50, 850, 1000, 100)
	s.sampleHost()
	if got := s.Read(time.Time{}).Samples; len(got) != 0 {
		t.Fatalf("first read must only seed counters, got %d samples", len(got))
	}

	clk.Advance(5 * time.Second)
	f.counters(t, 200, 80, 920, 51000, 300)
	f.write(t, "pressure/cpu", "some avg10=2.50 avg60=1.00 avg300=0.50 total=1\nfull avg10=9.00 avg60=0 avg300=0 total=0\n")
	s.sampleHost()

	snap := s.Read(time.Time{})
	if snap.HostError != "" || len(snap.Samples) != 1 {
		t.Fatalf("host error %q, %d samples", snap.HostError, len(snap.Samples))
	}
	h := snap.Samples[0]
	// 200 ticks elapsed: 100 user, 30 system.
	if h.CPUUser != 50 || h.CPUSystem != 15 || h.CPUIOWait != 0 {
		t.Errorf("cpu = %v/%v/%v, want 50/15/0", h.CPUUser, h.CPUSystem, h.CPUIOWait)
	}
	if h.NetRxBps != 10000 || h.NetTxBps != 5000 {
		t.Errorf("net = %v/%v, want 10000/5000 (lo and virtual interfaces excluded)", h.NetRxBps, h.NetTxBps)
	}
	if h.DiskReadBps != 200*512/5.0 || h.DiskWriteBps != 400*512/5.0 {
		t.Errorf("disk = %v/%v (partitions and virtual devices excluded)", h.DiskReadBps, h.DiskWriteBps)
	}
	if h.MemoryUsedBytes != 600*1024 || h.MemoryAvailableBytes != 400*1024 || h.MemoryFreeBytes != 100*1024 || h.SwapUsedBytes != 50*1024 {
		t.Errorf("memory = %+v", h)
	}
	if h.Load1 != 1.5 || h.Load15 != 3.5 {
		t.Errorf("load = %v/%v", h.Load1, h.Load15)
	}
	if h.PressureCPU == nil || *h.PressureCPU != 2.5 || h.PressureMemory != nil || h.PressureIO != nil {
		t.Errorf("pressure = %v/%v/%v, want only cpu", h.PressureCPU, h.PressureMemory, h.PressureIO)
	}
	if snap.Host.CPUCores != 2 || snap.Host.MemoryTotalBytes != 1000*1024 || snap.Host.SwapTotalBytes != 200*1024 || snap.Host.UptimeSeconds != 1234.56 {
		t.Errorf("host = %+v", snap.Host)
	}

	clk.Advance(5 * time.Second)
	f.counters(t, 10, 5, 20, 100, 10) // every counter went backwards
	s.sampleHost()
	snap = s.Read(h.At)
	if len(snap.Samples) != 1 {
		t.Fatalf("since filter returned %d samples, want 1", len(snap.Samples))
	}
	if r := snap.Samples[0]; r.CPUUser != 0 || r.NetRxBps != 0 || r.DiskWriteBps != 0 {
		t.Errorf("counter reset produced %+v, want zeros", r)
	}
}

func TestHostErrorKeepsTheRealCauseAndRecovers(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	s := New(Options{ProcRoot: f.proc, SysRoot: f.sys, GOOS: "linux"})
	if err := os.Remove(filepath.Join(f.proc, "loadavg")); err != nil {
		t.Fatal(err)
	}
	s.sampleHost()
	if got := s.Read(time.Time{}).HostError; !strings.Contains(got, "loadavg") || !strings.Contains(got, "no such file") {
		t.Errorf("host error = %q", got)
	}
	f.write(t, "loadavg", "0 0 0 1/1 1\n")
	s.sampleHost()
	if got := s.Read(time.Time{}).HostError; got != "" {
		t.Errorf("host error after recovery = %q", got)
	}

	other := New(Options{GOOS: "darwin"})
	other.sampleHost()
	if got, want := other.Read(time.Time{}).HostError, "system stats need a Linux host; this server runs on darwin"; got != want {
		t.Errorf("host error = %q, want %q", got, want)
	}
}

type fakeSource struct {
	calls atomic.Int64
	mu    sync.Mutex
	stats []runtime.ContainerStat
	err   error
}

func (f *fakeSource) ContainerStats(context.Context) ([]runtime.ContainerStat, error) {
	f.calls.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stats, f.err
}

func TestContainerRatesNeedTwoSamples(t *testing.T) {
	t.Parallel()
	clk := &clock{t: time.Unix(1000, 0)}
	s := New(Options{Source: &fakeSource{}, Now: clk.Now})
	read := time.Unix(1000, 0)
	sample := func(cpuNs, periods, throttled, rx uint64) {
		s.finishContainers([]runtime.ContainerStat{{
			ID: "c1", Read: read, CPUUsageNanos: cpuNs, CPUPeriods: periods, CPUThrottledPeriods: throttled,
			NetRxBytes: rx, MemoryBytes: 5,
		}}, nil)
	}

	sample(1e9, 10, 1, 0)
	first := s.Read(time.Time{}).Containers
	if len(first) != 1 || first[0].Rates != nil || first[0].MemoryBytes != 5 {
		t.Fatalf("first sample = %+v, want gauges without rates", first)
	}

	read = read.Add(5 * time.Second)
	clk.Advance(5 * time.Second)
	sample(6e9, 30, 6, 5000)
	r := s.Read(time.Time{}).Containers[0].Rates
	if r == nil || r.CPUCores != 1 || r.NetRxBps != 1000 || r.ThrottledPercent == nil || *r.ThrottledPercent != 25 {
		t.Fatalf("rates = %+v, want 1 core, 1000 B/s, 25%% throttled", r)
	}

	clk.Advance(11 * time.Second)
	if snap := s.Read(time.Time{}); len(snap.Containers) != 0 || !snap.ContainersAt.IsZero() {
		t.Errorf("snapshot older than two intervals must carry no containers, got %+v", snap)
	}

	read = read.Add(time.Minute)
	clk.Advance(time.Minute)
	s.finishContainers([]runtime.ContainerStat{{ID: "c1", Read: read, CPUUsageNanos: 9e10}}, errors.New("1 of 2 containers could not be sampled: gone"))
	snap := s.Read(time.Time{})
	if len(snap.Containers) != 1 || snap.Containers[0].Rates != nil {
		t.Errorf("after a watch gap = %+v, want the container without rates", snap.Containers)
	}
	if snap.ContainersError != "1 of 2 containers could not be sampled: gone" {
		t.Errorf("partial failure error = %q", snap.ContainersError)
	}
}

func TestContainersAreSampledOnlyWhileWatched(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	src := &fakeSource{stats: []runtime.ContainerStat{{ID: "c1"}}}
	s := New(Options{ProcRoot: f.proc, SysRoot: f.sys, Source: src, GOOS: "linux"})
	s.interval, s.delay, s.ttl = 40*time.Millisecond, 10*time.Millisecond, 150*time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)

	time.Sleep(100 * time.Millisecond)
	if n := src.calls.Load(); n != 0 {
		t.Fatalf("idle sampler read containers %d times", n)
	}

	s.Read(time.Time{})
	waitFor(t, "wake sample and the sample after it", func() bool { return src.calls.Load() >= 2 })

	// Reads keep it watched; once they stop, sampling stops after the TTL.
	waitFor(t, "ticks while watched", func() bool { return src.calls.Load() >= 5 })
	time.Sleep(s.ttl + 2*s.interval)
	stopped := src.calls.Load()
	time.Sleep(4 * s.interval)
	if n := src.calls.Load(); n != stopped {
		t.Errorf("sampling continued after the watch expired: %d -> %d", stopped, n)
	}

	s.Read(time.Time{})
	waitFor(t, "a Read while idle wakes the sampler", func() bool { return src.calls.Load() > stopped })
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
