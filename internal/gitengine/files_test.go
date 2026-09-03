package gitengine

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
)

func TestFilePathsRejectTraversal(t *testing.T) {
	e := newUnitEngine(t)
	ctx := context.Background()
	repo := filepath.Join(t.TempDir(), "repo.git")
	bad := []string{"../x", "/etc/passwd", ".git/config"}
	for _, path := range bad {
		t.Run(path, func(t *testing.T) {
			if err := ValidatePath(path); err == nil {
				t.Fatalf("ValidatePath(%q) accepted traversal", path)
			}
			if _, _, _, err := e.ReadFile(ctx, repo, "main", path, 0); !errors.Is(err, ErrInvalidPath) {
				t.Fatalf("ReadFile(%q) error = %v, want ErrInvalidPath", path, err)
			}
			if _, err := e.ListTree(ctx, repo, "main", path); !errors.Is(err, ErrInvalidPath) {
				t.Fatalf("ListTree(%q) error = %v, want ErrInvalidPath", path, err)
			}
		})
	}
}

func TestReadFileRunCheckoutShowsUncommittedEdit(t *testing.T) {
	e := newUnitEngine(t)
	ctx := context.Background()
	repo, err := e.InitWorkspaceRepo(ctx, domain.WorkspaceID("ws1"))
	if err != nil {
		t.Fatalf("InitWorkspaceRepo: %v", err)
	}
	source := t.TempDir()
	gitFileTest(t, source, "init", "-q", "-b", "main")
	gitFileTest(t, source, "config", "user.name", "Files Test")
	gitFileTest(t, source, "config", "user.email", "files@example.test")
	if writeErr := os.WriteFile(filepath.Join(source, "file.txt"), []byte("base\n"), 0o644); writeErr != nil {
		t.Fatalf("write seed: %v", writeErr)
	}
	gitFileTest(t, source, "add", "file.txt")
	gitFileTest(t, source, "commit", "-q", "-m", "seed")
	gitFileTest(t, source, "push", "-q", repo, "main")

	checkout, _, err := e.CreateRunCheckout(ctx, "ws1", "run1", "main", "read files")
	if err != nil {
		t.Fatalf("CreateRunCheckout: %v", err)
	}
	if writeErr := os.WriteFile(filepath.Join(checkout, "file.txt"), []byte("uncommitted\n"), 0o644); writeErr != nil {
		t.Fatalf("write edit: %v", writeErr)
	}
	content, truncated, binary, err := e.ReadFile(ctx, checkout, "", "file.txt", MaxFileBytes)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if got := string(content); got != "uncommitted\n" {
		t.Errorf("ReadFile content = %q, want uncommitted edit", got)
	}
	if truncated || binary {
		t.Errorf("ReadFile metadata = truncated %v, binary %v", truncated, binary)
	}
}

func TestListTreeAndFileDiff(t *testing.T) {
	e := newUnitEngine(t)
	ctx := context.Background()
	repo, err := e.InitWorkspaceRepo(ctx, domain.WorkspaceID("ws1"))
	if err != nil {
		t.Fatalf("InitWorkspaceRepo: %v", err)
	}
	source := t.TempDir()
	gitFileTest(t, source, "init", "-q", "-b", "main")
	gitFileTest(t, source, "config", "user.name", "Files Test")
	gitFileTest(t, source, "config", "user.email", "files@example.test")
	for name, body := range map[string]string{"file.txt": "base\n", "pkg/nested.go": "package pkg\n"} {
		if mkdirErr := os.MkdirAll(filepath.Dir(filepath.Join(source, name)), 0o755); mkdirErr != nil {
			t.Fatalf("mkdir seed: %v", mkdirErr)
		}
		if writeErr := os.WriteFile(filepath.Join(source, name), []byte(body), 0o644); writeErr != nil {
			t.Fatalf("write seed: %v", writeErr)
		}
	}
	gitFileTest(t, source, "add", "-A")
	gitFileTest(t, source, "commit", "-q", "-m", "seed")
	gitFileTest(t, source, "push", "-q", repo, "main")

	entries, err := e.ListTree(ctx, repo, "main", "")
	if err != nil {
		t.Fatalf("ListTree root: %v", err)
	}
	if len(entries) != 2 || entries[0].Name != "file.txt" || entries[0].Kind != "file" ||
		entries[1].Name != "pkg" || entries[1].Kind != "dir" {
		t.Fatalf("ListTree root = %+v", entries)
	}
	entries, err = e.ListTree(ctx, repo, "main", "pkg")
	if err != nil {
		t.Fatalf("ListTree pkg: %v", err)
	}
	if len(entries) != 1 || entries[0].Name != "nested.go" || entries[0].Kind != "file" {
		t.Fatalf("ListTree pkg = %+v", entries)
	}

	checkout, _, err := e.CreateRunCheckout(ctx, "ws1", "run1", "main", "diff files")
	if err != nil {
		t.Fatalf("CreateRunCheckout: %v", err)
	}
	if writeErr := os.WriteFile(filepath.Join(checkout, "file.txt"), []byte("edited\n"), 0o644); writeErr != nil {
		t.Fatalf("write edit: %v", writeErr)
	}
	patch, err := e.FileDiff(ctx, "run1", "file.txt")
	if err != nil {
		t.Fatalf("FileDiff: %v", err)
	}
	if patch.Truncated || !strings.Contains(patch.Text, "diff --git a/file.txt b/file.txt") ||
		!strings.Contains(patch.Text, "+edited") {
		t.Fatalf("FileDiff = truncated %v, patch %q", patch.Truncated, patch.Text)
	}
}

func gitFileTest(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}
