package memberhome

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
)

func TestSaveImageRefusesSymlinkedImageDirectory(t *testing.T) {
	root := filepath.Join(t.TempDir(), "homes")
	manager, err := New(root, t.TempDir(), nil)
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
	manager, err := New(filepath.Join(t.TempDir(), "homes"), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	data := encodedTestImage(t, ".png")
	path, err := manager.SaveImage("member-1", ".png", data)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.IsAbs(path) || filepath.Dir(path) != terminalImageDir || filepath.Ext(path) != ".png" || !ValidImageName(filepath.Base(path)) {
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
	if !bytes.Equal(image, data) {
		t.Fatalf("stored image bytes differ from the uploaded image")
	}
	info, err := os.Stat(filepath.Join(home, filepath.FromSlash(path)))
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("stored image mode/type = %v", info.Mode())
	}
}
