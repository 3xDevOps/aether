package disk

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Bytes each seeded tree holds on disk. The shared pack is one inode with
// two pathnames, exactly as `git clone --local` leaves a checkout, and
// belongs to the repo it was cloned from.
const (
	seededWorktreeBytes   = 500
	seededTranscriptBytes = 200
	seededDatabaseBytes   = 300
	seededSharedBytes     = 600
	seededRepoBytes       = 750 + seededSharedBytes
)

// seedDataDir builds a data directory holding one run checkout, one
// transcript, a database file and one workspace bare repo, with distinct
// sizes so a component that measures the wrong directory is visible in the
// number. One pack file is hard-linked into the checkout the way a local
// clone leaves it, so a walk that trusts pathnames double-counts it.
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
	write(filepath.Join(reposDir, "ws_1.git", "objects", "pack", "pack-1.pack"), 700)
	write(filepath.Join(reposDir, "ws_1.git", "logs", "HEAD"), 50)

	shared := filepath.Join(dir, reposDir, "ws_1.git", "objects", "pack", "shared.pack")
	if err := os.WriteFile(shared, make([]byte, seededSharedBytes), 0o644); err != nil {
		t.Fatalf("write the shared pack: %v", err)
	}
	link := filepath.Join(dir, checkoutsDir, "run_1", ".git", "objects", "pack", "shared.pack")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(link), err)
	}
	if err := os.Link(shared, link); err != nil {
		t.Fatalf("hardlink the shared pack into the checkout: %v", err)
	}
	return dir
}

func TestMeasureAccountsForEachGrowingDirectory(t *testing.T) {
	got, err := Measure(seedDataDir(t))
	if err != nil {
		t.Fatalf("Measure: %v", err)
	}
	if got.WorktreeBytes != seededWorktreeBytes {
		t.Errorf("worktree bytes = %d, want %d (the checkout's own files, not the pack it "+
			"hardlinks from the bare repo)", got.WorktreeBytes, seededWorktreeBytes)
	}
	if got.TranscriptBytes != seededTranscriptBytes {
		t.Errorf("transcript bytes = %d, want %d", got.TranscriptBytes, seededTranscriptBytes)
	}
	if got.DatabaseBytes != seededDatabaseBytes {
		t.Errorf("database bytes = %d, want %d", got.DatabaseBytes, seededDatabaseBytes)
	}
	if got.RepoBytes != seededRepoBytes {
		t.Errorf("repo bytes = %d, want %d (the whole bare repo tree, including the shared pack)",
			got.RepoBytes, seededRepoBytes)
	}
	if got.TotalBytes == 0 || got.FreeBytes == 0 {
		t.Errorf("filesystem reading = %+v, want a real total and free", got)
	}
}

// Run checkouts are `git clone --local` hardlink clones, so a walk that
// sums sizes per pathname charges every shared object twice and the gauge
// claims a repo and its one clone hold twice the space they do.
func TestMeasureCountsAHardlinkedObjectOnce(t *testing.T) {
	got, err := Measure(seedDataDir(t))
	if err != nil {
		t.Fatalf("Measure: %v", err)
	}
	onDisk := uint64(seededWorktreeBytes + seededTranscriptBytes + seededDatabaseBytes + seededRepoBytes)
	total := got.WorktreeBytes + got.TranscriptBytes + got.DatabaseBytes + got.RepoBytes
	if total != onDisk {
		t.Errorf("components sum to %d, want %d: the pack shared with the checkout is counted "+
			"%d bytes too many", total, onDisk, total-onDisk)
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
	if got.WorktreeBytes != 0 || got.TranscriptBytes != 0 || got.DatabaseBytes != 0 || got.RepoBytes != 0 {
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
	if first.WorktreeBytes != seededWorktreeBytes {
		t.Fatalf("worktree bytes = %d, want %d", first.WorktreeBytes, seededWorktreeBytes)
	}
	if first.RepoBytes != seededRepoBytes {
		t.Fatalf("repo bytes = %d, want %d", first.RepoBytes, seededRepoBytes)
	}

	if werr := os.WriteFile(filepath.Join(dir, checkoutsDir, "run_1", "big"), make([]byte, 1000), 0o644); werr != nil {
		t.Fatalf("grow the checkout: %v", werr)
	}
	if werr := os.WriteFile(filepath.Join(dir, reposDir, "ws_1.git", "objects", "pack", "pack-2.pack"), make([]byte, 250), 0o644); werr != nil {
		t.Fatalf("grow the bare repo: %v", werr)
	}
	within, err := c.Usage()
	if err != nil {
		t.Fatalf("Usage within the TTL: %v", err)
	}
	if within.WorktreeBytes != seededWorktreeBytes {
		t.Errorf("worktree bytes within the TTL = %d, want the cached %d",
			within.WorktreeBytes, seededWorktreeBytes)
	}
	if within.RepoBytes != seededRepoBytes {
		t.Errorf("repo bytes within the TTL = %d, want the cached %d",
			within.RepoBytes, seededRepoBytes)
	}

	clock = clock.Add(time.Minute)
	after, err := c.Usage()
	if err != nil {
		t.Fatalf("Usage after the TTL: %v", err)
	}
	if want := uint64(seededWorktreeBytes + 1000); after.WorktreeBytes != want {
		t.Errorf("worktree bytes after the TTL = %d, want the re-walked %d", after.WorktreeBytes, want)
	}
	if want := uint64(seededRepoBytes + 250); after.RepoBytes != want {
		t.Errorf("repo bytes after the TTL = %d, want the re-walked %d", after.RepoBytes, want)
	}
}
