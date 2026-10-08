package memberhome

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCacheRefusesSymlinksAtEveryOwnedBoundary(t *testing.T) {
	for _, boundary := range []string{"root", "member", "pool", "data"} {
		t.Run(boundary, func(t *testing.T) {
			calls := 0
			manager := newCacheManager(t, func(context.Context, string) error {
				calls++
				return nil
			})
			outside := t.TempDir()
			victim := filepath.Join(outside, "keep")
			if err := os.WriteFile(victim, []byte("credential"), 0o600); err != nil {
				t.Fatal(err)
			}
			var target string
			switch boundary {
			case "root":
				target = manager.CacheRoot()
			case "member":
				target = filepath.Join(manager.CacheRoot(), "member")
			case "pool":
				target = filepath.Join(manager.CacheRoot(), "member", CachePoolRuns)
			case "data":
				data, err := manager.CachePath("member", CachePoolRuns)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(data); err != nil {
					t.Fatal(err)
				}
				target = data
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, target); err != nil {
				t.Fatal(err)
			}
			if _, err := manager.CachePath("member", CachePoolRuns); err == nil {
				t.Fatal("created cache through symlink")
			}
			if err := manager.RemoveCache(t.Context(), "member", CachePoolRuns); err == nil {
				t.Fatal("removed cache through symlink")
			}
			if calls != 0 {
				t.Fatalf("unsafe target reached path remover %d times", calls)
			}
			if got, err := os.ReadFile(victim); err != nil || string(got) != "credential" {
				t.Fatalf("external target changed: %q, %v", got, err)
			}
			if entries, err := os.ReadDir(outside); err != nil || len(entries) != 1 {
				t.Fatalf("external directory mutated: %v, %v", entries, err)
			}
		})
	}
}

func TestCacheUncertainMetadataCannotBecomeFreshOwnership(t *testing.T) {
	for _, corruption := range []string{"missing", "empty", "malformed", "unknown-version", "unknown-field", "oversized", "symlink", "hardlink"} {
		t.Run(corruption, func(t *testing.T) {
			calls := 0
			manager := newCacheManager(t, func(context.Context, string) error {
				calls++
				return nil
			})
			data, err := manager.CachePath("member", CachePoolRuns)
			if err != nil {
				t.Fatal(err)
			}
			metadata := filepath.Join(filepath.Dir(data), cacheMetadataName)
			original, err := os.ReadFile(metadata)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.Remove(metadata); err != nil {
				t.Fatal(err)
			}
			outside := filepath.Join(t.TempDir(), "outside")
			if err = os.WriteFile(outside, original, 0o640); err != nil {
				t.Fatal(err)
			}
			switch corruption {
			case "missing":
			case "empty":
				err = os.WriteFile(metadata, nil, 0o600)
			case "malformed":
				err = os.WriteFile(metadata, []byte("{"), 0o600)
			case "unknown-version":
				err = os.WriteFile(metadata, []byte(strings.Replace(string(original), `"version":1`, `"version":2`, 1)), 0o600)
			case "unknown-field":
				err = os.WriteFile(metadata, []byte(strings.Replace(string(original), "{", `{"unknown_owner":"held",`, 1)), 0o600)
			case "oversized":
				err = os.WriteFile(metadata, []byte(strings.Repeat(" ", maxCacheMetadataBytes+1)), 0o600)
			case "symlink":
				err = os.Symlink(outside, metadata)
			case "hardlink":
				err = os.Link(outside, metadata)
			}
			if err != nil {
				t.Fatal(err)
			}
			info, err := manager.ReadCache("member", CachePoolRuns)
			if err != nil || info.MetadataError == "" || !info.DataExists {
				t.Fatalf("uncertain metadata was hidden: %+v, %v", info, err)
			}
			if entries, err := manager.ListCaches(); err != nil || len(entries) != 1 || entries[0].MetadataError == "" {
				t.Fatalf("uncertain pool disappeared: %+v, %v", entries, err)
			}
			if _, err := manager.CachePath("member", CachePoolRuns); err == nil {
				t.Fatal("uncertain ownership replaced by fresh use")
			}
			if err := manager.TouchCache("member", CachePoolRuns); err == nil {
				t.Fatal("uncertain ownership refreshed")
			}
			if err := manager.SetCacheCleanupError("member", CachePoolRuns, "retry"); err == nil {
				t.Fatal("uncertain metadata overwritten")
			}
			if err := manager.RemoveCache(t.Context(), "member", CachePoolRuns); err == nil || calls != 0 {
				t.Fatalf("uncertain ownership deleted: calls=%d, err=%v", calls, err)
			}
			if contents, err := os.ReadFile(outside); err != nil || string(contents) != string(original) {
				t.Fatalf("external metadata target changed: %q, %v", contents, err)
			}
			if info, err := os.Stat(outside); err != nil || info.Mode().Perm() != 0o640 {
				t.Fatalf("external metadata target permissions changed: %v, %v", info, err)
			}
		})
	}
}

func TestCacheDeletionOnlyUnlinksExternalLinks(t *testing.T) {
	manager := newCacheManager(t, nil)
	data, cacheErr := manager.CachePath("member", CachePoolRuns)
	if cacheErr != nil {
		t.Fatal(cacheErr)
	}
	outside := t.TempDir()
	victim := filepath.Join(outside, "credential")
	if cacheErr = os.WriteFile(victim, []byte("secret"), 0o640); cacheErr != nil {
		t.Fatal(cacheErr)
	}
	before, cacheErr := os.Stat(victim)
	if cacheErr != nil {
		t.Fatal(cacheErr)
	}
	if cacheErr = os.Link(victim, filepath.Join(data, "hardlink")); cacheErr != nil {
		t.Fatal(cacheErr)
	}
	if cacheErr = os.Symlink(outside, filepath.Join(data, "symlink")); cacheErr != nil {
		t.Fatal(cacheErr)
	}
	if _, err := manager.CachePath("member", CachePoolRuns); err != nil {
		t.Fatal(err)
	}
	if err := manager.RemoveCache(t.Context(), "member", CachePoolRuns); err != nil {
		t.Fatal(err)
	}
	after, cacheErr := os.Stat(victim)
	if cacheErr != nil || !os.SameFile(before, after) || before.Mode() != after.Mode() || !before.ModTime().Equal(after.ModTime()) {
		t.Fatalf("external inode mutated: before=%v, after=%v, err=%v", before, after, cacheErr)
	}
	if contents, err := os.ReadFile(victim); err != nil || string(contents) != "secret" {
		t.Fatalf("external bytes mutated: %q, %v", contents, err)
	}
	if _, err := os.Stat(data); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("cache data was not removed: %v", err)
	}
}

func TestHomeRemovalRefusesSymlinkedMemberAndRoot(t *testing.T) {
	for _, targetRoot := range []bool{false, true} {
		manager := newCacheManager(t, nil)
		outside := t.TempDir()
		victim := filepath.Join(outside, "member", "keep")
		if err := os.MkdirAll(filepath.Dir(victim), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(victim, []byte("safe"), 0o600); err != nil {
			t.Fatal(err)
		}
		link, destination := manager.Root(), outside
		if !targetRoot {
			if err := os.Mkdir(manager.Root(), 0o700); err != nil {
				t.Fatal(err)
			}
			link, destination = filepath.Join(manager.Root(), "member"), filepath.Dir(victim)
		}
		if err := os.Symlink(destination, link); err != nil {
			t.Fatal(err)
		}
		if err := manager.Remove(t.Context(), "member"); err == nil {
			t.Fatal("home removal accepted symlinked root/member")
		}
		if contents, err := os.ReadFile(victim); err != nil || string(contents) != "safe" {
			t.Fatalf("external home damaged: %q, %v", contents, err)
		}
	}
}
