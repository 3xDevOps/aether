package secretstore

import (
	"maps"
	"os"
	"path/filepath"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
)

func TestStoreKeepsSecretsInOwnerOnlyFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "workspace-secrets")
	s := New(dir)

	got, err := s.Get("ws_1")
	if err != nil || len(got) != 0 {
		t.Fatalf("Get before any Put = %v, %v, want empty", got, err)
	}
	want := map[string]string{"NPM_TOKEN": "value-one", "MULTI": "line one\nline two"}
	if err = s.Put("ws_1", want); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if got, err = s.Get("ws_1"); err != nil || !maps.Equal(got, want) {
		t.Fatalf("Get = %v, %v, want %v", got, err, want)
	}
	if other, err := s.Get("ws_2"); err != nil || len(other) != 0 {
		t.Fatalf("another workspace reads %v, %v, want empty", other, err)
	}
	for path, mode := range map[string]os.FileMode{dir: 0o700, filepath.Join(dir, "ws_1.json"): 0o600} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		if info.Mode().Perm() != mode {
			t.Errorf("%s mode = %o, want %o", path, info.Mode().Perm(), mode)
		}
	}

	if err := s.Put("ws_1", nil); err != nil {
		t.Fatalf("Put empty: %v", err)
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
		t.Fatalf("directory after removing every secret = %v, %v, want empty", entries, err)
	}
	if err := s.Delete("ws_1"); err != nil {
		t.Fatalf("Delete with nothing stored: %v", err)
	}
}

func TestStoreRefusesAnIDThatIsNotAFileName(t *testing.T) {
	s := New(t.TempDir())
	for _, id := range []string{"", ".", "..", "../ws_1", "a/b"} {
		if err := s.Put(domain.WorkspaceID(id), map[string]string{"A": "b"}); err == nil {
			t.Errorf("Put(%q) succeeded", id)
		}
		if _, err := s.Get(domain.WorkspaceID(id)); err == nil {
			t.Errorf("Get(%q) succeeded", id)
		}
	}
}
