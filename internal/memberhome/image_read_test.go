package memberhome

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func encodedTestImage(t *testing.T, extension string) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var buf bytes.Buffer
	var err error
	switch extension {
	case ".png":
		err = png.Encode(&buf, img)
	case ".jpg":
		err = jpeg.Encode(&buf, img, nil)
	case ".gif":
		err = gif.Encode(&buf, img, nil)
	default:
		t.Fatalf("unsupported test image extension %q", extension)
	}
	if err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestReadImageRoundTrip(t *testing.T) {
	for _, tc := range []struct{ ext, mime string }{
		{".png", "image/png"}, {".jpg", "image/jpeg"}, {".gif", "image/gif"},
	} {
		t.Run(tc.ext, func(t *testing.T) {
			manager, err := New(filepath.Join(t.TempDir(), "homes"), nil)
			if err != nil {
				t.Fatal(err)
			}
			data := encodedTestImage(t, tc.ext)
			ext, mime, err := ValidateImage(data)
			if err != nil || ext != tc.ext || mime != tc.mime {
				t.Fatalf("ValidateImage = %q, %q, %v", ext, mime, err)
			}
			path, err := manager.SaveImage("member-1", ext, data)
			if err != nil {
				t.Fatal(err)
			}
			name := filepath.Base(path)
			if !ValidImageName(name) {
				t.Fatalf("generated image name rejected: %q", name)
			}
			got, mime, err := manager.ReadImage("member-1", name)
			if err != nil || mime != tc.mime || !bytes.Equal(got, data) {
				t.Fatalf("ReadImage = %d bytes, %q, %v", len(got), mime, err)
			}
			// Even another allowed filename extension must not dictate MIME.
			home, err := manager.Path("member-1")
			if err != nil {
				t.Fatal(err)
			}
			renamed := strings.TrimSuffix(name, ext) + ".webp"
			if renameErr := os.Rename(filepath.Join(home, path), filepath.Join(home, terminalImageDir, renamed)); renameErr != nil {
				t.Fatal(renameErr)
			}
			got, mime, err = manager.ReadImage("member-1", renamed)
			if err != nil || mime != tc.mime || !bytes.Equal(got, data) {
				t.Fatalf("renamed ReadImage = %d bytes, %q, %v", len(got), mime, err)
			}
		})
	}
}

func TestReadImageRejectsUnsafeFiles(t *testing.T) {
	for _, kind := range []string{"missing", "empty", "invalid", "oversized", "directory", "symlink", "hardlink", "image directory symlink", "aether directory symlink", "home symlink"} {
		t.Run(kind, func(t *testing.T) {
			manager, err := New(filepath.Join(t.TempDir(), "homes"), nil)
			if err != nil {
				t.Fatal(err)
			}
			data := encodedTestImage(t, ".png")
			rel, err := manager.SaveImage("member-1", ".png", data)
			if err != nil {
				t.Fatal(err)
			}
			home, err := manager.Path("member-1")
			if err != nil {
				t.Fatal(err)
			}
			name := filepath.Base(rel)
			file := filepath.Join(home, rel)
			outside := filepath.Join(t.TempDir(), "private.png")
			if writeErr := os.WriteFile(outside, data, 0o600); writeErr != nil {
				t.Fatal(writeErr)
			}
			if removeErr := os.Remove(file); removeErr != nil {
				t.Fatal(removeErr)
			}
			switch kind {
			case "empty":
				err = os.WriteFile(file, nil, 0o600)
			case "invalid":
				err = os.WriteFile(file, []byte("private non-image content"), 0o600)
			case "oversized":
				large := make([]byte, MaxImageBytes+1)
				copy(large, data)
				err = os.WriteFile(file, large, 0o600)
			case "directory":
				err = os.Mkdir(file, 0o700)
			case "symlink":
				err = os.Symlink(outside, file)
			case "hardlink":
				err = os.Link(outside, file)
			case "image directory symlink", "aether directory symlink", "home symlink":
				dir := filepath.Dir(file)
				switch kind {
				case "aether directory symlink":
					dir = filepath.Join(home, ".aether")
				case "home symlink":
					dir = home
				}
				// Move the real directory outside the home, then link to it.
				// Even a link to an otherwise valid image must be refused.
				if writeErr := os.WriteFile(file, data, 0o600); writeErr != nil {
					t.Fatal(writeErr)
				}
				target := filepath.Join(t.TempDir(), "moved")
				if renameErr := os.Rename(dir, target); renameErr != nil {
					t.Fatal(renameErr)
				}
				err = os.Symlink(target, dir)
			}
			if err != nil {
				t.Fatal(err)
			}
			got, mime, err := manager.ReadImage("member-1", name)
			if err == nil || got != nil || mime != "" {
				t.Fatalf("unsafe ReadImage = %d bytes, %q, %v", len(got), mime, err)
			}
			unchanged, err := os.ReadFile(outside)
			if err != nil || !bytes.Equal(unchanged, data) {
				t.Fatalf("outside file changed: %v", err)
			}
		})
	}
}

func TestReadImageDoesNotCreateMissingHome(t *testing.T) {
	root := filepath.Join(t.TempDir(), "homes")
	manager, err := New(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	name := "image-" + strings.Repeat("a", 32) + ".png"
	if data, mime, err := manager.ReadImage("absent-member", name); err == nil || data != nil || mime != "" {
		t.Fatalf("missing-home ReadImage = %d bytes, %q, %v", len(data), mime, err)
	}
	if _, err := os.Stat(filepath.Join(root, "absent-member")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("read created missing home: %v", err)
	}
}

func TestReadImageRejectsArbitraryNames(t *testing.T) {
	manager, err := New(filepath.Join(t.TempDir(), "homes"), nil)
	if err != nil {
		t.Fatal(err)
	}
	rel, err := manager.SaveImage("member-1", ".png", encodedTestImage(t, ".png"))
	if err != nil {
		t.Fatal(err)
	}
	name := filepath.Base(rel)
	for _, invalid := range []string{"", ".", "..", "../" + name, "/" + name, rel, "x/" + name, "x\\" + name, strings.ToUpper(name), name + "\n", name + "\x00", strings.TrimSuffix(name, ".png") + ".svg", "image-abc.png"} {
		t.Run(invalid, func(t *testing.T) {
			if ValidImageName(invalid) {
				t.Fatalf("invalid generated name accepted: %q", invalid)
			}
			if data, mime, err := manager.ReadImage("member-1", invalid); err == nil || data != nil || mime != "" {
				t.Fatalf("invalid-name ReadImage = %d bytes, %q, %v", len(data), mime, err)
			}
		})
	}
	if _, _, err := manager.ReadImage("../member-1", name); err == nil {
		t.Fatal("invalid member accepted")
	}
}

func TestReadImageFileRejectsGrowthAfterStat(t *testing.T) {
	manager, err := New(filepath.Join(t.TempDir(), "homes"), nil)
	if err != nil {
		t.Fatal(err)
	}
	data := make([]byte, MaxImageBytes)
	copy(data, encodedTestImage(t, ".png"))
	rel, err := manager.SaveImage("member-1", ".png", data)
	if err != nil {
		t.Fatal(err)
	}
	if got, mime, readErr := manager.ReadImage("member-1", filepath.Base(rel)); readErr != nil || mime != "image/png" || !bytes.Equal(got, data) {
		t.Fatalf("exact limit ReadImage = %d bytes, %q, %v", len(got), mime, readErr)
	}
	root, err := manager.openExistingHome("member-1")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	f, info, err := openRegular(root, rel)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	writer, err := root.OpenFile(rel, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte{1}); err != nil {
		_ = writer.Close()
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	// Reuse the pre-growth descriptor stat exactly as ReadImage does.
	if got, err := readImageFile(f, info); err == nil || got != nil {
		t.Fatalf("growing image returned %d bytes, %v", len(got), err)
	}
}
