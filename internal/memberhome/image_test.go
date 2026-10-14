package memberhome

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
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

func TestSafeFileName(t *testing.T) {
	long := strings.Repeat("é", 100) + ".pdf"
	for _, tc := range []struct{ in, want string }{
		{"Q3 report.pdf", "Q3 report.pdf"},
		{"", "file"},
		{"..", "file"},
		{" .env", "env"},
		{"../a/b\\c", "_a_b_c"},
		{"x\x00y\n", "x_y_"},
		{"\u202egpj.exe", "_gpj.exe"},
		{"bad\xffbyte", "bad\ufffdbyte"},
		{long, strings.Repeat("é", 62) + ".pdf"},
	} {
		got := SafeFileName(tc.in)
		if got != tc.want || SafeFileName(got) != got || len(got) > maxFileNameBytes {
			t.Errorf("SafeFileName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	dir := strings.Repeat("a", 32)
	for _, invalid := range []string{"", "notes.txt", dir, dir + "/", dir + "/.env", dir + "/a/b", "../" + dir + "/a", strings.ToUpper(dir) + "/a", dir + "x/a"} {
		if ValidFileName(invalid) {
			t.Errorf("ValidFileName(%q) accepted", invalid)
		}
	}
}

func TestSaveFileKeepsSafeNameInPrivateGeneratedDirectory(t *testing.T) {
	manager, err := New(filepath.Join(t.TempDir(), "homes"), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("%PDF-1.7\n")
	rel, err := manager.SaveFile("member-1", "../Q3 report.pdf", data)
	if err != nil {
		t.Fatal(err)
	}
	name := strings.TrimPrefix(rel, terminalFileDir+"/")
	if filepath.Base(rel) != "_Q3 report.pdf" || !ValidFileName(name) {
		t.Fatalf("generated path = %q", rel)
	}
	size, mime, err := manager.StatFile("member-1", name)
	if err != nil || size != int64(len(data)) || mime != "application/pdf" {
		t.Fatalf("StatFile = %d, %q, %v", size, mime, err)
	}
	home, err := manager.Path("member-1")
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(home, filepath.FromSlash(rel))
	info, err := os.Stat(file)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("stored file = %v, %v", info, err)
	}
	outside := filepath.Join(t.TempDir(), "private.pdf")
	if err := os.WriteFile(outside, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, file); err != nil {
		t.Fatal(err)
	}
	if _, _, err := manager.StatFile("member-1", name); err == nil {
		t.Fatal("StatFile followed a symlink")
	}
	if _, err := manager.SaveFile("member-1", "big.bin", make([]byte, ConfigMaxFileBytes+1)); err == nil {
		t.Fatal("oversized file accepted")
	}
}
