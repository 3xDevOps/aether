package disk

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// seedDataDir builds a data directory holding one run checkout, one
// transcript and a database file, with distinct sizes so a component that
// measures the wrong directory is visible in the number.
func seedDataDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write := func(rel string, n int) {
		t.Helper()
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", path, err)
		}
		if err := os.WriteFile(path, make([]byte, n), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	write(filepath.Join(checkoutsDir, "run_1", "src", "main.go"), 400)
	write(filepath.Join(checkoutsDir, "run_1", "README.md"), 100)
	write(filepath.Join(transcriptsDir, "run_1.cast"), 200)
	write(databaseFile, 300)
	return dir
}

func TestMeasureAccountsForEachGrowingDirectory(t *testing.T) {
	got, err := Measure(seedDataDir(t))
	if err != nil {
		t.Fatalf("Measure: %v", err)
	}
	if got.WorktreeBytes != 500 {
		t.Errorf("worktree bytes = %d, want 500 (the whole checkout tree)", got.WorktreeBytes)
	}
	if got.TranscriptBytes != 200 {
		t.Errorf("transcript bytes = %d, want 200", got.TranscriptBytes)
	}
	if got.DatabaseBytes != 300 {
		t.Errorf("database bytes = %d, want 300", got.DatabaseBytes)
	}
	if got.TotalBytes == 0 || got.FreeBytes == 0 {
		t.Errorf("filesystem reading = %+v, want a real total and free", got)
	}
}

// A data directory the server has not populated yet must still produce a
// gauge: a component that cannot be read contributes zero rather than
// failing the whole reading.
func TestMeasureToleratesMissingDirectories(t *testing.T) {
	got, err := Measure(t.TempDir())
	if err != nil {
		t.Fatalf("Measure on an empty data dir: %v", err)
	}
	if got.WorktreeBytes != 0 || got.TranscriptBytes != 0 || got.DatabaseBytes != 0 {
		t.Errorf("components on an empty data dir = %+v, want zeroes", got)
	}
	if got.TotalBytes == 0 {
		t.Error("filesystem total is zero on an empty data dir")
	}
}

// The dashboard refreshes the gauge every couple of seconds, so the walk is
// reused for the TTL while the filesystem headroom stays fresh.
func TestCacheReusesTheWalkUntilTheTTLExpires(t *testing.T) {
	dir := seedDataDir(t)
	clock := time.Now()
	c := NewCache(dir, time.Minute)
	c.now = func() time.Time { return clock }

	first, err := c.Usage()
	if err != nil {
		t.Fatalf("Usage: %v", err)
	}
	if first.WorktreeBytes != 500 {
		t.Fatalf("worktree bytes = %d, want 500", first.WorktreeBytes)
	}

	if werr := os.WriteFile(filepath.Join(dir, checkoutsDir, "run_1", "big"), make([]byte, 1000), 0o644); werr != nil {
		t.Fatalf("grow the checkout: %v", werr)
	}
	within, err := c.Usage()
	if err != nil {
		t.Fatalf("Usage within the TTL: %v", err)
	}
	if within.WorktreeBytes != 500 {
		t.Errorf("worktree bytes within the TTL = %d, want the cached 500", within.WorktreeBytes)
	}

	clock = clock.Add(time.Minute)
	after, err := c.Usage()
	if err != nil {
		t.Fatalf("Usage after the TTL: %v", err)
	}
	if after.WorktreeBytes != 1500 {
		t.Errorf("worktree bytes after the TTL = %d, want the re-walked 1500", after.WorktreeBytes)
	}
}
