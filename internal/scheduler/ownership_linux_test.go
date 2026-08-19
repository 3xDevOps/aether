//go:build linux && integration

package scheduler

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/runtime"
)

// TestApplyRunOwnershipHardlinkSafe pins the hardlink-safe ownership pass:
// a checkout link to a bare-repo object inode is never chowned or chmodded
// - even after the checkout's git moved the link out of .git/objects -
// while ordinary checkout files, directories, and the credential home are
// handed to the run user. Requires root (chown to another uid).
func TestApplyRunOwnershipHardlinkSafe(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("ownership pass needs root to chown")
	}
	dir := t.TempDir()
	repos := filepath.Join(dir, "repos")
	e := newTestEnv(t, func(cfg *Config) { cfg.ReposDir = repos })

	// Workspace bare repo with one object file.
	bareObjects := filepath.Join(repos, string(e.ws.ID)+".git", "objects", "ab")
	if err := os.MkdirAll(bareObjects, 0o755); err != nil {
		t.Fatal(err)
	}
	object := filepath.Join(bareObjects, "cdef")
	if err := os.WriteFile(object, []byte("object-bytes"), 0o444); err != nil {
		t.Fatal(err)
	}

	// Checkout with a hardlink to the object - moved out of .git/objects,
	// as a repack in an earlier run would leave it - plus a normal file.
	checkout := filepath.Join(dir, "checkouts", "run-1")
	if err := os.MkdirAll(filepath.Join(checkout, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	movedLink := filepath.Join(checkout, ".git", "moved-object")
	if err := os.Link(object, movedLink); err != nil {
		t.Fatal(err)
	}
	normal := filepath.Join(checkout, "main.go")
	if err := os.WriteFile(normal, []byte("package main"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Credential home mount (writable) plus a read-only mount that must
	// be left alone.
	cred := filepath.Join(dir, "homes", "m1", "claude")
	if err := os.MkdirAll(cred, 0o700); err != nil {
		t.Fatal(err)
	}
	ro := filepath.Join(dir, "profiles", "m1")
	if err := os.MkdirAll(ro, 0o755); err != nil {
		t.Fatal(err)
	}
	mounts := []runtime.Mount{
		{HostPath: cred, ContainerPath: "/root/.claude"},
		{HostPath: ro, ContainerPath: "/opt/profile", ReadOnly: true},
	}

	run := &domain.Run{ID: "run-1", Worktree: checkout}
	if err := e.sched.applyRunOwnership(e.ws, run, mounts, "1000:1000"); err != nil {
		t.Fatalf("applyRunOwnership: %v", err)
	}

	stat := func(path string) *syscall.Stat_t {
		t.Helper()
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		return info.Sys().(*syscall.Stat_t)
	}

	// The moved hardlink (and through it the bare object) kept root
	// ownership, mode, and content.
	if st := stat(movedLink); st.Uid != 0 || st.Gid != 0 {
		t.Errorf("protected inode chowned to %d:%d", st.Uid, st.Gid)
	}
	info, err := os.Lstat(object)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o444 {
		t.Errorf("protected object mode = %v, want 0444", info.Mode().Perm())
	}
	if data, _ := os.ReadFile(object); string(data) != "object-bytes" {
		t.Errorf("protected object content changed: %q", data)
	}

	// Ordinary checkout files, directories (so the run can add objects),
	// and the credential home belong to the run user.
	for _, p := range []string{normal, checkout, filepath.Join(checkout, ".git"), cred} {
		if st := stat(p); st.Uid != 1000 || st.Gid != 1000 {
			t.Errorf("%s owned by %d:%d, want 1000:1000", p, st.Uid, st.Gid)
		}
	}
	// The read-only mount was not touched.
	if st := stat(ro); st.Uid != 0 || st.Gid != 0 {
		t.Errorf("read-only mount chowned to %d:%d", st.Uid, st.Gid)
	}

	// A second pass for a concurrent run of the same member+harness is a
	// no-op with the same mapping.
	if err := e.sched.applyRunOwnership(e.ws, run, mounts, "1000:1000"); err != nil {
		t.Fatalf("second applyRunOwnership: %v", err)
	}
	if st := stat(movedLink); st.Uid != 0 {
		t.Errorf("protected inode chowned on second pass")
	}
}

// TestApplyRunOwnershipRootIsNoop pins that root runs skip the pass.
func TestApplyRunOwnershipRootIsNoop(t *testing.T) {
	e := newTestEnv(t, nil)
	if err := e.sched.applyRunOwnership(e.ws, &domain.Run{}, nil, ""); err != nil {
		t.Fatalf("applyRunOwnership(root): %v", err)
	}
}
