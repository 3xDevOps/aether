// Package disk measures the server's data directory: how much room the
// filesystem holding it has left, and how much of that the four Aether
// directories that grow without bound are using. One measurement serves
// both consumers, so the dashboard's gauge and the scheduler's free-space
// floor can never disagree about the same disk.
package disk

import (
	"io/fs"
	"os"
	"path/filepath"
)

// Usage is one reading of the data directory.
//
// Used and Total describe the whole filesystem - the gauge answers "is the
// disk filling up", which is not a question about Aether's own footprint.
// Free is what an unprivileged writer can still claim, which is smaller
// than Total-Used wherever the filesystem reserves blocks; it is the number
// the floor is checked against.
//
// The four component fields are Aether's own growing tenants. Everything
// else under the data directory (host keys, invites, credential homes,
// profile snapshots) is bounded by the number of members and is not worth
// a line on a gauge.
type Usage struct {
	// FreeBytes, UsedBytes and TotalBytes describe the filesystem.
	FreeBytes  uint64
	UsedBytes  uint64
	TotalBytes uint64
	// WorktreeBytes is <data>/checkouts: the run worktrees the scheduler
	// garbage-collects after their TTL, counting only the bytes reclaiming
	// one would actually give back - the objects it shares with the bare
	// repo are charged to RepoBytes.
	WorktreeBytes uint64
	// TranscriptBytes is <data>/transcripts: kept for the life of the run
	// row, never GC'd.
	TranscriptBytes uint64
	// DatabaseBytes is the SQLite file and its write-ahead log, which is
	// where the persisted event log lives alongside the store. The event
	// log is the part that grows without bound; it has no file of its own
	// to measure.
	DatabaseBytes uint64
	// RepoBytes is <data>/repos: the bare repo behind each workspace. It
	// keeps every push, every run branch, and the reflogs the engine turns
	// on, and nothing reclaims it, so on a long-lived server it is often
	// the component holding the most. Objects a run checkout hardlinks to
	// are counted here, once.
	RepoBytes uint64
}

// Data directory members Measure accounts for, relative to the data
// directory the server was given (server.Config.DataDir).
const (
	checkoutsDir   = "checkouts"
	transcriptsDir = "transcripts"
	reposDir       = "repos"
	databaseFile   = "aether.db"
)

// Measure reads the filesystem headroom under dataDir and adds up what
// Aether is holding there. A component that cannot be read contributes
// zero rather than failing the whole reading: a gauge that vanishes when
// one directory is missing is worse than one that under-reports. Only the
// filesystem read is fatal - without it there is no gauge at all.
func Measure(dataDir string) (Usage, error) {
	u, err := filesystem(dataDir)
	if err != nil {
		return Usage{}, err
	}
	return u.withComponents(components(dataDir)), nil
}

// components walks the data directory and returns a Usage carrying only
// the component fields. Both readers - Measure and Cache - assemble a
// reading from it, so there is one list of what the gauge accounts for.
//
// Repos is walked first because run checkouts are `git clone --local`
// hardlink clones of the bare repo: a checkout's object files are the bare
// repo's inodes under a second pathname. One shared inode set spans the
// two walks and the first tree to reach an object owns it, so the bytes
// land on the repo, which is where they stay after the checkout is
// reclaimed. Summing both pathnames would report a repo and its one clone
// as twice the space they occupy.
func components(dataDir string) Usage {
	counted := newSeen()
	return Usage{
		RepoBytes:       treeBytes(filepath.Join(dataDir, reposDir), counted),
		WorktreeBytes:   treeBytes(filepath.Join(dataDir, checkoutsDir), counted),
		TranscriptBytes: treeBytes(filepath.Join(dataDir, transcriptsDir), counted),
		DatabaseBytes:   databaseBytes(filepath.Join(dataDir, databaseFile)),
	}
}

// withComponents returns u with the component fields taken from c and its
// own filesystem reading left alone.
func (u Usage) withComponents(c Usage) Usage {
	u.WorktreeBytes = c.WorktreeBytes
	u.TranscriptBytes = c.TranscriptBytes
	u.DatabaseBytes = c.DatabaseBytes
	u.RepoBytes = c.RepoBytes
	return u
}

// Free reports the bytes an unprivileged writer can still claim on the
// filesystem holding path. It is the scheduler's floor check, and it takes
// the same reading Measure reports.
func Free(path string) (uint64, error) {
	u, err := filesystem(path)
	if err != nil {
		return 0, err
	}
	return u.FreeBytes, nil
}

// treeBytes sums the apparent size of every regular file under root that
// counted has not already charged to an earlier tree. Directories the walk
// cannot enter are skipped: a partially readable answer is the honest one.
func treeBytes(root string, counted seen) uint64 {
	var total uint64
	_ = filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		if info, ierr := d.Info(); ierr == nil && counted.claim(info) {
			total += uint64(info.Size())
		}
		return nil
	})
	return total
}

// databaseBytes sums the SQLite file and the sidecar files that carry the
// writes not yet folded into it, so a busy server's gauge does not lag its
// write-ahead log.
func databaseBytes(path string) uint64 {
	var total uint64
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		if info, err := os.Stat(p); err == nil && info.Mode().IsRegular() {
			total += uint64(info.Size())
		}
	}
	return total
}
