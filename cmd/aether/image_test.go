package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInitImageScaffoldRefusesExistingFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "Dockerfile")
	if err := os.WriteFile(path, []byte("keep me\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := initImageScaffold(dir, imageScaffoldOptions{})
	if err == nil {
		t.Fatal("initImageScaffold succeeded with an existing Dockerfile")
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != "keep me\n" {
		t.Fatalf("existing Dockerfile changed: %q", got)
	}
}

func TestInitImageScaffoldOutputIsDeterministic(t *testing.T) {
	first, second := t.TempDir(), t.TempDir()
	if err := initImageScaffold(first, imageScaffoldOptions{DevContainer: true}); err != nil {
		t.Fatal(err)
	}
	if err := initImageScaffold(second, imageScaffoldOptions{DevContainer: true}); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"Dockerfile", ".dockerignore", filepath.Join(".devcontainer", "devcontainer.json")} {
		left, err := os.ReadFile(filepath.Join(first, name))
		if err != nil {
			t.Fatal(err)
		}
		right, err := os.ReadFile(filepath.Join(second, name))
		if err != nil {
			t.Fatal(err)
		}
		if string(left) != string(right) {
			t.Errorf("%s differs between scaffolds", name)
		}
	}
}

func TestInitImageScaffoldForceOverwrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "Dockerfile")
	if err := os.WriteFile(path, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := initImageScaffold(dir, imageScaffoldOptions{Force: true}); err != nil {
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

func TestWorkspaceEnvironmentDefaultsToNeutralImage(t *testing.T) {
	env := workspaceEnvironment("")
	if !env.NeutralImage || env.CustomImage != "" {
		t.Fatalf("empty image environment = %#v, want neutral selection", env)
	}
}
func TestWorkspaceInitRequiresName(t *testing.T) {
	err := workspaceInit(nil)
	if err == nil {
		t.Fatal("workspace init without a name succeeded")
	}
	if got := err.Error(); got != "usage: aether workspace init <name> [--image <image>]" {
		t.Fatalf("workspace init usage = %q", got)
	}
}
