package gitengine

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

func fileCheckout(t *testing.T, e *Engine) string {
	t.Helper()
	checkout, err := os.MkdirTemp(e.cfg.CheckoutsDir, "files-")
	if err != nil {
		t.Fatal(err)
	}
	return checkout
}

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
func TestReadCheckoutFileRejectsSymlinks(t *testing.T) {
	e := newUnitEngine(t)
	ctx := context.Background()
	checkout := fileCheckout(t, e)
	if err := os.Mkdir(filepath.Join(checkout, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	if err := os.WriteFile(filepath.Join(checkout, ".git", "config"), []byte("url = ssh://host/data\n"), 0o600); err != nil {
		t.Fatalf("write git config: %v", err)
	}
	for _, test := range []struct {
		name   string
		target string
		path   string
	}{
		{name: "git metadata alias", target: ".git", path: "gitlink/config"},
		{name: "absolute escape", target: "/etc", path: "escape/passwd"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := os.Symlink(test.target, filepath.Join(checkout, strings.Split(test.path, "/")[0])); err != nil {
				t.Fatalf("symlink %s: %v", test.target, err)
			}
			if _, _, _, err := e.ReadFile(ctx, checkout, "", test.path, 0); !errors.Is(err, ErrInvalidPath) {
				t.Fatalf("ReadFile(%q) error = %v, want ErrInvalidPath", test.path, err)
			}
		})
	}
}

func TestReadCheckoutFileRejectsFIFO(t *testing.T) {
	e := newUnitEngine(t)
	ctx := context.Background()
	checkout := fileCheckout(t, e)
	if err := os.Mkdir(filepath.Join(checkout, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	pipe := filepath.Join(checkout, "pipe")
	if err := makeFIFO(pipe); err != nil {
		t.Skipf("FIFO is unavailable: %v", err)
	}
	result := make(chan error, 1)
	go func() {
		_, _, _, err := e.ReadFile(ctx, checkout, "", "pipe", 0)
		result <- err
	}()
	select {
	case err := <-result:
		if !errors.Is(err, ErrInvalidPath) {
			t.Fatalf("ReadFile FIFO error = %v, want ErrInvalidPath", err)
		}
	case <-time.After(time.Second):
		t.Fatal("ReadFile FIFO blocked instead of rejecting it")
	}
}

func TestReadFileMarksLateNULBinary(t *testing.T) {
	e := newUnitEngine(t)
	checkout := fileCheckout(t, e)
	if err := os.Mkdir(filepath.Join(checkout, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	content := append([]byte(strings.Repeat("a", 8<<10)), 0)
	content = append(content, '\n')
	if err := os.WriteFile(filepath.Join(checkout, "late.bin"), content, 0o600); err != nil {
		t.Fatal(err)
	}
	read, err := e.ReadFileMeta(context.Background(), checkout, "", "late.bin", MaxFileBytes)
	if err != nil {
		t.Fatal(err)
	}
	if !read.Binary || read.Writable || read.Revision != "" {
		t.Fatalf("late NUL metadata = binary %v writable %v revision %q, want read-only binary with no revision", read.Binary, read.Writable, read.Revision)
	}
}
func TestBareFilesUseHeadsRef(t *testing.T) {
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
	if writeErr := os.WriteFile(filepath.Join(source, "branch.txt"), []byte("branch\n"), 0o644); writeErr != nil {
		t.Fatalf("write branch file: %v", writeErr)
	}
	gitFileTest(t, source, "add", "branch.txt")
	gitFileTest(t, source, "commit", "-q", "-m", "branch")
	gitFileTest(t, source, "push", "-q", repo, "main")
	if removeErr := os.Remove(filepath.Join(source, "branch.txt")); removeErr != nil {
		t.Fatalf("remove branch file: %v", removeErr)
	}
	if writeErr := os.WriteFile(filepath.Join(source, "tag.txt"), []byte("tag\n"), 0o644); writeErr != nil {
		t.Fatalf("write tag file: %v", writeErr)
	}
	gitFileTest(t, source, "add", "-A")
	gitFileTest(t, source, "commit", "-q", "-m", "tag")
	gitFileTest(t, source, "tag", "-f", "main")
	gitFileTest(t, source, "push", "-q", repo, "refs/tags/main")

	entries, err := e.ListTree(ctx, repo, "main", "")
	if err != nil {
		t.Fatalf("ListTree: %v", err)
	}
	if len(entries) != 1 || entries[0].Name != "branch.txt" {
		t.Fatalf("ListTree = %+v, want branch.txt from refs/heads/main", entries)
	}
	content, _, _, err := e.ReadFile(ctx, repo, "main", "branch.txt", 0)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(content) != "branch\n" {
		t.Fatalf("ReadFile = %q, want branch content", content)
	}
}

func TestListCheckoutSkipsMissingAndSymlinkLeaves(t *testing.T) {
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
	for name, body := range map[string]string{"keep.txt": "keep\n", "deleted.txt": "gone\n"} {
		if writeErr := os.WriteFile(filepath.Join(source, name), []byte(body), 0o644); writeErr != nil {
			t.Fatalf("write %s: %v", name, writeErr)
		}
	}
	gitFileTest(t, source, "add", "-A")
	gitFileTest(t, source, "commit", "-q", "-m", "seed")
	gitFileTest(t, source, "push", "-q", repo, "main")
	checkout, _, err := e.CreateRunCheckout(ctx, "ws1", "run1", "main", "list files", "")
	if err != nil {
		t.Fatalf("CreateRunCheckout: %v", err)
	}
	if removeErr := os.Remove(filepath.Join(checkout, "deleted.txt")); removeErr != nil {
		t.Fatalf("delete tracked file: %v", removeErr)
	}
	if symlinkErr := os.Symlink("/etc", filepath.Join(checkout, "escape")); symlinkErr != nil {
		t.Fatalf("symlink escape: %v", symlinkErr)
	}

	entries, err := e.ListTree(ctx, checkout, "", "")
	if err != nil {
		t.Fatalf("ListTree: %v", err)
	}
	if len(entries) != 1 || entries[0].Name != "keep.txt" || entries[0].Kind != "file" {
		t.Fatalf("ListTree = %+v, want only keep.txt", entries)
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

	checkout, _, err := e.CreateRunCheckout(ctx, "ws1", "run1", "main", "read files", "")
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

	checkout, _, err := e.CreateRunCheckout(ctx, "ws1", "run1", "main", "diff files", "")
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

func TestFilesWriteCheckoutIsAtomicAndRevisionChecked(t *testing.T) {
	e := newUnitEngine(t)
	ctx := context.Background()
	repo, err := e.InitWorkspaceRepo(ctx, "ws1")
	if err != nil {
		t.Fatal(err)
	}
	source := t.TempDir()
	gitFileTest(t, source, "init", "-q", "-b", "main")
	gitFileTest(t, source, "config", "user.name", "Files Test")
	gitFileTest(t, source, "config", "user.email", "files@example.test")
	if err = os.WriteFile(filepath.Join(source, "run.sh"), []byte("old\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	gitFileTest(t, source, "add", "run.sh")
	gitFileTest(t, source, "commit", "-q", "-m", "seed")
	gitFileTest(t, source, "push", "-q", repo, "main")
	checkout, _, err := e.CreateRunCheckout(ctx, "ws1", "run1", "main", "write", "")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(filepath.Join(checkout, "run.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err = os.Link(filepath.Join(checkout, "run.sh"), filepath.Join(checkout, "copy.sh")); err != nil {
		t.Fatal(err)
	}
	read, err := e.FilesRead(ctx, "ws1", "run1", "", "run.sh", MaxFileBytes)
	if err != nil {
		t.Fatal(err)
	}
	if read.Revision == "" {
		t.Fatal("FilesRead returned no revision for text file")
	}
	external := filepath.Join(checkout, ".external-run.sh")
	if err = os.WriteFile(external, []byte("outside\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(external, filepath.Join(checkout, "run.sh")); err != nil {
		t.Fatal(err)
	}
	if _, err = e.FilesWrite(ctx, "ws1", "run1", "", "run.sh", []byte("lost\n"), read.Revision, domain.GitIdentity{}, nil); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale FilesWrite error = %v, want ErrRevisionConflict", err)
	}
	got, err := os.ReadFile(filepath.Join(checkout, "run.sh"))
	if err != nil || string(got) != "outside\n" {
		t.Fatalf("stale write changed checkout: %q (%v)", got, err)
	}
	current, err := e.FilesRead(ctx, "ws1", "run1", "", "run.sh", MaxFileBytes)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.FilesWrite(ctx, "ws1", "run1", "", "run.sh", []byte("new\n"), current.Revision, domain.GitIdentity{}, nil); err != nil {
		t.Fatal(err)
	}
	copy, err := os.ReadFile(filepath.Join(checkout, "copy.sh"))
	if err != nil || string(copy) != "old\n" {
		t.Fatalf("hardlink inode was mutated: %q (%v)", copy, err)
	}
	mode, err := os.Stat(filepath.Join(checkout, "run.sh"))
	if err != nil || mode.Mode().Perm() != 0o755 {
		t.Fatalf("replacement mode = %v (%v), want 0755", mode.Mode().Perm(), err)
	}
}

func TestFilesWriteBasePreservesTreeAndExecutableMode(t *testing.T) {
	e := newUnitEngine(t)
	ctx := context.Background()
	repo, err := e.InitWorkspaceRepo(ctx, "ws1")
	if err != nil {
		t.Fatal(err)
	}
	source := t.TempDir()
	gitFileTest(t, source, "init", "-q", "-b", "main")
	gitFileTest(t, source, "config", "user.name", "Files Test")
	gitFileTest(t, source, "config", "user.email", "files@example.test")
	for name, mode := range map[string]os.FileMode{"script.sh": 0o755, "keep.txt": 0o644} {
		if err = os.WriteFile(filepath.Join(source, name), []byte(name+"\n"), mode); err != nil {
			t.Fatal(err)
		}
	}
	gitFileTest(t, source, "add", "-A")
	gitFileTest(t, source, "commit", "-q", "-m", "seed")
	gitFileTest(t, source, "push", "-q", repo, "main")
	read, err := e.FilesRead(ctx, "ws1", "", "main", "script.sh", MaxFileBytes)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.FilesWrite(ctx, "ws1", "", "main", "script.sh", []byte("changed\n"), read.Revision, domain.GitIdentity{Name: "Ada", Email: "ada@example.test"}, nil); err != nil {
		t.Fatal(err)
	}
	script, _, _, err := e.ReadFile(ctx, repo, "main", "script.sh", MaxFileBytes)
	if err != nil || string(script) != "changed\n" {
		t.Fatalf("base script = %q (%v)", script, err)
	}
	keep, _, _, err := e.ReadFile(ctx, repo, "main", "keep.txt", MaxFileBytes)
	if err != nil || string(keep) != "keep.txt\n" {
		t.Fatalf("unrelated base file = %q (%v)", keep, err)
	}
	tree, err := e.git(ctx, repo, "ls-tree", "refs/heads/main", "--", "script.sh")
	if err != nil || !strings.HasPrefix(tree, "100755 blob ") {
		t.Fatalf("base executable mode = %q (%v), want 100755", tree, err)
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
