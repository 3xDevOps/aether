package vmimage

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
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

// NewCacheDisk creates a qcow2 cache disk at path over pool's newest cache,
// or over a new empty one, and pins that cache as NewRootDisk pins a base.
func (s *Store) NewCacheDisk(ctx context.Context, pool, path string) (Disk, error) {
	if !validPool(pool) {
		return Disk{}, fmt.Errorf("vmimage: cache pool %q is not a plain name", pool)
	}
	base, err := s.newestCache(ctx, pool)
	if err != nil {
		return Disk{}, err
	}
	return s.NewRootDisk(ctx, base, path)
}

func validPool(pool string) bool {
	return pool != "" && filepath.Base(pool) == pool && !strings.HasPrefix(pool, ".")
}

func (s *Store) newestCache(ctx context.Context, pool string) (Base, error) {
	dir := filepath.Join(s.cachesDir(), pool)
	if base, err := s.readNewest(dir); err == nil || !errors.Is(err, fs.ErrNotExist) {
		return base, err
	}
	work, err := os.MkdirTemp(s.tmpDir(), "cache-")
	if err != nil {
		return Base{}, fmt.Errorf("vmimage: new cache: %w", err)
	}
	defer func() { _ = os.RemoveAll(work) }()
	disk := filepath.Join(work, "disk.raw")
	if err = mkfs(ctx, disk, cacheSize, "aether-cache", ""); err != nil {
		return Base{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if base, err := s.readNewest(dir); err == nil || !errors.Is(err, fs.ErrNotExist) {
		return base, err
	}
	return s.publishCacheLocked(dir, disk)
}

func (s *Store) readNewest(dir string) (Base, error) {
	name, err := os.Readlink(filepath.Join(dir, currentCache))
	if err != nil {
		return Base{}, err
	}
	return Base{ID: "cache:" + strings.TrimSuffix(name, ".raw"), Path: filepath.Join(dir, name)}, nil
}

// publishCacheLocked moves disk into dir as the pool's newest cache.
func (s *Store) publishCacheLocked(dir, disk string) (Base, error) {
	id, err := newLocalID()
	if err != nil {
		return Base{}, err
	}
	name := strings.TrimPrefix(id, "local:") + ".raw"
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

// SnapshotCache copies a stopped VM's cache disk into the store, cloning it
// where the filesystem can, and pins the cache it is made over, so the VM
// may start again or be deleted while PromoteCache reads the copy.
func (s *Store) SnapshotCache(ctx context.Context, disk string) (string, error) {
	backing, err := backingFile(disk)
	if err != nil {
		return "", err
	}
	work, err := os.MkdirTemp(s.tmpDir(), "snapshot-")
	if err != nil {
		return "", fmt.Errorf("vmimage: copy cache: %w", err)
	}
	pin, snapshot := filepath.Join(work, "base.raw"), filepath.Join(work, "cache.qcow2")
	err = os.Link(backing, pin)
	if err == nil {
		err = run(ctx, "cp", "--reflink=auto", disk, snapshot)
	}
	if err == nil {
		err = run(ctx, "qemu-img", "rebase", "-q", "-u", "-f", "qcow2", "-F", "raw", "-b", pin, snapshot)
	}
	if err != nil {
		_ = os.RemoveAll(work)
		return "", fmt.Errorf("vmimage: copy cache %s: %w", disk, err)
	}
	return work, nil
}

// PromoteCache makes a snapshot SnapshotCache took pool's newest cache, or
// drops the pool's caches when it takes more than cacheLimit, then deletes
// the snapshot and every cache of the pool no disk uses.
func (s *Store) PromoteCache(ctx context.Context, pool, snapshot string) error {
	defer func() { _ = os.RemoveAll(snapshot) }()
	if !validPool(pool) {
		return fmt.Errorf("vmimage: cache pool %q is not a plain name", pool)
	}
	disk := filepath.Join(snapshot, "disk.raw")
	if err := run(ctx, "qemu-img", "convert", "-f", "qcow2", "-O", "raw", filepath.Join(snapshot, "cache.qcow2"), disk); err != nil {
		return err
	}
	info, err := os.Stat(disk)
	if err != nil {
		return fmt.Errorf("vmimage: promote cache: %w", err)
	}
	dir := filepath.Join(s.cachesDir(), pool)
	s.mu.Lock()
	defer s.mu.Unlock()
	if info.Sys().(*syscall.Stat_t).Blocks*512 > cacheLimit {
		err = removeIfExists(filepath.Join(dir, currentCache))
	} else {
		_, err = s.publishCacheLocked(dir, disk)
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

func (s *Store) cachesDir() string { return filepath.Join(s.dir, "caches") }
