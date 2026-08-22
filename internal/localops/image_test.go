package localops

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestScaffoldFilesRefusesExistingFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "Dockerfile")
	if err := os.WriteFile(path, []byte("keep me\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	written, err := ScaffoldFiles(dir, ScaffoldOptions{})
	if err == nil {
		t.Fatal("ScaffoldFiles succeeded with an existing Dockerfile")
	}
	if !errors.Is(err, ErrScaffoldExists) {
		t.Fatalf("refusal does not unwrap to ErrScaffoldExists: %v", err)
	}
	if want := "refusing to overwrite existing " + path + "; use --force"; err.Error() != want {
		t.Fatalf("refusal message = %q, want %q", err.Error(), want)
	}
	if written != nil {
		t.Fatalf("refusal returned written paths %v", written)
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != "keep me\n" {
		t.Fatalf("existing Dockerfile changed: %q", got)
	}
}

func TestScaffoldFilesWritesExpectedFiles(t *testing.T) {
	dir := t.TempDir()
	written, err := ScaffoldFiles(dir, ScaffoldOptions{DevContainer: true})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		filepath.Join(dir, "Dockerfile"),
		filepath.Join(dir, ".dockerignore"),
		filepath.Join(dir, ".devcontainer", "devcontainer.json"),
	}
	if len(written) != len(want) {
		t.Fatalf("written = %v, want %v", written, want)
	}
	for i, path := range want {
		if written[i] != path {
			t.Fatalf("written[%d] = %q, want %q", i, written[i], path)
		}
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if len(body) == 0 {
			t.Fatalf("%s is empty", path)
		}
	}
}

func TestScaffoldFilesForceOverwrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "Dockerfile")
	if err := os.WriteFile(path, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ScaffoldFiles(dir, ScaffoldOptions{Force: true}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != neutralScaffoldDockerfile {
		t.Fatalf("forced Dockerfile = %q", got)
	}
}

func TestScaffoldKinds(t *testing.T) {
	plain := t.TempDir()
	written, err := Scaffold(plain, "dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	if len(written) != 2 {
		t.Fatalf("dockerfile kind wrote %v, want 2 files", written)
	}

	dev := t.TempDir()
	written, err = Scaffold(dev, "devcontainer")
	if err != nil {
		t.Fatal(err)
	}
	if len(written) != 3 {
		t.Fatalf("devcontainer kind wrote %v, want 3 files", written)
	}
	if _, err := os.Stat(filepath.Join(dev, ".devcontainer", "devcontainer.json")); err != nil {
		t.Fatalf("devcontainer.json missing: %v", err)
	}

	if _, err := Scaffold(t.TempDir(), "vm-image"); err == nil {
		t.Fatal("Scaffold accepted an unknown kind")
	}
}
