package toolenv

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/store"
)

func TestDigestStableAndPreservesModes(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"one", "two"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "one", "tool"), []byte("hello"), 0o751); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "two", "data"), []byte("world"), 0o640); err != nil {
		t.Fatal(err)
	}
	d1, _, err := DigestTree(root)
	if err != nil {
		t.Fatal(err)
	}
	d2, _, err := DigestTree(root)
	if err != nil {
		t.Fatal(err)
	}
	if d1 != d2 {
		t.Fatalf("digest changed: %q != %q", d1, d2)
	}
	if err := os.Chmod(filepath.Join(root, "one", "tool"), 0o701); err != nil {
		t.Fatal(err)
	}
	d3, _, err := DigestTree(root)
	if err != nil {
		t.Fatal(err)
	}
	if d3 == d1 {
		t.Fatal("digest ignored file mode")
	}
}

func TestManagerStagingPromotionAndActiveHead(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	w := &domain.Workspace{Name: "w", Environment: domain.WorkspaceEnvironment{NeutralImage: true}}
	if err := db.CreateWorkspace(ctx, w); err != nil {
		t.Fatal(err)
	}
	m := &domain.Member{DisplayName: "m", TailnetLogin: "m@example.com", Role: domain.RoleCollaborator}
	if err := db.CreateMember(ctx, m); err != nil {
		t.Fatal(err)
	}
	mgr, err := NewManager(filepath.Join(t.TempDir(), "tools"), db)
	if err != nil {
		t.Fatal(err)
	}
	staging, err := mgr.CreateStaging(string(m.ID), string(w.ID))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(staging, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staging, "bin", "hello"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	snap, err := mgr.Promote(ctx, string(m.ID), string(w.ID), staging, domain.ToolManifest{Executable: "hello"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if snap.ID == "" || snap.Digest == "" {
		t.Fatalf("incomplete snapshot: %+v", snap)
	}
	path, err := mgr.ActivePath(ctx, m.ID, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(path, "bin", "hello")); err != nil {
		t.Fatal(err)
	}
	if got, err := os.Stat(filepath.Join(path, "bin", "hello")); err != nil || got.Mode().Perm() != 0o755 {
		t.Fatalf("mode lost: %v %v", got, err)
	}
}

func TestManagerRejectsTraversalAndSymlinkEscapeAndCleansStaging(t *testing.T) {
	mgr, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.CreateStaging("../member", "workspace"); !errors.Is(err, ErrInvalidIdentifier) {
		t.Fatalf("traversal error = %v", err)
	}
	staging, err := mgr.CreateStaging("member", "workspace")
	if err != nil {
		t.Fatal(err)
	}
	escape := filepath.Join(t.TempDir(), "escape")
	if err := os.WriteFile(escape, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(escape, filepath.Join(staging, "link")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := DigestTree(staging); !errors.Is(err, ErrSymlink) {
		t.Fatalf("symlink error = %v", err)
	}
	if err := mgr.CleanupStaging(staging); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(staging); !os.IsNotExist(err) {
		t.Fatalf("staging remains: %v", err)
	}
}

func TestManagerRollbackResetAndInterruptedPromotion(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	w := &domain.Workspace{Name: "w", Environment: domain.WorkspaceEnvironment{NeutralImage: true}}
	if err := db.CreateWorkspace(ctx, w); err != nil {
		t.Fatal(err)
	}
	m := &domain.Member{DisplayName: "m", TailnetLogin: "m@example.com", Role: domain.RoleCollaborator}
	if err := db.CreateMember(ctx, m); err != nil {
		t.Fatal(err)
	}
	mgr, err := NewManager(filepath.Join(t.TempDir(), "tools"), db)
	if err != nil {
		t.Fatal(err)
	}
	makeStaging := func(data string) string {
		s, e := mgr.CreateStaging(string(m.ID), string(w.ID))
		if e != nil {
			t.Fatal(e)
		}
		if e = os.MkdirAll(filepath.Join(s, "bin"), 0o755); e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(filepath.Join(s, "bin", "tool"), []byte(data), 0o755); e != nil {
			t.Fatal(e)
		}
		return s
	}
	a, err := mgr.Promote(ctx, string(m.ID), string(w.ID), makeStaging("a"), domain.ToolManifest{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	b, err := mgr.Promote(ctx, string(m.ID), string(w.ID), makeStaging("b"), domain.ToolManifest{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.Rollback(ctx, m.ID, w.ID, a.ID); err != nil {
		t.Fatal(err)
	}
	active, err := db.GetToolHead(ctx, m.ID, w.ID)
	if err != nil || active.ID != a.ID {
		t.Fatalf("rollback active = %+v, %v", active, err)
	}
	if err := mgr.Reset(ctx, m.ID, w.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetToolHead(ctx, m.ID, w.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("reset head = %v", err)
	}
	if _, err := os.Stat(filepath.Join(mgr.SnapshotRoot(), string(m.ID), string(w.ID), string(b.ID))); !os.IsNotExist(err) {
		t.Fatalf("reset did not remove snapshot: %v", err)
	}
}

func TestManagerStagingPathRejectsTraversal(t *testing.T) {
	mgr, err := NewManager(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.StagingPath(domain.MemberID("member"), domain.WorkspaceID("workspace"), "../escape"); err == nil {
		t.Fatal("expected traversal to be rejected")
	}
}
func TestManagerSnapshotPathResolvesExactOwnedSnapshot(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	w := &domain.Workspace{Name: "w", Environment: domain.WorkspaceEnvironment{NeutralImage: true}}
	if err := db.CreateWorkspace(ctx, w); err != nil {
		t.Fatal(err)
	}
	m := &domain.Member{DisplayName: "m", TailnetLogin: "m@example.com", Role: domain.RoleCollaborator}
	if err := db.CreateMember(ctx, m); err != nil {
		t.Fatal(err)
	}
	mgr, err := NewManager(filepath.Join(t.TempDir(), "tools"), db)
	if err != nil {
		t.Fatal(err)
	}
	makeStaging := func(data string) string {
		staging, e := mgr.CreateStaging(string(m.ID), string(w.ID))
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(filepath.Join(staging, "tool"), []byte(data), 0o755); e != nil {
			t.Fatal(e)
		}
		return staging
	}
	first, err := mgr.Promote(ctx, string(m.ID), string(w.ID), makeStaging("first"), domain.ToolManifest{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := mgr.Promote(ctx, string(m.ID), string(w.ID), makeStaging("second"), domain.ToolManifest{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	firstPath, err := mgr.SnapshotPath(ctx, m.ID, w.ID, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(mgr.SnapshotRoot(), string(m.ID), string(w.ID), string(first.ID)); firstPath != want {
		t.Fatalf("snapshot path = %q, want %q", firstPath, want)
	}
	activePath, err := mgr.ActivePath(ctx, m.ID, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if activePath == firstPath || activePath != filepath.Join(mgr.SnapshotRoot(), string(m.ID), string(w.ID), string(second.ID)) {
		t.Fatalf("active path = %q, want exact second snapshot path", activePath)
	}
	if _, err := mgr.SnapshotPath(ctx, domain.MemberID("other"), w.ID, first.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("wrong owner error = %v", err)
	}
	if _, err := mgr.SnapshotPath(ctx, m.ID, w.ID, domain.ToolSnapshotID("../escape")); !errors.Is(err, ErrInvalidIdentifier) {
		t.Fatalf("traversal error = %v", err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(firstPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, firstPath); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.SnapshotPath(ctx, m.ID, w.ID, first.ID); !errors.Is(err, ErrSymlink) {
		t.Fatalf("symlink escape error = %v", err)
	}
}

func TestManagerPromoteReusesDuplicateDigestSnapshot(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	w := &domain.Workspace{Name: "w", Environment: domain.WorkspaceEnvironment{NeutralImage: true}}
	if err := db.CreateWorkspace(ctx, w); err != nil {
		t.Fatal(err)
	}
	m := &domain.Member{DisplayName: "m", TailnetLogin: "m@example.com", Role: domain.RoleCollaborator}
	if err := db.CreateMember(ctx, m); err != nil {
		t.Fatal(err)
	}
	mgr, err := NewManager(filepath.Join(t.TempDir(), "tools"), db)
	if err != nil {
		t.Fatal(err)
	}
	makeStaging := func() string {
		staging, e := mgr.CreateStaging(string(m.ID), string(w.ID))
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(filepath.Join(staging, "tool"), []byte("same"), 0o755); e != nil {
			t.Fatal(e)
		}
		return staging
	}
	firstStaging := makeStaging()
	first, err := mgr.Promote(ctx, string(m.ID), string(w.ID), firstStaging, domain.ToolManifest{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	duplicateStaging := makeStaging()
	second, err := mgr.Promote(ctx, string(m.ID), string(w.ID), duplicateStaging, domain.ToolManifest{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID {
		t.Fatalf("duplicate snapshot ID = %q, want %q", second.ID, first.ID)
	}
	if _, err := os.Stat(duplicateStaging); err != nil {
		t.Fatalf("duplicate staging tree was removed: %v", err)
	}
	head, err := db.GetToolHead(ctx, m.ID, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if head.ID != first.ID {
		t.Fatalf("duplicate head = %q, want %q", head.ID, first.ID)
	}
}

func TestManagerResetPreservesMetadataWhenSnapshotCleanupFails(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	w := &domain.Workspace{Name: "w", Environment: domain.WorkspaceEnvironment{NeutralImage: true}}
	if err := db.CreateWorkspace(ctx, w); err != nil {
		t.Fatal(err)
	}
	m := &domain.Member{DisplayName: "m", TailnetLogin: "m@example.com", Role: domain.RoleCollaborator}
	if err := db.CreateMember(ctx, m); err != nil {
		t.Fatal(err)
	}
	mgr, err := NewManager(filepath.Join(t.TempDir(), "tools"), db)
	if err != nil {
		t.Fatal(err)
	}
	makeStaging := func(data string) string {
		staging, e := mgr.CreateStaging(string(m.ID), string(w.ID))
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(filepath.Join(staging, "tool"), []byte(data), 0o755); e != nil {
			t.Fatal(e)
		}
		return staging
	}
	first, err := mgr.Promote(ctx, string(m.ID), string(w.ID), makeStaging("first"), domain.ToolManifest{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Promote(ctx, string(m.ID), string(w.ID), makeStaging("second"), domain.ToolManifest{}, nil); err != nil {
		t.Fatal(err)
	}
	firstPath, err := mgr.snapshotPath(string(m.ID), string(w.ID), string(first.ID))
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(firstPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, firstPath); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Reset(ctx, m.ID, w.ID); !errors.Is(err, ErrSymlink) {
		t.Fatalf("reset cleanup error = %v", err)
	}
	if _, err := db.GetToolSnapshot(ctx, first.ID); err != nil {
		t.Fatalf("snapshot metadata lost after cleanup failure: %v", err)
	}
	if err := os.Remove(firstPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(firstPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Reset(ctx, m.ID, w.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetToolSnapshot(ctx, first.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("snapshot metadata remains after retry: %v", err)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("outside path changed during cleanup: %v", err)
	}
}
