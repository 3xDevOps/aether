// Package disk measures filesystem headroom and de-duplicated data-directory usage.
package disk

import (
	"errors"
	"io/fs"
	"os"
	"sort"
	"strings"
	"time"
)

// Usage separates filesystem headroom from apparent regular-file sizes.
// SnapshotBytes is included in WorktreeBytes. Entries attribute the component
// sizes, not additional usage or a promise that deletion reclaims those bytes.
type Usage struct {
	FreeBytes       uint64
	UsedBytes       uint64
	TotalBytes      uint64
	WorktreeBytes   uint64
	TranscriptBytes uint64
	DatabaseBytes   uint64
	RepoBytes       uint64
	HomeBytes       uint64
	CacheBytes      uint64
	EvidenceBytes   uint64
	OtherBytes      uint64
	SnapshotBytes   uint64
	Warnings        []string
	Entries         []Entry
	Truncated       bool
	Docker          *DockerUsage
}

// Entry.Key is an internal lookup key, never a wire-visible filename.
type Entry struct {
	Kind             string
	Key              string
	OwnerKind        string
	OwnerID          string
	Pool             string
	Bytes            uint64
	ReclaimableBytes *uint64
	RetainedUntil    *time.Time
	Reason           string
	Error            string
}

// DockerUsage is daemon-wide, not restricted to Aether-owned resources.
// Unknown values are nil; filesystem values must not be added to Usage totals.
type DockerUsage struct {
	UsedBytes        *uint64 `json:"used_bytes,omitempty"`
	TotalBytes       *uint64 `json:"total_bytes,omitempty"`
	FreeBytes        *uint64 `json:"free_bytes,omitempty"`
	ImagesBytes      *uint64 `json:"images_bytes,omitempty"`
	ContainersBytes  *uint64 `json:"containers_bytes,omitempty"`
	VolumesBytes     *uint64 `json:"volumes_bytes,omitempty"`
	BuildCacheBytes  *uint64 `json:"build_cache_bytes,omitempty"`
	ReclaimableBytes *uint64 `json:"reclaimable_bytes,omitempty"`
	SharedFilesystem *bool   `json:"shared_filesystem,omitempty"`
	Error            string  `json:"error,omitempty"`
}

const (
	checkoutsDir   = "checkouts"
	transcriptsDir = "transcripts"
	reposDir       = "repos"
	databaseFile   = "aether.db"
)

func Measure(dataDir string) (Usage, error) {
	u, err := filesystem(dataDir)
	if err != nil {
		return Usage{}, err
	}
	return u.withComponents(components(dataDir)), nil
}

func components(dataDir string) Usage {
	var u Usage
	root, err := os.OpenRoot(dataDir)
	if err != nil {
		u.Warnings = []string{"data directory: " + measurementError(err)}
		return u
	}
	defer func() { _ = root.Close() }()
	return componentTree(root.FS(), newSeen(root))
}

func componentTree(tree fs.FS, counted seen) Usage {
	return componentTreeOwners(tree, counted, componentOwner)
}

func componentTreeOwners(tree fs.FS, counted seen, owner func(string) (string, string)) Usage {
	var u Usage
	top, err := fs.ReadDir(tree, ".")
	if err != nil {
		u.Warnings = []string{"data directory: " + measurementError(err)}
		return u
	}
	// Repositories own hard-linked clone objects even when another tree is
	// encountered first lexically. Every other category shares the same set.
	sort.SliceStable(top, func(i, j int) bool { return top[i].Name() == reposDir && top[j].Name() != reposDir })
	entries := make(map[[2]string]*Entry)
	warnings := make(map[string]bool)
	for _, dir := range top {
		// WalkDir stats its root and would follow even an in-tree symlink.
		// ReadDir metadata does not follow links, so skip them before walking.
		if dir.Type()&fs.ModeSymlink != 0 {
			continue
		}
		walkErr := fs.WalkDir(tree, dir.Name(), func(name string, d fs.DirEntry, err error) error {
			kind, key := owner(name)
			index := [2]string{kind, key}
			e := entries[index]
			if e == nil {
				e = &Entry{Kind: kind, Key: key, OwnerKind: "server", Reason: "Ownership has not been resolved"}
				entries[index] = e
			}
			if err == nil && d.Type().IsRegular() {
				var info fs.FileInfo
				info, err = d.Info()
				var claimed bool
				if err == nil && info.Mode().IsRegular() && info.Size() > 0 {
					claimed, err = counted.claim(name, info)
				}
				if err == nil && claimed {
					n := uint64(info.Size())
					e.Bytes += n
					switch kind {
					case "repo":
						u.RepoBytes += n
					case "checkout":
						u.WorktreeBytes += n
					case "snapshot":
						u.WorktreeBytes += n
						u.SnapshotBytes += n
					case "transcript":
						u.TranscriptBytes += n
					case "database":
						u.DatabaseBytes += n
					case "home":
						u.HomeBytes += n
					case "cache":
						u.CacheBytes += n
					case "evidence":
						u.EvidenceBytes += n
					default:
						u.OtherBytes += n
					}
				}
			}
			if err != nil {
				e.Error = measurementError(err)
				warning := kind + ": partial measurement: " + e.Error
				if !warnings[warning] {
					warnings[warning] = true
					u.Warnings = append(u.Warnings, warning)
				}
			}
			return nil
		})
		if walkErr != nil {
			u.Warnings = append(u.Warnings, "data directory: "+measurementError(walkErr))
		}
	}
	for _, entry := range entries {
		if entry.Bytes > 0 || entry.Error != "" || entry.Kind == "cache" {
			u.Entries = append(u.Entries, *entry)
		}
	}
	sort.Slice(u.Entries, func(i, j int) bool {
		a, b := u.Entries[i], u.Entries[j]
		if a.Bytes != b.Bytes {
			return a.Bytes > b.Bytes
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.Key < b.Key
	})
	return u
}

func componentOwner(name string) (kind, key string) {
	top, rest, _ := strings.Cut(name, "/")
	key, _, _ = strings.Cut(rest, "/")
	switch top {
	case reposDir:
		return "repo", strings.TrimSuffix(key, ".git")
	case checkoutsDir:
		if strings.HasSuffix(key, ".diffsnap") {
			return "snapshot", strings.TrimSuffix(key, ".diffsnap")
		}
		return "checkout", key
	case transcriptsDir:
		key = strings.TrimSuffix(strings.TrimSuffix(key, ".items.jsonl"), ".cast")
		key, _, _ = strings.Cut(key, ".~")
		return "transcript", key
	case "homes":
		return "home", key
	case "home-caches":
		return cachePoolOwner(rest)
	case "evidence":
		return "evidence", strings.TrimSuffix(strings.TrimSuffix(key, ".captures"), ".transcript")
	case "coord":
		return "coord", key
	case databaseFile, databaseFile + "-wal", databaseFile + "-shm":
		return "database", ""
	default:
		return "other", ""
	}
}

// MeasureCachePools measures only the managed cache root, sharing one inode
// set across pools. Partial results are accompanied by an error and must not
// be interpreted as complete pressure or cleanup eligibility information.
// A root not yet created is an empty cache inventory, not a failed scan.
func MeasureCachePools(cacheRoot string) (map[string]uint64, error) {
	root, err := os.OpenRoot(cacheRoot)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// OpenRoot also reports ENOENT for an existing dangling symlink.
			// Only an absent root itself is a legitimate empty inventory.
			if _, statErr := os.Lstat(cacheRoot); errors.Is(statErr, fs.ErrNotExist) {
				return nil, nil
			}
		}
		return nil, err
	}
	defer func() { _ = root.Close() }()
	u := componentTreeOwners(root.FS(), newSeen(root), cachePoolOwner)
	pools := make(map[string]uint64)
	for _, entry := range u.Entries {
		if entry.Kind == "cache" {
			pools[entry.Key] = entry.Bytes
		}
	}
	if len(u.Warnings) != 0 {
		return pools, errors.New(strings.Join(u.Warnings, "; "))
	}
	return pools, nil
}

func cachePoolOwner(name string) (string, string) {
	member, poolPath, _ := strings.Cut(name, "/")
	pool, _, _ := strings.Cut(poolPath, "/")
	if member != "" && (pool == "runs" || pool == "terminal") {
		return "cache", member + "/" + pool
	}
	return "other", ""
}

func measurementError(err error) string {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		err = pe.Err
	}
	return err.Error()
}

func (u Usage) withComponents(c Usage) Usage {
	c.FreeBytes, c.UsedBytes, c.TotalBytes = u.FreeBytes, u.UsedBytes, u.TotalBytes
	return c
}

func Free(path string) (uint64, error) {
	u, err := filesystem(path)
	if err != nil {
		return 0, err
	}
	return u.FreeBytes, nil
}

// Filesystem reads only headroom, without walking Docker's private data root.
func Filesystem(path string) (Usage, error) { return filesystem(path) }
