//go:build linux

package memberhome

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
)

func writeLegacyFile(t *testing.T, home, relative, content string) string {
	t.Helper()
	name := filepath.Join(home, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(content), 0o640); err != nil {
		t.Fatal(err)
	}
	return name
}

func TestLegacyCacheRemovalPreservesCredentialsAndInstalledTools(t *testing.T) {
	manager := newCacheManager(t, nil)
	home, homeErr := manager.Path("member")
	if homeErr != nil {
		t.Fatal(homeErr)
	}
	preserve := []string{
		".npm/_npx/tool/bin/tool", ".npm/npmrc", ".npm/bin/installed", ".cache/custom/valuable",
		".local/bin/agent", ".local/lib/tool", ".config/gh/hosts.yml", ".ssh/aether_signing",
		".claude/.credentials.json", "go/bin/tool", "go/src/project/main.go", "project/main.go",
	}
	for _, relative := range preserve {
		writeLegacyFile(t, home, relative, "preserve")
	}
	for _, relative := range legacyCachePaths {
		writeLegacyFile(t, home, relative+"/entry", "reconstructible")
	}
	victim := filepath.Join(home, ".ssh", "aether_signing")
	before, homeErr := os.Stat(victim)
	if homeErr != nil {
		t.Fatal(homeErr)
	}
	if homeErr = os.Link(victim, filepath.Join(home, ".npm", "_cacache", "hardlink")); homeErr != nil {
		t.Fatal(homeErr)
	}
	if homeErr = os.Symlink(filepath.Join(home, ".ssh"), filepath.Join(home, ".cache", "pip", "symlink")); homeErr != nil {
		t.Fatal(homeErr)
	}
	unlock := manager.LockCaches("member")
	defer unlock()
	if homeErr = manager.RemoveLegacyCaches(t.Context(), "member"); homeErr != nil {
		t.Fatal(homeErr)
	}
	for _, relative := range legacyCachePaths {
		if _, err := os.Stat(filepath.Join(home, filepath.FromSlash(relative))); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("legacy cache %q retained: %v", relative, err)
		}
	}
	for _, relative := range preserve {
		if data, err := os.ReadFile(filepath.Join(home, filepath.FromSlash(relative))); err != nil || string(data) != "preserve" {
			t.Errorf("valuable path %q changed: %q, %v", relative, data, err)
		}
	}
	after, homeErr := os.Stat(victim)
	if homeErr != nil {
		t.Fatal(homeErr)
	}
	beforeStat, afterStat := before.Sys().(*syscall.Stat_t), after.Sys().(*syscall.Stat_t)
	if !os.SameFile(before, after) || before.Mode() != after.Mode() || beforeStat.Uid != afterStat.Uid || beforeStat.Gid != afterStat.Gid {
		t.Fatalf("hard-linked credential mutated: before=%+v, after=%+v", beforeStat, afterStat)
	}
	if err := manager.RemoveLegacyCaches(t.Context(), "member"); err != nil {
		t.Fatalf("idempotent legacy removal: %v", err)
	}
	info, homeErr := manager.ReadCache("member", CachePoolRuns)
	if homeErr != nil || info.DataExists || info.CleanupError != "" {
		t.Fatalf("legacy-only cleanup created pool data: %+v, %v", info, homeErr)
	}
}

func TestLegacyCleanupRetrySurvivesDeletedHomeAndManagerRestart(t *testing.T) {
	failed := true
	manager := newCacheManager(t, func(_ context.Context, name string) error {
		if failed {
			return fs.ErrPermission
		}
		return os.RemoveAll(name)
	})
	home, homeErr := manager.Path("deleted")
	if homeErr != nil {
		t.Fatal(homeErr)
	}
	writeLegacyFile(t, home, ".cache/uv/entry", "cache")
	if homeErr = manager.RemoveLegacyCaches(t.Context(), "deleted"); homeErr == nil {
		t.Fatal("legacy remover failure was hidden")
	}
	info, homeErr := manager.ReadCache("deleted", CachePoolRuns)
	if homeErr != nil || info.CleanupError == "" || info.DataExists {
		t.Fatalf("legacy retry metadata = %+v, %v", info, homeErr)
	}
	if _, statErr := os.Stat(filepath.Join(home, ".cache", "uv")); !errors.Is(statErr, fs.ErrNotExist) {
		t.Fatalf("failed cache was not moved out of user-mutable home: %v", statErr)
	}
	failed = false
	if err := manager.Remove(t.Context(), "deleted"); err != nil {
		t.Fatal(err)
	}
	restarted, restartErr := New(manager.Root(), manager.CacheRoot(), nil)
	if restartErr != nil {
		t.Fatal(restartErr)
	}
	members, restartErr := restarted.CacheMembers()
	if restartErr != nil || !slices.Equal(members, []domain.MemberID{"deleted"}) {
		t.Fatalf("deleted legacy retry owner disappeared: %v, %v", members, restartErr)
	}
	if err := restarted.RemoveLegacyCaches(t.Context(), "deleted"); err != nil {
		t.Fatal(err)
	}
	info, restartErr = restarted.ReadCache("deleted", CachePoolRuns)
	if restartErr != nil || info.CleanupError != "" {
		t.Fatalf("legacy retry did not clear failure: %+v, %v", info, restartErr)
	}
	entries, restartErr := os.ReadDir(filepath.Dir(info.Path))
	if restartErr != nil || len(entries) != 1 || entries[0].Name() != cacheMetadataName {
		t.Fatalf("staged legacy retry not removed: %v, %v", entries, restartErr)
	}
}

func TestLegacyCleanupRefusesSymlinkParentsAndLeavesRetryMarker(t *testing.T) {
	for _, leaf := range []bool{false, true} {
		manager := newCacheManager(t, nil)
		home, homeErr := manager.Path("member")
		if homeErr != nil {
			t.Fatal(homeErr)
		}
		outside := t.TempDir()
		victim := writeLegacyFile(t, outside, "pip/keep", "valuable")
		link, target := filepath.Join(home, ".cache"), outside
		if leaf {
			if homeErr = os.Mkdir(link, 0o700); homeErr != nil {
				t.Fatal(homeErr)
			}
			link, target = filepath.Join(link, "pip"), filepath.Dir(victim)
		}
		if homeErr = os.Symlink(target, link); homeErr != nil {
			t.Fatal(homeErr)
		}
		writeLegacyFile(t, home, ".npm/_logs/log", "reconstructible")
		if homeErr = manager.RemoveLegacyCaches(t.Context(), "member"); homeErr == nil {
			t.Fatal("symlinked legacy cache accepted")
		}
		if data, err := os.ReadFile(victim); err != nil || string(data) != "valuable" {
			t.Fatalf("symlink target changed: %q, %v", data, err)
		}
		if _, err := os.Lstat(link); err != nil {
			t.Fatalf("refused symlink was removed: %v", err)
		}
		if _, err := os.Stat(filepath.Join(home, ".npm", "_logs")); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("independent safe cache not reclaimed: %v", err)
		}
		info, homeErr := manager.ReadCache("member", CachePoolRuns)
		if homeErr != nil || info.CleanupError == "" {
			t.Fatalf("unsafe legacy cache missing retry marker: %+v, %v", info, homeErr)
		}
		if err := os.Remove(link); err != nil {
			t.Fatal(err)
		}
		if err := manager.RemoveLegacyCaches(t.Context(), "member"); err != nil {
			t.Fatalf("resolved symlink did not permit retry: %v", err)
		}
	}
}

func TestLegacyStagingNeverFollowsReplacedParent(t *testing.T) {
	manager := newCacheManager(t, nil)
	home, err := manager.Path("member")
	if err != nil {
		t.Fatal(err)
	}
	writeLegacyFile(t, home, ".cache/pip/entry", "cache")
	source, err := os.OpenRoot(filepath.Join(home, ".cache"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = source.Close() }()
	if err = manager.SetCacheCleanupError("member", CachePoolRuns, "retry"); err != nil {
		t.Fatal(err)
	}
	target, err := manager.openCachePool("member", CachePoolRuns, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = target.Close() }()
	outside := t.TempDir()
	victim := writeLegacyFile(t, outside, "pip/keep", "valuable")
	if err := os.Rename(filepath.Join(home, ".cache"), filepath.Join(home, "old-cache")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(home, ".cache")); err != nil {
		t.Fatal(err)
	}
	if err := renameCacheEntry(source, "pip", target, ".legacy-cache-2"); err != nil {
		t.Fatal(err)
	}
	if err := removeCacheDirectory(t.Context(), target, ".legacy-cache-2", filepath.Join(manager.CacheRoot(), "member", CachePoolRuns, ".legacy-cache-2"), manager.removeHome); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(victim); err != nil || string(data) != "valuable" {
		t.Fatalf("renamed parent redirected deletion: %q, %v", data, err)
	}
}
