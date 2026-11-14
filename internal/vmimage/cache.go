package vmimage

import (
	"context"
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

const (
	// cacheSize is every cache filesystem's size; it is sparse.
	cacheSize = 32 << 30
	// cacheLimit is the most a pool's newest cache may take on the host; a
	// larger one is dropped, so the pool's next session starts empty.
	cacheLimit = 20 << 30
	// currentCache names, in a pool's directory, the link to its newest
	// cache.
	currentCache = "current"
)

// NewCacheDisk creates a raw cache disk at path: a copy of pool's newest
// cache, cloned where the filesystem can, or an empty one. A session wrote
// every cache, so no qcow2 disk ever backs onto one.
func (s *Store) NewCacheDisk(ctx context.Context, pool, path string) (Disk, error) {
	if !validPool(pool) {
		return Disk{}, fmt.Errorf("vmimage: cache pool %q is not a plain name", pool)
	}
	tmp, src := path+".tmp", path+".newest"
	if err := removeIfExists(tmp); err != nil {
		return Disk{}, err
	}
	// The link keeps a promotion's sweep from deleting the cache mid-copy.
	s.mu.Lock()
	newest, err := s.readNewest(filepath.Join(s.cachesDir(), pool))
	if err == nil {
		err = s.pinLocked(newest.Path, src)
	}
	s.mu.Unlock()
	switch {
	case errors.Is(err, fs.ErrNotExist):
		err = mkfs(ctx, tmp, cacheSize, "aether-cache", "")
	case err == nil:
		err = copyDisk(ctx, src, tmp)
	}
	if err = errors.Join(err, removeIfExists(src)); err != nil {
		return Disk{}, errors.Join(err, removeIfExists(tmp))
	}
	if err = os.Rename(tmp, path); err != nil {
		return Disk{}, fmt.Errorf("vmimage: cache disk %s: %w", path, err)
	}
	return Disk{Path: path, Format: FormatRaw}, nil
}

func validPool(pool string) bool {
	return pool != "" && filepath.Base(pool) == pool && !strings.HasPrefix(pool, ".")
}

func (s *Store) readNewest(dir string) (Base, error) {
	name, err := os.Readlink(filepath.Join(dir, currentCache))
	if err != nil {
		return Base{}, err
	}
	return Base{ID: "cache:" + strings.TrimSuffix(name, ".raw"), Path: filepath.Join(dir, name)}, nil
}

// publishCacheLocked moves disk, a session's cache as of seq, into dir as
// the pool's newest cache.
func (s *Store) publishCacheLocked(dir, disk string, seq int64) (Base, error) {
	id, err := newLocalID()
	if err != nil {
		return Base{}, err
	}
	name := fmt.Sprintf("%020d-%s.raw", seq, strings.TrimPrefix(id, localPrefix))
	if err = os.MkdirAll(dir, 0o700); err != nil {
		return Base{}, fmt.Errorf("vmimage: cache pool: %w", err)
	}
	if err = os.Chmod(disk, 0o444); err != nil {
		return Base{}, fmt.Errorf("vmimage: cache: %w", err)
	}
	if err = os.Rename(disk, filepath.Join(dir, name)); err != nil {
		return Base{}, fmt.Errorf("vmimage: cache: %w", err)
	}
	link := filepath.Join(dir, currentCache)
	if err = removeIfExists(link + ".tmp"); err != nil {
		return Base{}, err
	}
	if err = os.Symlink(name, link+".tmp"); err != nil {
		return Base{}, fmt.Errorf("vmimage: cache: %w", err)
	}
	if err = os.Rename(link+".tmp", link); err != nil {
		return Base{}, fmt.Errorf("vmimage: cache: %w", err)
	}
	return Base{ID: "cache:" + strings.TrimSuffix(name, ".raw"), Path: filepath.Join(dir, name)}, nil
}

// SnapshotCache copies a stopped VM's raw cache disk into the store,
// cloning it where the filesystem can, so the VM may start again or be
// deleted while PromoteCache publishes the copy.
func (s *Store) SnapshotCache(ctx context.Context, disk Disk) (string, error) {
	if disk.Format != FormatRaw {
		return "", fmt.Errorf("vmimage: copy cache %s: a cache disk must be raw, not %q", disk.Path, disk.Format)
	}
	work, err := os.MkdirTemp(s.tmpDir(), fmt.Sprintf("snapshot-%d-", time.Now().UnixNano()))
	if err != nil {
		return "", fmt.Errorf("vmimage: copy cache: %w", err)
	}
	if err = copyDisk(ctx, disk.Path, filepath.Join(work, "disk.raw")); err != nil {
		return "", errors.Join(fmt.Errorf("vmimage: copy cache %s: %w", disk.Path, err), os.RemoveAll(work))
	}
	return work, nil
}

// PromoteCache makes a snapshot SnapshotCache took pool's newest cache, or
// drops the pool's caches when it takes more than cacheLimit, then deletes
// the snapshot and every cache of the pool no disk uses. A snapshot older
// than the pool's newest cache, whose promotion finished first, changes
// nothing.
func (s *Store) PromoteCache(pool, snapshot string) error {
	defer func() { _ = os.RemoveAll(snapshot) }()
	if !validPool(pool) {
		return fmt.Errorf("vmimage: cache pool %q is not a plain name", pool)
	}
	disk := filepath.Join(snapshot, "disk.raw")
	info, err := os.Stat(disk)
	if err != nil {
		return fmt.Errorf("vmimage: promote cache: %w", err)
	}
	seq := snapshotSeq(snapshot)
	dir := filepath.Join(s.cachesDir(), pool)
	s.mu.Lock()
	defer s.mu.Unlock()
	switch newest, _ := s.readNewest(dir); {
	case newestSeq(newest) > seq:
	case info.Sys().(*syscall.Stat_t).Blocks*512 > cacheLimit:
		err = removeIfExists(filepath.Join(dir, currentCache))
	default:
		_, err = s.publishCacheLocked(dir, disk, seq)
	}
	return errors.Join(err, s.sweepCachesLocked(dir))
}

// sweepCachesLocked deletes a pool's caches that are not its newest and
// that no disk links to.
func (s *Store) sweepCachesLocked(dir string) error {
	newest, _ := os.Readlink(filepath.Join(dir, currentCache))
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("vmimage: sweep caches: %w", err)
	}
	var errs []error
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".raw") || e.Name() == newest {
			continue
		}
		path := filepath.Join(dir, e.Name())
		linked, err := linkedElsewhere(path)
		if err == nil && !linked {
			err = removeIfExists(path)
		}
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// snapshotSeq is when SnapshotCache took a snapshot, from its name.
func snapshotSeq(snapshot string) int64 {
	digits, _, _ := strings.Cut(strings.TrimPrefix(filepath.Base(snapshot), "snapshot-"), "-")
	seq, _ := strconv.ParseInt(digits, 10, 64)
	return seq
}

// newestSeq is when the snapshot a pool's newest cache came from was taken,
// 0 for none.
func newestSeq(newest Base) int64 {
	digits, _, _ := strings.Cut(filepath.Base(newest.Path), "-")
	seq, _ := strconv.ParseInt(digits, 10, 64)
	return seq
}

func (s *Store) cachesDir() string { return filepath.Join(s.dir, "caches") }
