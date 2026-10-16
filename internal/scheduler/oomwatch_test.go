package scheduler

import (
	"os"
	"path/filepath"
	"testing"
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
	want := "Out of memory: the kernel killed a process in this run at its 8.0 GiB memory limit (peak 8.0 GiB)"
	if got := oomMessage(st); got != want {
		t.Fatalf("oomMessage = %q, want %q", got, want)
	}
	write("memory.max", "max\n")
	st, err = readMemoryStats(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := oomMessage(st); got != "Out of memory: the kernel killed a process in this run (peak 8.0 GiB)" {
		t.Fatalf("unlimited message = %q", got)
	}
	if err := os.Remove(filepath.Join(dir, "memory.events")); err != nil {
		t.Fatal(err)
	}
	if _, err := readMemoryStats(dir); !os.IsNotExist(err) {
		t.Fatalf("missing memory.events error = %v, want not-exist", err)
	}
}
