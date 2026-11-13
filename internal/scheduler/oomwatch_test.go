package scheduler

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/3xDevOps/Aether/internal/runtime"
)

func TestReadMemoryStatsAndMessage(t *testing.T) {
	proc, cg := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(proc, "42"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proc, "42", "cgroup"), []byte("0::/system.slice/docker-abc.scope\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	path, err := cgroupPath(proc, 42)
	if err != nil || path != "/system.slice/docker-abc.scope" {
		t.Fatalf("cgroupPath = %q, %v", path, err)
	}
	dir := filepath.Join(cg, path)
	if err = os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		t.Helper()
		if writeErr := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	write("memory.events", "low 0\nhigh 0\nmax 3\noom 1\noom_kill 1\n")
	write("memory.max", "8589934592\n")
	write("memory.peak", "8589934592\n")
	st, err := readMemoryStats(dir)
	if err != nil || st.oomKills != 1 {
		t.Fatalf("readMemoryStats = %+v, %v", st, err)
	}
	want := "Out of memory: the kernel killed a process in this session at its 8.0 GiB memory limit (peak 8.0 GiB)"
	if got := oomMessage(st); got != want {
		t.Fatalf("oomMessage = %q, want %q", got, want)
	}
	write("memory.max", "max\n")
	st, err = readMemoryStats(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := oomMessage(st); got != "Out of memory: the kernel killed a process in this session (peak 8.0 GiB)" {
		t.Fatalf("unlimited message = %q", got)
	}
	if err := os.Remove(filepath.Join(dir, "memory.events")); err != nil {
		t.Fatal(err)
	}
	if _, err := readMemoryStats(dir); !os.IsNotExist(err) {
		t.Fatalf("missing memory.events error = %v, want not-exist", err)
	}
}

type guestMemory struct {
	pid    int
	own    bool
	events runtime.MemoryEvents
}

func (g guestMemory) InitPID(context.Context, runtime.ID) (int, error) { return g.pid, nil }

func (g guestMemory) MemoryEvents(context.Context, runtime.ID) (runtime.MemoryEvents, bool, error) {
	return g.events, g.own, nil
}

func TestOOMWatcherReadsGuestKillsFromTheRuntime(t *testing.T) {
	proc, cg := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(proc, "7"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proc, "7", "cgroup"), []byte("0::/aether.slice/run.scope\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(cg, "aether.slice", "run.scope"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cg, "aether.slice", "run.scope", "memory.events"), []byte("oom_kill 2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	w := &oomWatcher{checked: true, enabled: true, cgroup: cg, proc: proc, paths: map[runtime.ID]string{}, kills: map[runtime.ID]uint64{}}

	guest := guestMemory{pid: 7, own: true, events: runtime.MemoryEvents{OOMKills: 5, LimitBytes: 4 << 30}}
	st, err := w.read(t.Context(), guest, "vm-run-a")
	if err != nil || st.oomKills != 5 || st.max != 4<<30 {
		t.Fatalf("guest read = %+v, %v", st, err)
	}
	host := guestMemory{pid: 7}
	st, err = w.read(t.Context(), host, "container-b")
	if err != nil || st.oomKills != 2 {
		t.Fatalf("cgroup fallback = %+v, %v", st, err)
	}
}
