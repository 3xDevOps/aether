//go:build linux

package scheduler

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Every launch walks the member's whole home and caches, so a file's cost
// must not grow with how deep it sits: files spread down a chain of
// directories and the same files side by side walk in about the same time.
func TestChownTreeCostDoesNotGrowWithDepth(t *testing.T) {
	t.Parallel()
	const dirs, files = maxHeldDirs, 40
	populate := func(dir string) {
		t.Helper()
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for i := range files {
			if err := os.WriteFile(filepath.Join(dir, strconv.Itoa(i)), nil, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	deep := filepath.Join(t.TempDir(), "deep")
	flat := filepath.Join(t.TempDir(), "flat")
	for i := range dirs {
		populate(filepath.Join(deep, strings.Repeat("d/", i+1)))
		populate(filepath.Join(flat, strconv.Itoa(i)))
	}
	walk := func(dir string) time.Duration {
		t.Helper()
		best := time.Duration(1 << 62)
		for range 5 {
			start := time.Now()
			if err := chownTree(dir, os.Getuid(), os.Getgid(), nil); err != nil {
				t.Fatal(err)
			}
			best = min(best, time.Since(start))
		}
		return best
	}
	deepTime, flatTime := walk(deep), walk(flat)
	if deepTime > 4*flatTime+5*time.Millisecond {
		t.Fatalf("walking %d files down %d directories took %s, side by side %s", dirs*files, dirs, deepTime, flatTime)
	}
}

// However deep a run nests directories, one walk holds a bounded number of
// descriptors. The walk runs in a child process so the lowered limit leaves
// the other tests alone.
func TestChownTreeHoldsBoundedDescriptors(t *testing.T) {
	t.Parallel()
	if dir := os.Getenv("AETHER_TEST_CHOWN_FD_CHILD"); dir != "" {
		limit := syscall.Rlimit{Cur: maxHeldDirs + 32, Max: maxHeldDirs + 32}
		if err := syscall.Setrlimit(syscall.RLIMIT_NOFILE, &limit); err != nil {
			t.Fatal(err)
		}
		if err := chownTree(dir, os.Getuid(), os.Getgid(), nil); err != nil {
			t.Fatal(err)
		}
		return
	}
	dir := t.TempDir()
	deepest := filepath.Join(dir, strings.Repeat("d/", 4*maxHeldDirs))
	if err := os.MkdirAll(deepest, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestChownTreeHoldsBoundedDescriptors$")
	cmd.Env = append(os.Environ(), "AETHER_TEST_CHOWN_FD_CHILD="+dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("walk under a %d descriptor limit: %v\n%s", maxHeldDirs+32, err, out)
	}
}

// The pass hands the run user everything it does not own however deep, past
// the directories it keeps open too, chowns a symlink itself rather than its
// target, leaves a hard-linked file alone, and does not touch an entry the
// user already owns, whose inode a chown would rewrite.
func TestChownTreeOwnershipRules(t *testing.T) {
	t.Parallel()
	if os.Geteuid() != 0 {
		t.Skip("ownership pass needs root to chown")
	}
	home := filepath.Join(t.TempDir(), "home")
	outside := filepath.Join(t.TempDir(), "outside")
	nested := filepath.Join(home, "a", "b", "c")
	deepest := filepath.Join(home, strings.Repeat("d/", 2*maxHeldDirs))
	for _, dir := range []string{nested, deepest, outside} {
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
	write(filepath.Join(deepest, "deepest"))
	write(filepath.Join(outside, "target"))
	write(filepath.Join(home, "linked"))
	if err := os.Link(filepath.Join(home, "linked"), filepath.Join(outside, "other-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(home, "a", "escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(deepest, "escape")); err != nil {
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
	for _, path := range []string{home, filepath.Join(home, "a"), nested, filepath.Join(nested, "deep"), filepath.Join(home, "a", "escape"),
		deepest, filepath.Join(deepest, "deepest"), filepath.Join(deepest, "escape")} {
		if st := stat(path); st.Uid != 1000 || st.Gid != 1000 {
			t.Errorf("%s owned by %d:%d, want 1000:1000", path, st.Uid, st.Gid)
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
