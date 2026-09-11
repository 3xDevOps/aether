package memberhome

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
)

func TestSaveImageRefusesSymlinkedImageDirectory(t *testing.T) {
	root := filepath.Join(t.TempDir(), "homes")
	manager, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	home, err := manager.Path(domain.MemberID("member-1"))
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if mkdirErr := os.Mkdir(outside, 0o700); mkdirErr != nil {
		t.Fatal(mkdirErr)
	}
	if symlinkErr := os.Symlink(outside, filepath.Join(home, ".aether")); symlinkErr != nil {
		t.Fatal(symlinkErr)
	}
	if _, saveErr := manager.SaveImage("member-1", ".png", []byte("image")); saveErr == nil {
		t.Fatal("SaveImage followed a symlinked destination directory")
	}
	entries, err := os.ReadDir(outside)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("symlink target was modified: %v", entries)
	}
}

func TestSaveImageCreatesPrivateGeneratedPath(t *testing.T) {
	manager, err := New(filepath.Join(t.TempDir(), "homes"))
	if err != nil {
		t.Fatal(err)
	}
	path, err := manager.SaveImage("member-1", ".png", []byte("image"))
	if err != nil {
		t.Fatal(err)
	}
	if filepath.IsAbs(path) || filepath.Dir(path) != terminalImageDir || filepath.Ext(path) != ".png" {
		t.Fatalf("generated path = %q", path)
	}
	home, err := manager.Path("member-1")
	if err != nil {
		t.Fatal(err)
	}
	image, err := os.ReadFile(filepath.Join(home, filepath.FromSlash(path)))
	if err != nil {
		t.Fatal(err)
	}
	if string(image) != "image" {
		t.Fatalf("stored image bytes = %q, want %q", image, "image")
	}
	info, err := os.Stat(filepath.Join(home, filepath.FromSlash(path)))
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("stored image mode/type = %v", info.Mode())
	}
}
