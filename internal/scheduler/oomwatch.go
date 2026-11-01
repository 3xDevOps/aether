package scheduler

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/runtime"
)

// oomWatcher tracks each live container's cgroup v2 oom_kill counter. The
// first reading is the baseline; a later increase is reported.
type oomWatcher struct {
	checked bool
	enabled bool
	cgroup  string
	proc    string
	paths   map[runtime.ID]string
	kills   map[runtime.ID]uint64
}

type memoryStats struct {
	oomKills uint64
	peak     uint64
	max      uint64
}

func (w *oomWatcher) enable() bool {
	if !w.checked {
		w.checked = true
		w.cgroup, w.proc = "/sys/fs/cgroup", "/proc"
		_, err := os.Stat(filepath.Join(w.cgroup, "cgroup.controllers"))
		w.enabled = err == nil
		w.paths = map[runtime.ID]string{}
		w.kills = map[runtime.ID]uint64{}
	}
	return w.enabled
}

// oomProbeTimeout bounds one container's Docker lookup so a stalled reply
// cannot block the sweep loop.
const oomProbeTimeout = 5 * time.Second

func (s *Scheduler) checkOOMKills(ctx context.Context) {
	pids, ok := s.cfg.Runtime.(runtime.InitPIDRuntime)
	if !ok || !s.oom.enable() {
		return
	}
	type target struct {
		container runtime.ID
		workspace domain.WorkspaceID
		run       domain.RunID
	}
	s.mu.Lock()
	var targets []target
	for _, e := range s.runs {
		if !e.finalizing && e.containerID != "" {
			targets = append(targets, target{e.containerID, e.workspaceID, e.runID})
		}
	}
	s.mu.Unlock()

	seen := map[runtime.ID]bool{}
	for _, t := range targets {
		seen[t.container] = true
		probeCtx, cancel := context.WithTimeout(ctx, oomProbeTimeout)
		stats, err := s.oom.read(probeCtx, pids, t.container)
		cancel()
		if err != nil {
			s.oom.forget(t.container)
			if !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, runtime.ErrNotFound) {
				slog.Warn("scheduler: read container memory events", "run", t.run, "container", t.container, "error", err)
			}
			continue
		}
		prev, known := s.oom.kills[t.container]
		s.oom.kills[t.container] = stats.oomKills
		if known && stats.oomKills > prev {
			s.publishTimeline(ctx, t.workspace, t.run, "", events.TimelineNote, oomMessage(stats))
		}
	}
	for id := range s.oom.paths {
		if !seen[id] {
			s.oom.forget(id)
		}
	}
}

func (w *oomWatcher) forget(id runtime.ID) {
	delete(w.paths, id)
	delete(w.kills, id)
}

func (w *oomWatcher) read(ctx context.Context, pids runtime.InitPIDRuntime, id runtime.ID) (memoryStats, error) {
	path, ok := w.paths[id]
	if !ok {
		pid, err := pids.InitPID(ctx, id)
		if err != nil {
			return memoryStats{}, err
		}
		if pid <= 0 {
			return memoryStats{}, fmt.Errorf("container %q is not running: %w", id, fs.ErrNotExist)
		}
		if path, err = cgroupPath(w.proc, pid); err != nil {
			return memoryStats{}, err
		}
		w.paths[id] = path
	}
	return readMemoryStats(filepath.Join(w.cgroup, path))
}

// cgroupPath returns the cgroup v2 path of pid from its "0::<path>" line.
func cgroupPath(procRoot string, pid int) (string, error) {
	f, err := os.Open(filepath.Join(procRoot, strconv.Itoa(pid), "cgroup"))
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if path, ok := strings.CutPrefix(sc.Text(), "0::"); ok {
			return path, nil
		}
	}
	if err := sc.Err(); err != nil {
		return "", fmt.Errorf("read cgroup of pid %d: %w", pid, err)
	}
	return "", fmt.Errorf("pid %d has no cgroup v2 entry", pid)
}

// readMemoryStats reads memory.events, and memory.peak and memory.max when
// the kernel provides them. A max of "max" (unlimited) reads as 0.
func readMemoryStats(dir string) (memoryStats, error) {
	var st memoryStats
	data, err := os.ReadFile(filepath.Join(dir, "memory.events"))
	if err != nil {
		return st, err
	}
	found := false
	for line := range strings.SplitSeq(string(data), "\n") {
		if v, ok := strings.CutPrefix(line, "oom_kill "); ok {
			if st.oomKills, err = strconv.ParseUint(strings.TrimSpace(v), 10, 64); err != nil {
				return st, fmt.Errorf("parse oom_kill in %s: %w", dir, err)
			}
			found = true
		}
	}
	if !found {
		return st, fmt.Errorf("%s/memory.events has no oom_kill field", dir)
	}
	st.peak = readUint(filepath.Join(dir, "memory.peak"))
	st.max = readUint(filepath.Join(dir, "memory.max"))
	return st, nil
}

func readUint(path string) uint64 {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	v, err := strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
	if err != nil {
		return 0
	}
	return v
}

func oomMessage(st memoryStats) string {
	msg := "Out of memory: the kernel killed a process in this session"
	if st.max > 0 {
		msg += " at its " + formatGiB(st.max) + " memory limit"
	}
	if st.peak > 0 {
		msg += " (peak " + formatGiB(st.peak) + ")"
	}
	return msg
}

func formatGiB(b uint64) string {
	return fmt.Sprintf("%.1f GiB", float64(b)/(1<<30))
}
