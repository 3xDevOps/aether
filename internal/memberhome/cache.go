package memberhome

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/rootfs"
)

const (
	CachePoolRuns     = "runs"
	CachePoolTerminal = "terminal"

	cacheMetadataName     = "metadata.json"
	maxCacheMetadataBytes = 256 << 10
	maxCacheErrorBytes    = 512
	maxCacheOwners        = 256
	maxCacheOwnerBytes    = 512
)

// CacheInfo describes an existing pool without creating or touching it. A pool
// with MetadataError must be preserved: its ownership and activity are unknown.
// Metadata-only pools remain discoverable after deletion of data or the member.
// Path names only the disposable data directory, never the metadata directory.
type CacheInfo struct {
	Member             domain.MemberID
	Pool               string
	Path               string
	LastUsed           time.Time
	CleanupError       string
	DataCleanupPending bool
	MetadataError      string
	DataExists         bool
	Owners             []string
}

type cacheMetadata struct {
	Version             int       `json:"version"`
	LastUsed            time.Time `json:"last_used"`
	CleanupError        string    `json:"cleanup_error,omitempty"`
	DataCleanupPending  bool      `json:"data_cleanup_pending,omitempty"`
	ImageCleanupError   string    `json:"image_cleanup_error,omitempty"`
	RuntimeCleanupError string    `json:"runtime_cleanup_error,omitempty"`
	Owners              []string  `json:"owners,omitempty"`
	LegacyProtected     uint8     `json:"legacy_protected,omitempty"`
}

// LockCaches serializes ownership publication and cache maintenance for one
// member, independently of home/config locks. Take it before scheduler state
// locks; never wait for it while holding those locks. Cache operations do not
// acquire it themselves. Callers hold it through provisioning/publication or
// through the complete ownership check and deletion, not just filesystem I/O.
func (m *Manager) LockCaches(member domain.MemberID) func() {
	lock := m.cacheLock(member)
	lock.Lock()
	return lock.Unlock
}

// TryLockCaches lets maintenance skip a member whose provisioning holds its
// cache lock, without waiting while holding other lifecycle locks.
func (m *Manager) TryLockCaches(member domain.MemberID) (func(), bool) {
	lock := m.cacheLock(member)
	if !lock.TryLock() {
		return nil, false
	}
	return lock.Unlock, true
}

func (m *Manager) cacheLock(member domain.MemberID) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	lock := m.cacheLocks[member]
	if lock == nil {
		lock = &sync.Mutex{}
		m.cacheLocks[member] = lock
	}
	return lock
}

// CachePath creates only the owned data directory and records genuine use.
// Existing uncertain metadata is never replaced with a fresh ownership record.
// The caller holds LockCaches until the new runtime's ownership is published.
func (m *Manager) CachePath(member domain.MemberID, pool string) (string, error) {
	root, err := m.openCachePool(member, pool, true)
	if err != nil {
		return "", err
	}
	defer func() { _ = root.Close() }()
	meta, err := loadCacheMetadata(root, true)
	if err != nil {
		return "", err
	}
	meta.LastUsed = time.Now().UTC()
	if err = saveCacheMetadata(root, meta); err != nil {
		return "", err
	}
	data, err := openCacheDirectory(root, "data", true)
	if err != nil {
		return "", fmt.Errorf("memberhome: prepare cache data: %w", err)
	}
	if err := data.Close(); err != nil {
		return "", err
	}
	return m.cachePath(member, pool), nil
}

// TouchCache records ownership activity/release without recreating removed
// data. A missing pool is a no-op, including pre-cache lifecycle owners.
func (m *Manager) TouchCache(member domain.MemberID, pool string) error {
	root, err := m.openCachePool(member, pool, false)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	meta, err := loadCacheMetadata(root, false)
	if err != nil {
		return err
	}
	meta.LastUsed = time.Now().UTC()
	return saveCacheMetadata(root, meta)
}

// ReadCache reads an existing pool; a missing pool returns fs.ErrNotExist.
// Malformed metadata and unsafe data entries are represented by MetadataError
// so read-only inventory can surface the pool while treating it as protected.
func (m *Manager) ReadCache(member domain.MemberID, pool string) (CacheInfo, error) {
	info := CacheInfo{Member: member, Pool: pool, Path: m.cachePath(member, pool)}
	root, err := m.openCachePool(member, pool, false)
	if err != nil {
		return info, err
	}
	defer func() { _ = root.Close() }()
	meta, err := loadCacheMetadata(root, false)
	if err != nil {
		info.MetadataError = "cache ownership metadata is unavailable or invalid"
	} else {
		info.LastUsed, info.CleanupError, info.Owners = meta.LastUsed, meta.CleanupError, meta.Owners
		info.DataCleanupPending = meta.DataCleanupPending
		if info.DataCleanupPending {
			info.CleanupError = boundedCacheError(strings.TrimSuffix("cache cleanup failed; retry pending; "+info.CleanupError, "; "))
		}
		if meta.ImageCleanupError != "" {
			info.CleanupError = boundedCacheError(strings.TrimPrefix(info.CleanupError+"; "+meta.ImageCleanupError, "; "))
		}
		if meta.RuntimeCleanupError != "" {
			info.CleanupError = boundedCacheError(strings.TrimPrefix(info.CleanupError+"; "+meta.RuntimeCleanupError, "; "))
		}
	}
	entry, err := root.Lstat("data")
	if err == nil {
		info.DataExists = true
		if !entry.IsDir() || entry.Mode()&os.ModeSymlink != 0 {
			info.MetadataError = "cache data is not a safe directory"
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		info.MetadataError = "cache data is unavailable"
	}
	return info, nil
}

// ListCaches lists both pools, including metadata-only retry markers, without
// creating roots or refreshing activity. Unsafe entries remain explicit.
func (m *Manager) ListCaches() ([]CacheInfo, error) {
	members, err := cacheRootMembers(m.cacheRoot)
	if err != nil {
		return nil, err
	}
	var result []CacheInfo
	for _, member := range members {
		for _, pool := range []string{CachePoolRuns, CachePoolTerminal} {
			info, err := m.ReadCache(member, pool)
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if err != nil {
				info.MetadataError = "cache pool is unavailable or unsafe"
			}
			result = append(result, info)
		}
	}
	return result, nil
}

// CacheMembers discovers durable retry owners from both roots, including
// members removed from the database and homes predating managed cache pools.
func (m *Manager) CacheMembers() ([]domain.MemberID, error) {
	members, err := cacheRootMembers(m.cacheRoot)
	if err != nil {
		return nil, err
	}
	homes, err := cacheRootMembers(m.root)
	if err != nil {
		return nil, err
	}
	members = append(members, homes...)
	slices.Sort(members)
	return slices.Compact(members), nil
}

// SetCacheCleanupError records a bounded public diagnostic, creating only a
// metadata retry marker if needed. Callers must supply no paths or secrets.
func (m *Manager) SetCacheCleanupError(member domain.MemberID, pool, message string) error {
	return m.updateCacheMetadata(member, pool, true, func(meta *cacheMetadata) error {
		meta.CleanupError = boundedCacheError(message)
		return nil
	})
}

// EnsureCacheMetadata preserves an existing retry owner without clearing any
// pending operation, or creates a metadata-only owner when none exists.
func (m *Manager) EnsureCacheMetadata(member domain.MemberID, pool string) error {
	return m.updateCacheMetadata(member, pool, true, func(*cacheMetadata) error { return nil })
}

// SetCacheImageCleanupError updates only the saved-image cleanup diagnostic.
// Cache data and other failed operations retain their independent retry state.
func (m *Manager) SetCacheImageCleanupError(member domain.MemberID, message string) error {
	return m.updateCacheMetadata(member, CachePoolTerminal, true, func(meta *cacheMetadata) error {
		meta.ImageCleanupError = boundedCacheError(message)
		return nil
	})
}

// SetCacheRuntimeCleanupError changes only runtime-owner reconciliation status.
func (m *Manager) SetCacheRuntimeCleanupError(member domain.MemberID, pool, message string) error {
	return m.updateCacheMetadata(member, pool, true, func(meta *cacheMetadata) error {
		meta.RuntimeCleanupError = boundedCacheError(message)
		return nil
	})
}

// AddCacheOwner persists a runtime creation key before detached runtime
// creation. Existing keys are idempotent; a full record refuses new creation.
func (m *Manager) AddCacheOwner(member domain.MemberID, pool, key string) error {
	if !validCacheOwner(key) {
		return fmt.Errorf("memberhome: invalid cache owner key")
	}
	return m.updateCacheMetadata(member, pool, true, func(meta *cacheMetadata) error {
		if !slices.Contains(meta.Owners, key) {
			if len(meta.Owners) >= maxCacheOwners {
				return fmt.Errorf("memberhome: too many pending cache owners")
			}
			meta.Owners = append(meta.Owners, key)
		}
		meta.LastUsed = time.Now().UTC()
		return nil
	})
}

// RemoveCacheOwner removes one key only after the caller confirms its runtime
// was destroyed or is absent. Actual release refreshes activity; a no-op does
// not make old caches young again. Missing pools are not recreated.
func (m *Manager) RemoveCacheOwner(member domain.MemberID, pool, key string) error {
	if !validCacheOwner(key) {
		return fmt.Errorf("memberhome: invalid cache owner key")
	}
	return m.updateCacheMetadata(member, pool, false, func(meta *cacheMetadata) error {
		if i := slices.Index(meta.Owners, key); i >= 0 {
			meta.Owners = slices.Delete(meta.Owners, i, i+1)
			meta.LastUsed = time.Now().UTC()
		}
		return nil
	})
}

// RemoveCache removes only data, never durable metadata. The caller must hold
// LockCaches through its runtime/store ownership check and this operation.
// The remover must unlink entries, not chmod/chown them: cache files may have
// external hardlinks. Neither deletion nor failed retries count as activity.
func (m *Manager) RemoveCache(ctx context.Context, member domain.MemberID, pool string) error {
	root, err := m.openCachePool(member, pool, false)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	meta, err := loadCacheMetadata(root, false)
	if err != nil {
		return err
	}
	if len(meta.Owners) != 0 {
		return fmt.Errorf("memberhome: cache still has pending runtime owners")
	}
	err = removeCacheDirectory(ctx, root, "data", m.cachePath(member, pool), m.removeHome)
	meta.DataCleanupPending = err != nil
	return errors.Join(err, saveCacheMetadata(root, meta))
}

func (m *Manager) updateCacheMetadata(member domain.MemberID, pool string, create bool, update func(*cacheMetadata) error) error {
	root, err := m.openCachePool(member, pool, create)
	if !create && errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	meta, err := loadCacheMetadata(root, create)
	if err != nil {
		return err
	}
	if err := update(&meta); err != nil {
		return err
	}
	return saveCacheMetadata(root, meta)
}

func (m *Manager) cachePath(member domain.MemberID, pool string) string {
	return filepath.Join(m.cacheRoot, string(member), pool, "data")
}

func (m *Manager) openCachePool(member domain.MemberID, pool string, create bool) (*os.Root, error) {
	if err := validateMemberID(string(member)); err != nil {
		return nil, fmt.Errorf("memberhome: invalid cache member: %w", err)
	}
	if pool != CachePoolRuns && pool != CachePoolTerminal {
		return nil, fmt.Errorf("memberhome: invalid cache pool")
	}
	root, err := openManagedRoot(m.cacheRoot, create)
	if err != nil {
		return nil, err
	}
	for _, name := range []string{string(member), pool} {
		next, err := openCacheDirectory(root, name, create)
		_ = root.Close()
		if err != nil {
			return nil, err
		}
		root = next
	}
	return root, nil
}

func loadCacheMetadata(root *os.Root, initialize bool) (cacheMetadata, error) {
	var meta cacheMetadata
	data, err := readRegularFile(root, cacheMetadataName, maxCacheMetadataBytes)
	if err != nil {
		return meta, fmt.Errorf("memberhome: read cache metadata: %w", err)
	}
	if data == nil {
		if initialize {
			entries, err := cacheDirectoryEntries(root)
			if err != nil {
				return meta, err
			}
			if len(entries) == 0 {
				return cacheMetadata{Version: 1, LastUsed: time.Now().UTC()}, nil
			}
		}
		return meta, fmt.Errorf("memberhome: cache ownership metadata is missing")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&meta); err != nil {
		return meta, fmt.Errorf("memberhome: invalid cache metadata")
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF || meta.Version != 1 || meta.LastUsed.IsZero() ||
		len(meta.CleanupError) > maxCacheErrorBytes || meta.CleanupError != boundedCacheError(meta.CleanupError) ||
		len(meta.RuntimeCleanupError) > maxCacheErrorBytes || meta.RuntimeCleanupError != boundedCacheError(meta.RuntimeCleanupError) ||
		len(meta.ImageCleanupError) > maxCacheErrorBytes || meta.ImageCleanupError != boundedCacheError(meta.ImageCleanupError) || len(meta.Owners) > maxCacheOwners {
		return meta, fmt.Errorf("memberhome: unsupported or invalid cache metadata")
	}
	if meta.LegacyProtected >= 1<<len(legacyCachePaths) {
		return meta, fmt.Errorf("memberhome: invalid legacy cache protection")
	}
	for i, owner := range meta.Owners {
		if !validCacheOwner(owner) || slices.Contains(meta.Owners[:i], owner) {
			return meta, fmt.Errorf("memberhome: invalid cache ownership metadata")
		}
	}
	// Upgrade the previous data diagnostic before any metadata mutation can
	// replace a generic error, preserving its independent durable retry.
	if meta.CleanupError == "cache cleanup failed; retry pending" {
		meta.DataCleanupPending = true
		meta.CleanupError = ""
	}
	if meta.CleanupError == "Saved image cleanup failed; automatic retry pending" {
		meta.ImageCleanupError = meta.CleanupError
		meta.CleanupError = ""
	}
	if meta.CleanupError == "Updater cleanup pending; cache protected" || meta.CleanupError == "Runtime ownership unavailable; cache protected" {
		meta.RuntimeCleanupError = meta.CleanupError
		meta.CleanupError = ""
	}
	return meta, nil
}

// saveCacheMetadata atomically replaces a small server-owned file, never
// opening a preexisting inode writable or changing another hardlink's mode.
func saveCacheMetadata(root *os.Root, meta cacheMetadata) error {
	data, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	if len(data) > maxCacheMetadataBytes {
		return fmt.Errorf("memberhome: cache ownership metadata exceeds size limit")
	}
	name := ".metadata-" + rand.Text()
	f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = root.Remove(name) }()
	_, writeErr := f.Write(data)
	if writeErr == nil {
		writeErr = f.Sync()
	}
	if err := errors.Join(writeErr, f.Close()); err != nil {
		return err
	}
	if err := root.Rename(name, cacheMetadataName); err != nil {
		return err
	}
	return syncCacheDirectory(root)
}

func validCacheOwner(key string) bool {
	return key != "" && len(key) <= maxCacheOwnerBytes && utf8.ValidString(key) && strings.IndexFunc(key, unicode.IsControl) == -1
}

func boundedCacheError(message string) string {
	message = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, strings.ToValidUTF8(message, ""))
	message = strings.Join(strings.Fields(message), " ")
	if len(message) > maxCacheErrorBytes {
		message = message[:maxCacheErrorBytes]
		for !utf8.ValidString(message) {
			message = message[:len(message)-1]
		}
	}
	return strings.TrimSpace(message)
}

func removeCacheDirectory(ctx context.Context, root *os.Root, name, absolute string, remove func(context.Context, string) error) error {
	dir, err := rootfs.OpenRoot(root, name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("memberhome: unsafe cache deletion target: %w", err)
	}
	_ = dir.Close()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := remove(ctx, absolute); err != nil {
		return err
	}
	if _, err := root.Lstat(name); !errors.Is(err, fs.ErrNotExist) {
		if err != nil {
			return err
		}
		return fmt.Errorf("memberhome: cache remover left data behind")
	}
	return syncCacheDirectory(root)
}
