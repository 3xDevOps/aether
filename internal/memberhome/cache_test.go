package memberhome

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/3xDevOps/Aether/internal/domain"
)

func newCacheManager(t *testing.T, remove func(context.Context, string) error) *Manager {
	t.Helper()
	base := t.TempDir()
	manager, err := New(filepath.Join(base, "homes"), filepath.Join(base, "home-caches"), remove)
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

func setCacheLastUsed(t *testing.T, manager *Manager, member domain.MemberID, pool string, used time.Time) {
	t.Helper()
	if err := manager.updateCacheMetadata(member, pool, false, func(meta *cacheMetadata) error {
		meta.LastUsed = used
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCacheRootsAndReadOnlyDiscovery(t *testing.T) {
	manager := newCacheManager(t, nil)
	for _, roots := range [][2]string{{"", manager.CacheRoot()}, {manager.Root(), ""},
		{manager.Root(), manager.Root()}, {manager.Root(), filepath.Join(manager.Root(), "cache")},
		{filepath.Join(manager.CacheRoot(), "homes"), manager.CacheRoot()}} {
		if _, err := New(roots[0], roots[1], nil); err == nil {
			t.Fatalf("New(%q, %q) accepted missing/overlapping roots", roots[0], roots[1])
		}
	}
	if entries, err := manager.ListCaches(); err != nil || len(entries) != 0 {
		t.Fatalf("initial inventory = %v, %v", entries, err)
	}
	if members, err := manager.CacheMembers(); err != nil || len(members) != 0 {
		t.Fatalf("initial members = %v, %v", members, err)
	}
	if err := manager.TouchCache("member", CachePoolRuns); err != nil {
		t.Fatal(err)
	}
	for _, root := range []string{manager.Root(), manager.CacheRoot()} {
		if _, err := os.Stat(root); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("read-only lookup created root %q: %v", root, err)
		}
	}
	for _, member := range []domain.MemberID{"../member", "member/name", ".member", ""} {
		if _, err := manager.CachePath(member, CachePoolRuns); err == nil {
			t.Fatalf("accepted unsafe member %q", member)
		}
	}
	for _, pool := range []string{"", "other", "../runs", "runs/data"} {
		if _, err := manager.CachePath("member", pool); err == nil {
			t.Fatalf("accepted unsafe pool %q", pool)
		}
	}
	if _, err := manager.Path("home-only"); err != nil {
		t.Fatal(err)
	}
	if err := manager.SetCacheCleanupError("deleted", CachePoolTerminal, "image cleanup pending"); err != nil {
		t.Fatal(err)
	}
	members, err := manager.CacheMembers()
	if err != nil || !slices.Equal(members, []domain.MemberID{"deleted", "home-only"}) {
		t.Fatalf("retry discovery = %v, %v", members, err)
	}
	info, err := manager.ReadCache("deleted", CachePoolTerminal)
	if err != nil || info.DataExists || info.CleanupError != "image cleanup pending" {
		t.Fatalf("metadata-only retry marker = %+v, %v", info, err)
	}
	if _, err := os.Stat(filepath.Join(manager.Root(), "deleted")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("retry marker created home: %v", err)
	}
}

func TestCacheActivityAndPoolIsolationSurviveRestart(t *testing.T) {
	manager := newCacheManager(t, nil)
	unlock := manager.LockCaches("member")
	defer unlock()
	for _, pool := range []string{CachePoolRuns, CachePoolTerminal} {
		data, err := manager.CachePath("member", pool)
		if err != nil {
			t.Fatal(err)
		}
		if want := filepath.Join(manager.CacheRoot(), "member", pool, "data"); data != want {
			t.Fatalf("cache path = %q, want %q", data, want)
		}
		if _, err := os.Stat(filepath.Join(data, cacheMetadataName)); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("metadata exposed inside writable data: %v", err)
		}
		if err := os.WriteFile(filepath.Join(data, "keep"), []byte(pool), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	setCacheLastUsed(t, manager, "member", CachePoolRuns, old)
	setCacheLastUsed(t, manager, "member", CachePoolTerminal, old)
	if err := manager.TouchCache("member", CachePoolRuns); err != nil {
		t.Fatal(err)
	}
	restarted, err := New(manager.Root(), manager.CacheRoot(), nil)
	if err != nil {
		t.Fatal(err)
	}
	runs, err := restarted.ReadCache("member", CachePoolRuns)
	if err != nil || !runs.LastUsed.After(old) || !runs.DataExists || runs.MetadataError != "" {
		t.Fatalf("activity after restart = %+v, %v", runs, err)
	}
	terminal, err := restarted.ReadCache("member", CachePoolTerminal)
	if err != nil || !terminal.LastUsed.Equal(old) {
		t.Fatalf("other pool's activity changed = %+v, %v", terminal, err)
	}
	if err = restarted.RemoveCache(t.Context(), "member", CachePoolRuns); err != nil {
		t.Fatal(err)
	}
	after, err := restarted.ReadCache("member", CachePoolRuns)
	if err != nil || after.DataExists || !after.LastUsed.Equal(runs.LastUsed) {
		t.Fatalf("metadata after data removal = %+v, %v", after, err)
	}
	if got, err := os.ReadFile(filepath.Join(terminal.Path, "keep")); err != nil || string(got) != CachePoolTerminal {
		t.Fatalf("other pool damaged: %q, %v", got, err)
	}
	if entries, err := restarted.ListCaches(); err != nil || len(entries) != 2 {
		t.Fatalf("metadata-only pool disappeared: %+v, %v", entries, err)
	}
}

func TestCacheFailedRemovalKeepsDurableRetryAndActivity(t *testing.T) {
	failed := true
	calls := 0
	manager := newCacheManager(t, func(_ context.Context, name string) error {
		calls++
		if failed {
			return errors.New("private host path /private/secret: permission denied")
		}
		return os.RemoveAll(name)
	})
	data, err := manager.CachePath("removed-member", CachePoolRuns)
	if err != nil {
		t.Fatal(err)
	}
	old := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	setCacheLastUsed(t, manager, "removed-member", CachePoolRuns, old)
	if err = manager.RemoveCache(t.Context(), "removed-member", CachePoolRuns); err == nil {
		t.Fatal("cleanup failure was hidden")
	}
	restarted, err := New(manager.Root(), manager.CacheRoot(), manager.removeHome)
	if err != nil {
		t.Fatal(err)
	}
	info, err := restarted.ReadCache("removed-member", CachePoolRuns)
	if err != nil || !info.DataExists || info.CleanupError == "" || strings.Contains(info.CleanupError, "/private") || !info.LastUsed.Equal(old) {
		t.Fatalf("durable retry state = %+v, %v", info, err)
	}
	failed = false
	if err = restarted.RemoveCache(t.Context(), "removed-member", CachePoolRuns); err != nil {
		t.Fatal(err)
	}
	info, err = restarted.ReadCache("removed-member", CachePoolRuns)
	if err != nil || info.DataExists || info.CleanupError != "" || !info.LastUsed.Equal(old) || calls != 2 {
		t.Fatalf("retry completion = %+v, calls %d, %v", info, calls, err)
	}
	if _, err := os.Stat(data); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("retry left data behind: %v", err)
	}
	if members, err := restarted.CacheMembers(); err != nil || !slices.Equal(members, []domain.MemberID{"removed-member"}) {
		t.Fatalf("image retry owner lost after cache deletion: %v, %v", members, err)
	}
}

func TestCacheRemoverMustActuallyRemoveData(t *testing.T) {
	manager := newCacheManager(t, func(context.Context, string) error { return nil })
	if _, err := manager.CachePath("member", CachePoolRuns); err != nil {
		t.Fatal(err)
	}
	if err := manager.RemoveCache(t.Context(), "member", CachePoolRuns); err == nil {
		t.Fatal("unconfirmed cleanup accepted")
	}
	info, err := manager.ReadCache("member", CachePoolRuns)
	if err != nil || !info.DataExists || info.CleanupError == "" {
		t.Fatalf("unconfirmed cleanup lost retry state: %+v, %v", info, err)
	}
}

func TestCacheOwnersPersistBeforeCreationAndUntilConfirmedRelease(t *testing.T) {
	manager := newCacheManager(t, nil)
	if err := manager.AddCacheOwner("member", CachePoolTerminal, "updater-key"); err != nil {
		t.Fatal(err)
	}
	restarted, restartErr := New(manager.Root(), manager.CacheRoot(), nil)
	if restartErr != nil {
		t.Fatal(restartErr)
	}
	info, restartErr := restarted.ReadCache("member", CachePoolTerminal)
	if restartErr != nil || info.DataExists || !slices.Equal(info.Owners, []string{"updater-key"}) {
		t.Fatalf("pre-create ownership = %+v, %v", info, restartErr)
	}
	if _, restartErr = restarted.CachePath("member", CachePoolTerminal); restartErr != nil {
		t.Fatal(restartErr)
	}
	if restartErr = restarted.RemoveCache(t.Context(), "member", CachePoolTerminal); restartErr == nil {
		t.Fatal("deleted cache with persisted owner")
	}
	old := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	setCacheLastUsed(t, restarted, "member", CachePoolTerminal, old)
	if restartErr = restarted.RemoveCacheOwner("member", CachePoolTerminal, "unknown-key"); restartErr != nil {
		t.Fatal(restartErr)
	}
	info, restartErr = restarted.ReadCache("member", CachePoolTerminal)
	if restartErr != nil || !info.LastUsed.Equal(old) || len(info.Owners) != 1 {
		t.Fatalf("unknown release changed ownership: %+v, %v", info, restartErr)
	}
	if err := restarted.RemoveCacheOwner("member", CachePoolTerminal, "updater-key"); err != nil {
		t.Fatal(err)
	}
	info, restartErr = restarted.ReadCache("member", CachePoolTerminal)
	if restartErr != nil || !info.LastUsed.After(old) || len(info.Owners) != 0 {
		t.Fatalf("confirmed release did not refresh use: %+v, %v", info, restartErr)
	}
	if err := restarted.RemoveCache(t.Context(), "member", CachePoolTerminal); err != nil {
		t.Fatal(err)
	}
	if err := restarted.AddCacheOwner("member", CachePoolTerminal, strings.Repeat("x", maxCacheOwnerBytes+1)); err == nil {
		t.Fatal("accepted unbounded owner")
	}
	message := strings.Repeat("error\nλ", maxCacheErrorBytes)
	if err := restarted.SetCacheCleanupError("member", CachePoolTerminal, message); err != nil {
		t.Fatal(err)
	}
	info, restartErr = restarted.ReadCache("member", CachePoolTerminal)
	if restartErr != nil || len(info.CleanupError) > maxCacheErrorBytes || !utf8.ValidString(info.CleanupError) || strings.Contains(info.CleanupError, "\n") {
		t.Fatalf("diagnostic not bounded valid text: %+v, %v", info, restartErr)
	}
}

func TestCacheLocksSeparateMembersAndHomeConfiguration(t *testing.T) {
	manager := newCacheManager(t, nil)
	unlock := manager.LockCaches("member")
	if release, ok := manager.TryLockCaches("member"); ok {
		release()
		t.Fatal("maintenance bypassed member cache lock")
	}
	other, ok := manager.TryLockCaches("other")
	if !ok {
		t.Fatal("one member blocked another member")
	}
	other()
	configUnlock, err := manager.LockConfigRoot("member", ".claude")
	if err != nil {
		t.Fatal(err)
	}
	configUnlock()
	// Manager operations do not reenter the externally held cache lock.
	if _, err := manager.CachePath("member", CachePoolRuns); err != nil {
		t.Fatal(err)
	}
	unlock()
	if release, ok := manager.TryLockCaches("member"); !ok {
		t.Fatal("cache lock stayed held")
	} else {
		release()
	}
}

func TestCacheOwnershipBoundsNeverDiscardExistingOwners(t *testing.T) {
	manager := newCacheManager(t, nil)
	owners := make([]string, maxCacheOwners)
	for i := range owners {
		owners[i] = time.Unix(int64(i), 0).UTC().Format(time.RFC3339)
	}
	if err := manager.updateCacheMetadata("member", CachePoolTerminal, true, func(meta *cacheMetadata) error {
		meta.Owners = owners
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := manager.AddCacheOwner("member", CachePoolTerminal, "one-too-many"); err == nil {
		t.Fatal("accepted runtime creation after owner limit")
	}
	if err := manager.AddCacheOwner("member", CachePoolTerminal, "invalid-\xff"); err == nil {
		t.Fatal("accepted non-round-trippable owner key")
	}
	if err := manager.updateCacheMetadata("member", CachePoolTerminal, false, func(meta *cacheMetadata) error {
		meta.Owners = slices.Clone(meta.Owners)
		for i := range meta.Owners {
			meta.Owners[i] += strings.Repeat("<", 480)
		}
		return nil
	}); err == nil {
		t.Fatal("accepted oversized encoded ownership metadata")
	}
	info, err := manager.ReadCache("member", CachePoolTerminal)
	if err != nil || info.MetadataError != "" || !slices.Equal(info.Owners, owners) {
		t.Fatalf("failed ownership update damaged durable keys: %+v, %v", info, err)
	}
}

func TestCacheCleanupOperationsKeepIndependentDurableFailures(t *testing.T) {
	failed := true
	manager := newCacheManager(t, func(_ context.Context, path string) error {
		if failed {
			return errors.New("partial removal")
		}
		return os.RemoveAll(path)
	})
	if _, err := manager.CachePath("member", CachePoolTerminal); err != nil {
		t.Fatal(err)
	}
	if err := manager.SetCacheCleanupError("member", CachePoolTerminal, "unrelated cleanup failed"); err != nil {
		t.Fatal(err)
	}
	if err := manager.SetCacheImageCleanupError("member", "image cleanup failed"); err != nil {
		t.Fatal(err)
	}
	if err := manager.RemoveCache(t.Context(), "member", CachePoolTerminal); err == nil {
		t.Fatal("expected data failure")
	}
	if err := manager.EnsureCacheMetadata("member", CachePoolTerminal); err != nil {
		t.Fatal(err)
	}
	if err := manager.AddCacheOwner("member", CachePoolTerminal, "updater"); err != nil {
		t.Fatal(err)
	}
	if err := manager.RemoveCacheOwner("member", CachePoolTerminal, "updater"); err != nil {
		t.Fatal(err)
	}
	restarted, err := New(manager.Root(), manager.CacheRoot(), manager.removeHome)
	if err != nil {
		t.Fatal(err)
	}
	info, err := restarted.ReadCache("member", CachePoolTerminal)
	if err != nil || !info.DataCleanupPending || !strings.Contains(info.CleanupError, "image cleanup failed") || !strings.Contains(info.CleanupError, "unrelated cleanup failed") {
		t.Fatalf("independent retry state lost: %+v, %v", info, err)
	}
	if err = restarted.SetCacheImageCleanupError("member", ""); err != nil {
		t.Fatal(err)
	}
	info, err = restarted.ReadCache("member", CachePoolTerminal)
	if err != nil || !info.DataCleanupPending || !strings.Contains(info.CleanupError, "unrelated cleanup failed") {
		t.Fatalf("image success cleared another failure: %+v, %v", info, err)
	}
	failed = false
	if err = restarted.RemoveCache(t.Context(), "member", CachePoolTerminal); err != nil {
		t.Fatal(err)
	}
	info, err = restarted.ReadCache("member", CachePoolTerminal)
	if err != nil || info.DataCleanupPending || info.CleanupError != "unrelated cleanup failed" {
		t.Fatalf("data success cleared unrelated cause: %+v, %v", info, err)
	}
}
