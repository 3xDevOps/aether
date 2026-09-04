package memberhome

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
)

func TestPathValidatesAndCreatesMemberHome(t *testing.T) {
	root := filepath.Join(t.TempDir(), "homes")
	manager, err := New(root)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	got, err := manager.Path(domain.MemberID("member-1"))
	if err != nil {
		t.Fatalf("Path: %v", err)
	}
	want := filepath.Join(root, "member-1")
	if got != want {
		t.Fatalf("Path = %q, want %q", got, want)
	}
	info, err := os.Stat(got)
	if err != nil {
		t.Fatalf("stat member home: %v", err)
	}
	if !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatalf("member home mode/type = %v, want directory 0700", info.Mode())
	}

	for _, member := range []domain.MemberID{"", "../x", "-member", ".member", "member..x", "member/x", "é"} {
		t.Run("reject "+string(member), func(t *testing.T) {
			if _, err := manager.Path(member); err == nil {
				t.Fatal("Path accepted invalid member ID")
			}
		})
	}
}

func TestRemoveDeletesMemberHome(t *testing.T) {
	root := filepath.Join(t.TempDir(), "homes")
	manager, err := New(root)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	home, err := manager.Path("member-1")
	if err != nil {
		t.Fatalf("Path: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, "state"), []byte("ok"), 0o600); err != nil {
		t.Fatalf("write state: %v", err)
	}
	if err := manager.Remove("member-1"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatalf("member home stat error = %v, want not exists", err)
	}
	if err := manager.Remove("member-1"); err != nil {
		t.Fatalf("second Remove: %v", err)
	}
}

func TestMigrateLegacyHomesFlattensKnownHarnessHomes(t *testing.T) {
	root := filepath.Join(t.TempDir(), "homes")
	legacy := filepath.Join(root, "m1", "claude", ".claude")
	if err := os.MkdirAll(legacy, 0o700); err != nil {
		t.Fatalf("create legacy home: %v", err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "x"), []byte("token"), 0o600); err != nil {
		t.Fatalf("write legacy credential: %v", err)
	}
	other := filepath.Join(root, "m1", "other", "keep")
	if err := os.MkdirAll(filepath.Dir(other), 0o700); err != nil {
		t.Fatalf("create non-harness home: %v", err)
	}
	if err := os.WriteFile(other, []byte("safe"), 0o600); err != nil {
		t.Fatalf("write non-harness file: %v", err)
	}

	if err := MigrateLegacyHomes(root, []string{"claude"}); err != nil {
		t.Fatalf("MigrateLegacyHomes: %v", err)
	}
	migrated := filepath.Join(root, "m1", ".claude", "x")
	if contents, err := os.ReadFile(migrated); err != nil || string(contents) != "token" {
		t.Fatalf("migrated credential = %q, error = %v", contents, err)
	}
	if _, err := os.Stat(filepath.Join(root, "m1", "claude")); !os.IsNotExist(err) {
		t.Fatalf("legacy harness directory stat error = %v, want not exists", err)
	}
	if contents, err := os.ReadFile(other); err != nil || string(contents) != "safe" {
		t.Fatalf("non-harness file = %q, error = %v", contents, err)
	}

	if err := MigrateLegacyHomes(root, []string{"claude"}); err != nil {
		t.Fatalf("second MigrateLegacyHomes: %v", err)
	}
	if err := MigrateLegacyHomes(filepath.Join(root, "missing"), []string{"claude"}); err != nil {
		t.Fatalf("missing-root MigrateLegacyHomes: %v", err)
	}
}
