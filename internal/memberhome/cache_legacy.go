package memberhome

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/rootfs"
)

var legacyCachePaths = [...]string{
	".npm/_cacache",
	".npm/_logs",
	".cache/pip",
	".cache/uv",
	".cache/go-build",
	"go/pkg/mod",
}

// RemoveLegacyCaches removes only known reconstructible home cache directories.
// The caller must hold LockCaches and prove ALL users of the old home are gone,
// including retained runtimes, terminal containers and detached updaters.
// Directories are first renamed through pinned descriptors into server-owned
// retry slots beside runs metadata. Thus the remover never traverses mutable
// home parents, and an interrupted/failed removal remains discoverable even if
// the member's home is subsequently removed. Cross-filesystem moves fail closed.
func (m *Manager) RemoveLegacyCaches(ctx context.Context, member domain.MemberID) error {
	if err := validateMemberID(string(member)); err != nil {
		return err
	}
	home, err := m.openExistingHome(member)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if home != nil {
		defer func() { _ = home.Close() }()
	}
	pool, err := m.openCachePool(member, CachePoolRuns, false)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	defer func() {
		if pool != nil {
			_ = pool.Close()
		}
	}()
	var meta cacheMetadata
	if pool != nil {
		meta, err = loadCacheMetadata(pool, false)
		if err != nil {
			return err
		}
		if len(meta.Owners) != 0 {
			return fmt.Errorf("memberhome: legacy caches still have pending runtime owners")
		}
	}
	var cleanupErr error
	for i, rel := range legacyCachePaths {
		if err := ctx.Err(); err != nil {
			cleanupErr = errors.Join(cleanupErr, err)
			break
		}
		stage := fmt.Sprintf(".legacy-cache-%d", i)
		absolute := filepath.Join(m.cacheRoot, string(member), CachePoolRuns, stage)
		if pool != nil {
			if err := removeCacheDirectory(ctx, pool, stage, absolute, m.removeHome); err != nil {
				cleanupErr = errors.Join(cleanupErr, err)
				continue
			}
		}
		if home == nil {
			continue
		}
		parent, err := rootfs.OpenRoot(home, path.Dir(rel))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			cleanupErr = errors.Join(cleanupErr, err)
			continue
		}
		entry, err := parent.Lstat(path.Base(rel))
		if errors.Is(err, fs.ErrNotExist) {
			_ = parent.Close()
			continue
		}
		if err != nil || !entry.IsDir() || entry.Mode()&os.ModeSymlink != 0 {
			_ = parent.Close()
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("memberhome: unsafe legacy cache directory"), err)
			continue
		}
		if pool == nil {
			pool, err = m.openCachePool(member, CachePoolRuns, true)
			if err == nil {
				meta, err = loadCacheMetadata(pool, true)
			}
			if err == nil {
				err = saveCacheMetadata(pool, meta)
			}
			if err != nil {
				_ = parent.Close()
				return errors.Join(cleanupErr, err)
			}
		}
		err = renameCacheEntry(parent, path.Base(rel), pool, stage)
		if err == nil {
			err = errors.Join(syncCacheDirectory(parent), syncCacheDirectory(pool))
		}
		_ = parent.Close()
		if err == nil {
			err = removeCacheDirectory(ctx, pool, stage, absolute, m.removeHome)
		}
		cleanupErr = errors.Join(cleanupErr, err)
	}
	if cleanupErr != nil {
		// Create a metadata-only marker even when a symlink/permission failure
		// prevented staging; a deleted member must remain a retry owner.
		return errors.Join(cleanupErr, m.SetCacheCleanupError(member, CachePoolRuns, "legacy cache cleanup failed; retry pending"))
	}
	if pool != nil && meta.CleanupError == "legacy cache cleanup failed; retry pending" {
		meta.CleanupError = ""
		return saveCacheMetadata(pool, meta)
	}
	return nil
}
