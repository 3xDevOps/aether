//go:build linux

package scheduler

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Every launch walks the member's whole home and caches, so an entry's cost
// must not grow with its depth: resolving each path from the root again made
// a 400k-entry pass take six seconds. A chain and a flat tree with the same
// directories and files must walk in about the same time.
func TestChownTreeCostDoesNotGrowWithDepth(t *testing.T) {
	t.Parallel()
	const dirs = 800
	deep := filepath.Join(t.TempDir(), "deep")
	if err := os.MkdirAll(filepath.Join(deep, strings.Repeat("d/", dirs)), 0o755); err != nil {
		t.Fatal(err)
	}
	flat := filepath.Join(t.TempDir(), "flat")
	for i := range dirs {
		if err := os.MkdirAll(filepath.Join(flat, strconv.Itoa(i)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	walk := func(dir string) time.Duration {
		t.Helper()
		best := time.Duration(1 << 62)
		for range 3 {
			start := time.Now()
			if err := chownTree(dir, os.Getuid(), os.Getgid(), nil); err != nil {
				t.Fatal(err)
			}
			best = min(best, time.Since(start))
		}
		return best
	}
	deepTime, flatTime := walk(deep), walk(flat)
	if deepTime > 10*flatTime+20*time.Millisecond {
		t.Fatalf("walking %d nested directories took %s, %d side by side %s", dirs, deepTime, dirs, flatTime)
	}
}

// The pass hands the run user everything it does not own however deep, chowns
// a symlink itself rather than its target, leaves a hard-linked file alone,
// and does not touch an entry the user already owns, whose inode a chown
// would rewrite.
func TestChownTreeOwnershipRules(t *testing.T) {
	t.Parallel()
	if os.Geteuid() != 0 {
		t.Skip("ownership pass needs root to chown")
	}
	home := filepath.Join(t.TempDir(), "home")
	outside := filepath.Join(t.TempDir(), "outside")
	nested := filepath.Join(home, "a", "b", "c")
	for _, dir := range []string{nested, outside} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(path string) {
		t.Helper()
		if err := os.WriteFile(path, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(nested, "deep"))
	write(filepath.Join(outside, "target"))
	write(filepath.Join(home, "linked"))
	if err := os.Link(filepath.Join(home, "linked"), filepath.Join(outside, "other-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(home, "a", "escape")); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(home, "owned"))
	if err := os.Lchown(filepath.Join(home, "owned"), 1000, 1000); err != nil {
		t.Fatal(err)
	}
	stat := func(path string) *syscall.Stat_t {
		t.Helper()
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		return info.Sys().(*syscall.Stat_t)
	}
	ownedBefore := stat(filepath.Join(home, "owned")).Ctim
	time.Sleep(10 * time.Millisecond)

	if err := chownTree(home, 1000, 1000, nil); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{".", "a", "a/b", "a/b/c", "a/b/c/deep", "a/escape"} {
		if st := stat(filepath.Join(home, rel)); st.Uid != 1000 || st.Gid != 1000 {
			t.Errorf("%s owned by %d:%d, want 1000:1000", rel, st.Uid, st.Gid)
		}
	}
	for _, path := range []string{outside, filepath.Join(outside, "target"), filepath.Join(home, "linked")} {
		if st := stat(path); st.Uid != 0 {
			t.Errorf("%s chowned to %d", path, st.Uid)
		}
	}
	if after := stat(filepath.Join(home, "owned")).Ctim; after != ownedBefore {
		t.Errorf("an entry the user already owned was chowned again")
	}
}
