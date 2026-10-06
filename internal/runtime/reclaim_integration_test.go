//go:build integration

package runtime

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestDockerRemoverDeletesRootOwnedFiles(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("needs an unprivileged test user: root deletes root-owned files directly")
	}
	t.Parallel()
	d := newTestDocker(t)
	checkout := filepath.Join(t.TempDir(), "checkout")
	if err := os.Mkdir(checkout, 0o755); err != nil {
		t.Fatal(err)
	}

	id := createContainer(t, d, Spec{
		Image:             testImage,
		User:              "0:0",
		WorktreeHostPath:  checkout,
		WorktreeMountPath: "/workspace",
		Command:           []string{"sh", "-c", "mkdir -p /workspace/.git/objects/ab && echo x > /workspace/.git/objects/ab/object"},
	})
	if err := d.Start(t.Context(), id); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	if status, err := d.Wait(t.Context(), id); err != nil || status.Code != 0 {
		t.Fatalf("Wait() = %+v, %v; want exit 0", status, err)
	}
	if err := os.RemoveAll(checkout); !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("os.RemoveAll as uid %d = %v, want permission denied", os.Getuid(), err)
	}

	if err := Remover(d, testImage)(t.Context(), checkout); err != nil {
		t.Fatalf("Remover() error: %v", err)
	}
	if _, err := os.Stat(checkout); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("stat after Remover = %v, want not exist", err)
	}
}
