package sysstats

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// HostSample is one interval of host usage. Pressure fields are nil when the
// kernel does not expose /proc/pressure.
type HostSample struct {
	At                   time.Time
	CPUUser              float64
	CPUSystem            float64
	CPUIOWait            float64
	CPUSteal             float64
	MemoryUsedBytes      uint64
	MemoryAvailableBytes uint64
	MemoryFreeBytes      uint64
	SwapUsedBytes        uint64
	Load1                float64
	Load5                float64
	Load15               float64
	NetRxBps             float64
	NetTxBps             float64
	DiskReadBps          float64
	DiskWriteBps         float64
	PressureCPU          *float64
	PressureMemory       *float64
	PressureIO           *float64
}

type HostInfo struct {
	CPUCores         int
	MemoryTotalBytes uint64
	SwapTotalBytes   uint64
	UptimeSeconds    float64
}

type cpuTimes struct{ user, system, iowait, steal, total uint64 }

type ioCounters struct{ rx, tx uint64 }

type hostReading struct {
	at       time.Time
	info     HostInfo
	cpu      cpuTimes
	net      map[string]ioCounters
	disk     map[string]ioCounters
	sample   HostSample
	pressure [3]*float64
}

func readHost(procRoot, sysRoot string, at time.Time) (hostReading, error) {
	r := hostReading{at: at}
	if err := r.readStat(procRoot); err != nil {
		return r, err
	}
	if err := r.readMeminfo(procRoot); err != nil {
		return r, err
	}
	if err := r.readLoad(procRoot); err != nil {
		return r, err
	}
	if err := r.readUptime(procRoot); err != nil {
		return r, err
	}
	if err := r.readNet(procRoot, sysRoot); err != nil {
		return r, err
	}
	if err := r.readDisk(procRoot, sysRoot); err != nil {
		return r, err
	}
	for i, name := range [...]string{"cpu", "memory", "io"} {
		v, err := readPressure(filepath.Join(procRoot, "pressure", name))
		if err != nil {
			return r, err
		}
		r.pressure[i] = v
	}
	return r, nil
}

func readProc(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return b, nil
}

func (r *hostReading) readStat(procRoot string) error {
	path := filepath.Join(procRoot, "stat")
	b, err := readProc(path)
	if err != nil {
		return err
	}
	sc := bufio.NewScanner(bytes.NewReader(b))
	found := false
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 0 || !strings.HasPrefix(f[0], "cpu") {
			continue
		}
		if f[0] != "cpu" {
			r.info.CPUCores++
			continue
		}
		if len(f) < 9 {
			return fmt.Errorf("parse %s: cpu line has %d fields, want at least 9", path, len(f))
		}
		var v [8]uint64
		for i := range v {
			if v[i], err = strconv.ParseUint(f[i+1], 10, 64); err != nil {
				return fmt.Errorf("parse %s: %w", path, err)
			}
		}
		r.cpu = cpuTimes{
			user:   v[0] + v[1],
			system: v[2] + v[5] + v[6],
			iowait: v[4],
			steal:  v[7],
		}
		for _, x := range v {
			r.cpu.total += x
		}
		found = true
	}
	if !found {
		return fmt.Errorf("parse %s: no aggregate cpu line", path)
	}
	return nil
}

func (r *hostReading) readMeminfo(procRoot string) error {
	path := filepath.Join(procRoot, "meminfo")
	b, err := readProc(path)
	if err != nil {
		return err
	}
	kb := map[string]uint64{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		name, rest, ok := strings.Cut(sc.Text(), ":")
		f := strings.Fields(rest)
		if !ok || len(f) == 0 {
			continue
		}
		if v, err := strconv.ParseUint(f[0], 10, 64); err == nil {
			kb[name] = v * 1024
		}
	}
	for _, name := range [...]string{"MemTotal", "MemFree", "MemAvailable", "SwapTotal", "SwapFree"} {
		if _, ok := kb[name]; !ok {
			return fmt.Errorf("parse %s: %s is missing", path, name)
		}
	}
	r.info.MemoryTotalBytes, r.info.SwapTotalBytes = kb["MemTotal"], kb["SwapTotal"]
	r.sample.MemoryUsedBytes = sub(kb["MemTotal"], kb["MemAvailable"])
	r.sample.MemoryAvailableBytes = kb["MemAvailable"]
	r.sample.MemoryFreeBytes = kb["MemFree"]
	r.sample.SwapUsedBytes = sub(kb["SwapTotal"], kb["SwapFree"])
	return nil
}

func (r *hostReading) readLoad(procRoot string) error {
	path := filepath.Join(procRoot, "loadavg")
	b, err := readProc(path)
	if err != nil {
		return err
	}
	f := strings.Fields(string(b))
	if len(f) < 3 {
		return fmt.Errorf("parse %s: want three load averages, got %q", path, strings.TrimSpace(string(b)))
	}
	for i, dst := range [...]*float64{&r.sample.Load1, &r.sample.Load5, &r.sample.Load15} {
		if *dst, err = strconv.ParseFloat(f[i], 64); err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}
	}
	return nil
}

func (r *hostReading) readUptime(procRoot string) error {
	path := filepath.Join(procRoot, "uptime")
	b, err := readProc(path)
	if err != nil {
		return err
	}
	f := strings.Fields(string(b))
	if len(f) == 0 {
		return fmt.Errorf("parse %s: empty", path)
	}
	if r.info.UptimeSeconds, err = strconv.ParseFloat(f[0], 64); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	return nil
}

func (r *hostReading) readNet(procRoot, sysRoot string) error {
	path := filepath.Join(procRoot, "net", "dev")
	b, err := readProc(path)
	if err != nil {
		return err
	}
	r.net = map[string]ioCounters{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		name, rest, ok := strings.Cut(sc.Text(), ":")
		f := strings.Fields(rest)
		name = strings.TrimSpace(name)
		if !ok || len(f) < 9 || name == "lo" {
			continue
		}
		link, err := filepath.EvalSymlinks(filepath.Join(sysRoot, "class", "net", name))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("resolve interface %s: %w", name, err)
		}
		if strings.Contains(filepath.ToSlash(link), "/devices/virtual/") {
			continue
		}
		rx, rxErr := strconv.ParseUint(f[0], 10, 64)
		tx, txErr := strconv.ParseUint(f[8], 10, 64)
		if err := errors.Join(rxErr, txErr); err != nil {
			return fmt.Errorf("parse %s: interface %s: %w", path, name, err)
		}
		r.net[name] = ioCounters{rx, tx}
	}
	return nil
}

func (r *hostReading) readDisk(procRoot, sysRoot string) error {
	path := filepath.Join(procRoot, "diskstats")
	b, err := readProc(path)
	if err != nil {
		return err
	}
	r.disk = map[string]ioCounters{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 10 {
			continue
		}
		if _, err := os.Stat(filepath.Join(sysRoot, "block", f[2], "device")); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return fmt.Errorf("stat block device %s: %w", f[2], err)
		}
		read, readErr := strconv.ParseUint(f[5], 10, 64)
		written, writeErr := strconv.ParseUint(f[9], 10, 64)
		if err := errors.Join(readErr, writeErr); err != nil {
			return fmt.Errorf("parse %s: device %s: %w", path, f[2], err)
		}
		r.disk[f[2]] = ioCounters{read * 512, written * 512}
	}
	return nil
}

// readPressure returns nil when the kernel has no pressure file. A kernel
// booted with psi=0 keeps the files but fails reads with EOPNOTSUPP.
func readPressure(path string) (*float64, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.EOPNOTSUPP) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	for line := range strings.SplitSeq(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 || f[0] != "some" {
			continue
		}
		for _, field := range f[1:] {
			if v, ok := strings.CutPrefix(field, "avg10="); ok {
				p, err := strconv.ParseFloat(v, 64)
				if err != nil {
					return nil, fmt.Errorf("parse %s: %w", path, err)
				}
				return &p, nil
			}
		}
	}
	return nil, fmt.Errorf("parse %s: no \"some avg10=\" line", path)
}

// sampleSince turns two readings into one interval. A counter that went backwards
// contributes zero for the interval.
func (r hostReading) sampleSince(prev hostReading) HostSample {
	s := r.sample
	s.At = r.at
	if total := float64(delta(prev.cpu.total, r.cpu.total)); total > 0 {
		pct := func(a, b uint64) float64 { return float64(delta(a, b)) / total * 100 }
		s.CPUUser = pct(prev.cpu.user, r.cpu.user)
		s.CPUSystem = pct(prev.cpu.system, r.cpu.system)
		s.CPUIOWait = pct(prev.cpu.iowait, r.cpu.iowait)
		s.CPUSteal = pct(prev.cpu.steal, r.cpu.steal)
	}
	secs := r.at.Sub(prev.at).Seconds()
	s.NetRxBps, s.NetTxBps = ioRate(prev.net, r.net, secs)
	s.DiskReadBps, s.DiskWriteBps = ioRate(prev.disk, r.disk, secs)
	s.PressureCPU, s.PressureMemory, s.PressureIO = r.pressure[0], r.pressure[1], r.pressure[2]
	return s
}

// ioRate sums per-device deltas, so a device that appears or vanishes never
// counts its whole lifetime total as one interval's traffic.
func ioRate(prev, cur map[string]ioCounters, secs float64) (rx, tx float64) {
	if secs <= 0 {
		return 0, 0
	}
	var rxBytes, txBytes uint64
	for name, c := range cur {
		p, ok := prev[name]
		if !ok {
			continue
		}
		rxBytes += delta(p.rx, c.rx)
		txBytes += delta(p.tx, c.tx)
	}
	return float64(rxBytes) / secs, float64(txBytes) / secs
}

func delta(prev, cur uint64) uint64 { return sub(cur, prev) }

func sub(a, b uint64) uint64 {
	if b > a {
		return 0
	}
	return a - b
}
